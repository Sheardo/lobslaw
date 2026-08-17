package node

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/jmylchreest/lobslaw/internal/memory"
	"github.com/jmylchreest/lobslaw/internal/skills"
	"github.com/jmylchreest/lobslaw/internal/storage"
	lobslawv1 "github.com/jmylchreest/lobslaw/pkg/proto/lobslaw/v1"
)

// The skills mount becomes an IMPORT SOURCE rather than a live one.
//
// Files were playing three roles pointed at one directory, and the
// reconciliation rule between two authorities is the complexity R18
// calls self-inflicted. So the directory keeps its job — drop a skill
// in and it works — but what it feeds is the store, and the store is
// the only thing the registry loads from.
//
// The semantic change an operator has to know about: DELETING THE FILE
// NO LONGER REMOVES THE SKILL. It is in the store, replicated to every
// node, and it comes out with `lobslaw skills remove`. That is said
// out loud at boot rather than left to be discovered.
//
// No leader gate. Importing is a raft write and writes forward to the
// leader, so any node with the mount can do it — which matters, because
// a mount is per-node storage and leader-gating would mean only the
// leader's copy of the directory was ever read. Two nodes importing
// identical content converge: the record is skipped when it already
// matches, and blobs are content-addressed.

// skillImporter reads a mount into the store.
type skillImporter struct {
	node  *Node
	root  string
	label string

	// removalReported keeps the delete-does-not-remove notice to once
	// per skill per process. It is a surprise worth stating, and one
	// worth stating only once.
	removalReported sync.Map
}

// startSkillMountImport replaces the old registry watcher.
func (n *Node) startSkillMountImport(ctx context.Context) error {
	if n.skillStore == nil || n.storageMgr == nil || n.cfg.Skills.StorageLabel == "" {
		return nil
	}
	label := n.cfg.Skills.StorageLabel
	root, err := n.storageMgr.Resolve(label)
	if err != nil {
		return fmt.Errorf("skills: resolve mount %q: %w", label, err)
	}
	imp := &skillImporter{node: n, root: root, label: label}

	if err := imp.importAll(ctx); err != nil {
		n.log.Error("skills: initial mount import failed", "label", label, "err", err)
	}

	ch, err := n.storageMgr.Watch(ctx, label, storage.WatchOpts{
		Recursive: true,
		Include:   []string{"manifest.yaml"},
	})
	if err != nil {
		return fmt.Errorf("skills: watch mount %q: %w", label, err)
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-ch:
				if !ok {
					return
				}
				// A full re-import rather than per-event surgery. It is
				// idempotent — unchanged skills are skipped without a
				// write — so the cost of the simple version is a
				// directory read.
				if err := imp.importAll(ctx); err != nil {
					n.log.Warn("skills: mount import failed", "label", label, "err", err)
				}
			}
		}
	}()

	n.log.Info("skills: the mount is an import source; the store is what loads",
		"label", label, "root", root,
		"note", "deleting a file no longer removes the skill — use `lobslaw skills remove`")
	return nil
}

// importAll walks the mount and imports each skill directory.
func (i *skillImporter) importAll(ctx context.Context) error {
	entries, err := os.ReadDir(i.root)
	if err != nil {
		if os.IsNotExist(err) {
			// A configured mount that is not materialised yet is the
			// normal state on a node that has just started, not an
			// error to report every tick.
			return nil
		}
		return err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(i.root, e.Name())
		if _, err := os.Stat(filepath.Join(dir, memory.ManifestFile)); err != nil {
			continue
		}
		if err := i.importOne(ctx, dir); err != nil {
			i.node.log.Warn("skills: could not import from the mount",
				"dir", dir, "err", err)
		}
	}
	i.reportRemovals(entries)
	return nil
}

