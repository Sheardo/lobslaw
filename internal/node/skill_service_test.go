package node

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/raft"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jmylchreest/lobslaw/internal/memory"
	"github.com/jmylchreest/lobslaw/internal/skills"
	"github.com/jmylchreest/lobslaw/pkg/crypto"
	lobslawv1 "github.com/jmylchreest/lobslaw/pkg/proto/lobslaw/v1"
)

// Installing skills on a running cluster. The BYTES travel, not a
// path: the CLI runs on somebody's laptop and the cluster is
// elsewhere, so a service taking a directory would be reading one that
// exists perfectly well on the wrong machine.

func skillSvc(t *testing.T, policy skills.SigningPolicy, verifier *skills.Verifier) *skillService {
	t.Helper()
	dir := t.TempDir()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	store, err := memory.OpenStore(filepath.Join(dir, "state.db"), key)
	if err != nil {
		t.Fatal(err)
	}
	_, inmem := raft.NewInmemTransport("svc-node")
	node, err := memory.NewRaft(memory.RaftConfig{
		NodeID: "svc-node", LocalAddr: "svc-node",
		DataDir: dir, Bootstrap: true, Transport: inmem,
	}, memory.NewFSM(store))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = node.Shutdown()
		_ = store.Close()
	})
	if err := node.WaitForLeader(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	skillStore, err := memory.NewSkillStore(node, store)
	if err != nil {
		t.Fatal(err)
	}
	return &skillService{store: skillStore, policy: policy, verifier: verifier}
}

func bundleRequest(manifest, handler string) *lobslawv1.ImportSkillRequest {
	return &lobslawv1.ImportSkillRequest{
		Name: "tidy", Version: "1.2.3",
		Tier:         lobslawv1.SkillTier_SKILL_TIER_OPERATOR,
		ManifestYaml: []byte(manifest),
		Files:        map[string][]byte{"handler.py": []byte(handler)},
		Activate:     true,
	}
}

func plainManifest(handler string) string {
	sum := sha256.Sum256([]byte(handler))
	return "name: tidy\nversion: 1.2.3\nruntime: python\nhandler: handler.py\n" +
		"handler_sha256: " + hex.EncodeToString(sum[:]) + "\n"
}

func TestABundleImportsOverTheWire(t *testing.T) {
	t.Parallel()
	svc := skillSvc(t, skills.SigningOff, nil)
	resp, err := svc.ImportSkill(context.Background(), bundleRequest(plainManifest("print('hi')"), "print('hi')"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetSkill().GetName() != "tidy" || !resp.GetSkill().GetActive() {
		t.Errorf("record = %+v", resp.GetSkill())
	}
}

// Import then export must produce the same bytes, or a signed skill
// cannot survive the trip.
func TestTheRoundTripOverTheServiceIsByteIdentical(t *testing.T) {
	t.Parallel()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := "print('hi')"
	// Not already-normalised: a comment and trailing whitespace, both
	// of which a re-encode anywhere on this path would tidy away.
	sum := sha256.Sum256([]byte(handler))
	manifest := "# publisher: example.com\nversion: 1.2.3\nname: tidy\n" +
		"runtime: python\nhandler: handler.py\n" +
		"handler_sha256: " + hex.EncodeToString(sum[:]) + "   \n"
	sig := ed25519.Sign(priv, []byte(manifest))

	verifier := skills.NewVerifier()
	if err := verifier.AddKey("example", pub); err != nil {
		t.Fatal(err)
	}
	svc := skillSvc(t, skills.SigningRequire, verifier)

	req := bundleRequest(manifest, handler)
	req.Tier = lobslawv1.SkillTier_SKILL_TIER_SIGNED
	req.ManifestSig = sig
	if _, err := svc.ImportSkill(context.Background(), req); err != nil {
		t.Fatal(err)
	}

	out, err := svc.ExportSkill(context.Background(),
		&lobslawv1.ExportSkillRequest{Name: "tidy", Version: "1.2.3"})
	if err != nil {
		t.Fatal(err)
	}
	if string(out.GetManifestYaml()) != manifest {
		t.Errorf("manifest changed:\n got %q\nwant %q", out.GetManifestYaml(), manifest)
	}
	if !ed25519.Verify(pub, out.GetManifestYaml(), out.GetManifestSig()) {
		t.Error("the signature no longer verifies after a round trip through the service")
	}
	if string(out.GetFiles()["handler.py"]) != handler {
		t.Errorf("handler = %q", out.GetFiles()["handler.py"])
	}
}

// --- validation at the door -------------------------------------------

// Parsed through the REAL loader before storing, so an import is held
// to exactly the standard a load is. Otherwise the skill replicates to
// every node and fails to load on all of them.
func TestAnUnsignedBundleIsRefusedUnderSigningRequire(t *testing.T) {
	t.Parallel()
	svc := skillSvc(t, skills.SigningRequire, skills.NewVerifier())
	_, err := svc.ImportSkill(context.Background(),
		bundleRequest(plainManifest("print('hi')"), "print('hi')"))
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %v (err = %v)", status.Code(err), err)
	}
}

// A signed manifest pinning no handler digest covers no executable
// content. Verifying the signature by hand here would admit it; going
// through ParseWithPolicy does not.
func TestASignedBundleWithNoHandlerDigestIsRefused(t *testing.T) {
	t.Parallel()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	manifest := "name: tidy\nversion: 1.2.3\nruntime: python\nhandler: handler.py\n"
	verifier := skills.NewVerifier()
	if err := verifier.AddKey("example", pub); err != nil {
		t.Fatal(err)
	}
	svc := skillSvc(t, skills.SigningRequire, verifier)

	req := bundleRequest(manifest, "print('hi')")
	req.ManifestSig = ed25519.Sign(priv, []byte(manifest))
	_, err = svc.ImportSkill(context.Background(), req)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %v (err = %v)", status.Code(err), err)
	}
	if !strings.Contains(err.Error(), "handler_sha256") {
		t.Errorf("err = %q; it does not say what is missing", err)
	}
}

