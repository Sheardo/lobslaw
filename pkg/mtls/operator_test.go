package mtls

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"path/filepath"
	"testing"
	"time"
)

// An operator's laptop was given a NODE certificate, because that is
// the only kind there was. Three consequences: the laptop held
// material that could act as a cluster member, revoking one person
// meant rotating a node's identity, and every action they took was
// attributed to a host rather than to them.

// testCA writes a CA to a temp dir and loads it, the way the real
// flow does.
func testCA(t *testing.T) (*x509.Certificate, ed25519.PrivateKey) {
	t.Helper()
	dir := t.TempDir()
	certPath := filepath.Join(dir, "ca.pem")
	keyPath := filepath.Join(dir, "ca-key.pem")

	certPEM, keyPEM, err := GenerateCA(CAOpts{CommonName: "test-ca"})
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteCAFiles(certPath, keyPath, certPEM, keyPEM); err != nil {
		t.Fatal(err)
	}
	ca, key, err := LoadCA(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	return ca, key
}

func parseCert(t *testing.T, certPEM []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("no PEM block")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func signOperator(t *testing.T, name string) *x509.Certificate {
	t.Helper()
	caCert, caKey := testCA(t)
	certPEM, _, err := SignOperatorCert(caCert, caKey, SignOpts{NodeID: name})
	if err != nil {
		t.Fatal(err)
	}
	return parseCert(t, certPEM)
}

// THE CENTRAL PROPERTY. A node certificate carries ServerAuth because
// a node both dials its peers and serves them; that is exactly what
// makes a stolen node credential able to impersonate a member. An
// operator dials and is never dialled.
func TestAnOperatorCertCannotServe(t *testing.T) {
	t.Parallel()
	cert := signOperator(t, "alice")
	for _, usage := range cert.ExtKeyUsage {
		if usage == x509.ExtKeyUsageServerAuth {
			t.Error("an operator certificate carries ServerAuth; it could answer connections as a node")
		}
	}
	var canDial bool
	for _, usage := range cert.ExtKeyUsage {
		if usage == x509.ExtKeyUsageClientAuth {
			canDial = true
		}
	}
	if !canDial {
		t.Error("an operator certificate cannot dial, which makes it useless")
	}
}

// A node certificate must keep both, or this change breaks the
// cluster to secure the laptop.
func TestANodeCertStillServesAndDials(t *testing.T) {
	t.Parallel()
	caCert, caKey := testCA(t)
	certPEM, _, err := SignNodeCert(caCert, caKey, SignOpts{NodeID: "node-a"})
	if err != nil {
		t.Fatal(err)
	}
	cert := parseCert(t, certPEM)

	var server, client bool
	for _, u := range cert.ExtKeyUsage {
		switch u {
		case x509.ExtKeyUsageServerAuth:
			server = true
		case x509.ExtKeyUsageClientAuth:
			client = true
		}
	}
	if !server || !client {
		t.Errorf("node cert usages = %v; a node both serves and dials", cert.ExtKeyUsage)
	}
}

// ClientAuth alone does not stop it dialling raft — a peer dials as a
// client too — so the certificate must be identifiable as a person's.
func TestAnOperatorCertIsIdentifiable(t *testing.T) {
	t.Parallel()
	if !IsOperatorCert(signOperator(t, "alice")) {
		t.Error("an operator certificate is not recognisable as one")
	}
}

func TestANodeCertIsNotAnOperator(t *testing.T) {
	t.Parallel()
	caCert, caKey := testCA(t)
	certPEM, _, err := SignNodeCert(caCert, caKey, SignOpts{NodeID: "node-a"})
	if err != nil {
		t.Fatal(err)
	}
	if IsOperatorCert(parseCert(t, certPEM)) {
		t.Error("a node certificate reads as an operator; every node would be refused replication")
	}
	if IsOperatorCert(nil) {
		t.Error("a nil certificate reads as an operator")
	}
}

// The name identifies the PERSON, which is what makes an audit entry
// say who rather than which host.
func TestTheOperatorNameIsTheCommonName(t *testing.T) {
	t.Parallel()
	if got := signOperator(t, "alice").Subject.CommonName; got != "alice" {
		t.Errorf("CommonName = %q, want alice", got)
	}
}

// No DNS SAN: an operator is not reachable at a name, and a SAN is
// what a peer verifies when dialling. One would make the credential
// look addressable.
func TestAnOperatorCertHasNoDNSName(t *testing.T) {
	t.Parallel()
	if got := signOperator(t, "alice").DNSNames; len(got) != 0 {
		t.Errorf("DNSNames = %v; an operator is not reachable at a name", got)
	}
}

// A person's credential lives on a laptop that travels; a node's lives
// on a host somebody controls.
func TestAnOperatorCertExpiresSoonerByDefault(t *testing.T) {
	t.Parallel()
	caCert, caKey := testCA(t)

	opPEM, _, err := SignOperatorCert(caCert, caKey, SignOpts{NodeID: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	nodePEM, _, err := SignNodeCert(caCert, caKey, SignOpts{NodeID: "node-a"})
	if err != nil {
		t.Fatal(err)
	}
	op, node := parseCert(t, opPEM), parseCert(t, nodePEM)
	if !op.NotAfter.Before(node.NotAfter) {
		t.Errorf("operator expires %v, node %v; the travelling credential should be shorter-lived",
			op.NotAfter, node.NotAfter)
	}
}

func TestAnOperatorNeedsAName(t *testing.T) {
	t.Parallel()
	caCert, caKey := testCA(t)
	if _, _, err := SignOperatorCert(caCert, caKey, SignOpts{}); err == nil {
		t.Error("an operator certificate with no name was issued")
	}
}

// It must verify against the CA, or none of the above matters.
func TestAnOperatorCertVerifiesAgainstTheCA(t *testing.T) {
	t.Parallel()
	caCert, caKey := testCA(t)
	certPEM, _, err := SignOperatorCert(caCert, caKey, SignOpts{NodeID: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	if _, err := parseCert(t, certPEM).Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		t.Errorf("an operator certificate does not verify as a client: %v", err)
	}
}

// And it must NOT verify for server use, which is the property a
// hostile holder would try to exploit.
func TestAnOperatorCertDoesNotVerifyAsAServer(t *testing.T) {
	t.Parallel()
	caCert, caKey := testCA(t)
	certPEM, _, err := SignOperatorCert(caCert, caKey, SignOpts{NodeID: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	if _, err := parseCert(t, certPEM).Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err == nil {
		t.Error("an operator certificate verified for server use; it could impersonate a node")
	}
}

func TestAnExpiredValidityIsHonoured(t *testing.T) {
	t.Parallel()
	caCert, caKey := testCA(t)
	certPEM, _, err := SignOperatorCert(caCert, caKey, SignOpts{
		NodeID: "alice", ValidFor: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	cert := parseCert(t, certPEM)
	if d := cert.NotAfter.Sub(cert.NotBefore); d > 2*time.Hour {
		t.Errorf("validity = %v, want about an hour", d)
	}
}