// importOne parses and stores one skill directory.
//
// Parsed with the configured signing policy, so an unsigned skill in a
// SigningRequire deployment is refused HERE rather than replicated to
// every node and then failing to load on all of them. The feedback
// belongs where the file was dropped.
func (i *skillImporter) importOne(ctx context.Context, dir string) error {
	skill, err := skills.ParseWithPolicy(dir, i.node.skillSigningPolicy, i.node.skillVerifier)
	if err != nil {
		return err
	}
	name, version := skill.Manifest.Name, skill.Manifest.Version

	// Already stored and unchanged: no write. Without this the watcher
	// re-puts every skill on every filesystem event, which replicates
	// unchanged records to every node for the rest of the deployment's
	// life.
	if same, err := i.matchesStore(dir, name, version); err != nil {
		return err
	} else if same {
		return nil
	}

	tier := lobslawv1.SkillTier_SKILL_TIER_OPERATOR
	if skill.IsSigned {
		tier = lobslawv1.SkillTier_SKILL_TIER_SIGNED
	}
	if _, err := i.node.skillStore.Import(ctx, memory.ImportRequest{
		Dir: dir, Name: name, Version: version, Tier: tier,
		Source:     "mount:" + i.label + "/" + filepath.Base(dir),
		ImportedBy: "mount:" + i.label,
		Activate:   true,
	}); err != nil {
		return err
	}
	i.node.log.Info("skills: imported from the mount",
		"skill", name, "version", version, "tier", tier.String(), "signed", skill.IsSigned)
	return nil
}

// matchesStore reports whether the stored record already holds exactly
// what is on disk.
//
// Compared by the manifest bytes and every payload digest, not by
// version. A skill edited in place without its version moving is
// exactly what a drop-in directory is FOR during development, and a
// version check would make those edits invisible.
func (i *skillImporter) matchesStore(dir, name, version string) (bool, error) {
	rec, err := i.node.skillStore.Get(name, version)
	if err != nil {
		return false, nil //nolint:nilerr // not stored yet is not a failure
	}
	manifest, err := os.ReadFile(filepath.Join(dir, memory.ManifestFile)) //nolint:gosec // mount path
	if err != nil {
		return false, err
	}
	if string(manifest) != string(rec.GetManifestYaml()) {
		return false, nil
	}
	onDisk, err := collectMountFiles(dir)
	if err != nil {
		return false, err
	}
	if len(onDisk) != len(rec.GetFiles()) {
		return false, nil
	}
	for rel, content := range onDisk {
		if rec.GetFiles()[rel] != memory.Digest(content) {
			return false, nil
		}
	}
	return true, nil
}

// collectMountFiles reads the payloads a directory would import,
// mirroring what the store itself collects.
func collectMountFiles(dir string) (map[string][]byte, error) {
	out := map[string][]byte{}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if rel == memory.ManifestFile || rel == memory.SignatureFile {
			return nil
		}
		content, rerr := os.ReadFile(path) //nolint:gosec // walked from the mount
		if rerr != nil {
			return rerr
		}
		out[rel] = content
		return nil
	})
	return out, err
}

// reportRemovals says, once per skill, that a file deleted from the
// mount has not removed the skill.
//
// The one genuinely surprising consequence of the mount becoming an
// import source, so it is stated rather than left to be discovered
// when somebody deletes a directory and the skill keeps answering.
func (i *skillImporter) reportRemovals(entries []os.DirEntry) {
	onDisk := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() {
			onDisk[e.Name()] = true
		}
	}
	stored, err := i.node.skillStore.List()
	if err != nil {
		return
	}
	for _, rec := range stored {
		if rec.GetImportedBy() != "mount:"+i.label {
			continue
		}
		if onDisk[filepath.Base(rec.GetSource())] {
			continue
		}
		if _, seen := i.removalReported.LoadOrStore(rec.GetName(), struct{}{}); seen {
			continue
		}
		i.node.log.Info("skills: a skill's source directory is gone but the skill remains",
			"skill", rec.GetName(), "version", rec.GetVersion(),
			"remove_with", fmt.Sprintf("lobslaw skills remove %s %s", rec.GetName(), rec.GetVersion()))
	}
}
