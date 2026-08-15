package compute

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoUnscopedMemoryBucketReads guards the property the earlier
// TestNoUnscopedVectorSearch was too narrow to protect.
//
// That test asserted every *VectorSearch call* passes an Audience, and
// it passed for weeks while `memory_search` leaked every owner's
// records through a second path: runSubstringSearch walked
// BucketEpisodicRecords directly, never touching vector search at all.
// Worse, the semantic path falls back to substring when embedding
// fails and *augments* with substring matches when it under-delivers —
// so on a node with no embedder the tool was simply unscoped.
//
// The lesson is that the invariant is not "calls to this function are
// scoped". It is "nothing reads a memory bucket without deciding who
// may see the result". So this guards the bucket, not the function:
// any file that reads BucketEpisodicRecords or BucketVectorRecords
// must mention an audience somewhere.
//
// Deliberately coarse — file-level rather than call-level — because a
// precise dataflow check would be a small analyser and this only has
// to be harder to get wrong than the last one was.
func TestNoUnscopedMemoryBucketReads(t *testing.T) {
	t.Parallel()

	const (
		episodic = "BucketEpisodicRecords"
		vectors  = "BucketVectorRecords"
	)
	// Evidence that a file thought about visibility at all.
	audienceMarkers := []string{
		"Audience", "audience", "AllowsEpisodic", "AllowsVector", "readAudience",
	}

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(src)
		if !strings.Contains(text, episodic) && !strings.Contains(text, vectors) {
			continue
		}
		// Parsed so a bucket named only in a comment does not count as
		// a read — the comment explaining this rule would otherwise
		// trip it.
		file, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		var reads bool
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if sel.Sel.Name == episodic || sel.Sel.Name == vectors {
				reads = true
			}
			return true
		})
		if !reads {
			continue
		}
		var scoped bool
		for _, m := range audienceMarkers {
			if strings.Contains(text, m) {
				scoped = true
				break
			}
		}
		if !scoped {
			t.Errorf("%s reads a memory bucket and never mentions an audience.\n"+
				"    Every read has to decide who may see the result — see\n"+
				"    internal/memory/visibility.go. Scoping the vector index does\n"+
				"    not scope a reader that walks the bucket directly.", path)
		}
	}
}