// A bundle arriving over the wire is less trustworthy than one read
// from a local directory, not more.
func TestABundlePathCannotEscape(t *testing.T) {
	t.Parallel()
	svc := skillSvc(t, skills.SigningOff, nil)
	req := bundleRequest(plainManifest("print('hi')"), "print('hi')")
	req.Files["../escape.sh"] = []byte("rm -rf /")

	_, err := svc.ImportSkill(context.Background(), req)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %v (err = %v)", status.Code(err), err)
	}
}

func TestImportRequiresNameVersionAndManifest(t *testing.T) {
	t.Parallel()
	svc := skillSvc(t, skills.SigningOff, nil)
	base := bundleRequest(plainManifest("x"), "x")
	for name, mutate := range map[string]func(*lobslawv1.ImportSkillRequest){
		"no name":     func(r *lobslawv1.ImportSkillRequest) { r.Name = "" },
		"no version":  func(r *lobslawv1.ImportSkillRequest) { r.Version = "" },
		"no manifest": func(r *lobslawv1.ImportSkillRequest) { r.ManifestYaml = nil },
	} {
		req := bundleRequest(string(base.GetManifestYaml()), "x")
		mutate(req)
		if _, err := svc.ImportSkill(context.Background(), req); status.Code(err) != codes.InvalidArgument {
			t.Errorf("%s: code = %v", name, status.Code(err))
		}
	}
}

// --- listing and removal ----------------------------------------------

func TestListAndRemove(t *testing.T) {
	t.Parallel()
	svc := skillSvc(t, skills.SigningOff, nil)
	ctx := context.Background()
	if _, err := svc.ImportSkill(ctx, bundleRequest(plainManifest("x"), "x")); err != nil {
		t.Fatal(err)
	}

	listed, err := svc.ListSkills(ctx, &lobslawv1.ListSkillsRequest{ActiveOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.GetSkills()) != 1 {
		t.Fatalf("listed %d", len(listed.GetSkills()))
	}

	if _, err := svc.RemoveSkill(ctx, &lobslawv1.RemoveSkillRequest{
		Name: "tidy", Version: "1.2.3",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ExportSkill(ctx, &lobslawv1.ExportSkillRequest{
		Name: "tidy", Version: "1.2.3",
	}); status.Code(err) != codes.NotFound {
		t.Errorf("code = %v after removal, want NotFound", status.Code(err))
	}
}

// "Not found" and "too large" send an operator to different places —
// one is a typo, the other is a bundle needing a file moved to
// storage.
func TestSkillStoreErrorsMapToDistinctCodes(t *testing.T) {
	t.Parallel()
	svc := skillSvc(t, skills.SigningOff, nil)
	_, err := svc.ExportSkill(context.Background(),
		&lobslawv1.ExportSkillRequest{Name: "nope", Version: "1.0.0"})
	if status.Code(err) != codes.NotFound {
		t.Errorf("unknown skill: code = %v", status.Code(err))
	}
}

// A node without raft has no store, and every method has to say so
// rather than panicking on a nil.
func TestEveryMethodRefusesWithoutAStore(t *testing.T) {
	t.Parallel()
	svc := &skillService{}
	ctx := context.Background()
	calls := map[string]func() error{
		"import": func() error {
			_, err := svc.ImportSkill(ctx, bundleRequest(plainManifest("x"), "x"))
			return err
		},
		"export": func() error {
			_, err := svc.ExportSkill(ctx, &lobslawv1.ExportSkillRequest{Name: "a", Version: "1"})
			return err
		},
		"list": func() error {
			_, err := svc.ListSkills(ctx, &lobslawv1.ListSkillsRequest{})
			return err
		},
		"remove": func() error {
			_, err := svc.RemoveSkill(ctx, &lobslawv1.RemoveSkillRequest{Name: "a", Version: "1"})
			return err
		},
	}
	for name, call := range calls {
		if code := status.Code(call()); code != codes.FailedPrecondition {
			t.Errorf("%s: code = %v, want FailedPrecondition", name, code)
		}
	}
}
