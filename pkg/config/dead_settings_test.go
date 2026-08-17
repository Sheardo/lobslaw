package config_test

import (
	"go/ast"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

// A setting nothing reads is worse than a setting that does not exist.
//
// An operator who writes `network_allow_cidr` into their config has
// restricted nothing, and the system will not tell them so — it parses
// the key, validates it, and drops it. A sweep in 2026-08 found
// thirty-five of these across [sandbox], [[storage.mounts]],
// [[compute.plugins]], [audit], [memory.snapshot] and [logging]. Every
// one had been written into examples/config.toml, most into DESIGN.md,
// and two into the end-user security documentation.
//
// This test exists so the thirty-sixth cannot land quietly.
//
// TYPE-CHECKED, NOT GREPPED. A textual search for ".Level" cannot tell
// LoggingConfig.Level from the dozen other structs in this tree with a
// field of that name — and that is not a hypothetical, because
// [logging] level and format were two of the thirty-five, and a
// name-based check would have missed exactly them.

const cfgPkg = "github.com/jmylchreest/lobslaw/pkg/config"

// Captured at init, before any test runs.
//
// Other tests in this package os.Chdir into temp directories and
// t.Setenv HOME — which relocates GOCACHE and makes `go list` fail. An
// analysis that loads the module must not depend on process state that
// somebody else's test legitimately owns, so it takes its own copy
// while the process is still pristine.
var (
	moduleRoot  string
	pristineEnv []string
)

func init() {
	wd, err := os.Getwd()
	if err != nil {
		panic("dead settings: getwd: " + err.Error())
	}
	moduleRoot = filepath.Join(wd, "..", "..")
	pristineEnv = os.Environ()
}

// deliberatelyUnread lists fields that nothing reads ON PURPOSE.
//
// Adding to it is a decision, visible in review, and it needs a reason
// on the line. An empty allowlist is the healthy state; this is not a
// place to park work.
var deliberatelyUnread = map[string]string{}

func TestEverySettingIsReadBySomething(t *testing.T) {
	if testing.Short() {
		t.Skip("loads the whole module with type information")
	}
	pkgs, err := packages.Load(&packages.Config{
		Mode: packages.NeedName | packages.NeedSyntax | packages.NeedTypes |
			packages.NeedTypesInfo | packages.NeedDeps | packages.NeedImports,
		Dir: moduleRoot,
		Env: pristineEnv,
	}, "./...")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if packages.PrintErrors(pkgs) > 0 {
		t.Fatal("the module does not type-check; fix that before reading this result")
	}

	declared := declaredSettings(t, pkgs)
	if len(declared) < 100 {
		// The analysis silently returning nothing would make this test
		// pass forever while checking not one thing.
		t.Fatalf("only found %d settings; the analysis is broken, not the config", len(declared))
	}
	read := readSettings(pkgs, declared)

	var dead []string
	for field := range declared {
		if read[field] || deliberatelyUnread[field] != "" {
			continue
		}
		dead = append(dead, field)
	}
	sort.Strings(dead)

	for _, d := range dead {
		t.Errorf("%s is parsed from config and read by nothing.\n"+
			"    Wire it up, delete it, or add it to deliberatelyUnread with a reason.", d)
	}

	// A stale allowlist entry is its own kind of lie: it says a field
	// is unread on purpose when somebody has since wired it up.
	for field := range deliberatelyUnread {
		if !declared[field] {
			t.Errorf("deliberatelyUnread names %s, which no longer exists", field)
		}
		if read[field] {
			t.Errorf("deliberatelyUnread names %s, but something reads it now; remove the entry", field)
		}
	}
}

// declaredSettings returns every koanf-tagged exported field in
// pkg/config, keyed "Struct.Field".
func declaredSettings(t *testing.T, pkgs []*packages.Package) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, p := range pkgs {
		if p.PkgPath != cfgPkg {
			continue
		}
		scope := p.Types.Scope()
		for _, name := range scope.Names() {
			tn, ok := scope.Lookup(name).(*types.TypeName)
			// An alias declares no fields of its own. Registering
			// SpeakConfig.Provider separately from ModalityOverride's
			// would report it dead forever, because every use resolves
			// to the underlying struct.
			if !ok || tn.IsAlias() {
				continue
			}
			st, ok := tn.Type().Underlying().(*types.Struct)
			if !ok {
				continue
			}
			for i := 0; i < st.NumFields(); i++ {
				if strings.Contains(st.Tag(i), "koanf:") && st.Field(i).Exported() {
					out[tn.Name()+"."+st.Field(i).Name()] = true
				}
			}
		}
	}
	return out
}

// readSettings marks every declared setting that non-test code outside
// pkg/config selects.
//
// Tests do not count. A field read only by a test of the parser proves
// the parser works, not that the setting does anything.
func readSettings(pkgs []*packages.Package, declared map[string]bool) map[string]bool {
	read := map[string]bool{}
	for _, p := range pkgs {
		if p.PkgPath == cfgPkg {
			continue
		}
		for _, file := range p.Syntax {
			if strings.HasSuffix(p.Fset.Position(file.Package).Filename, "_test.go") {
				continue
			}
			ast.Inspect(file, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				tv, ok := p.TypesInfo.Types[sel.X]
				if !ok {
					return true
				}
				// Unalias BEFORE the *types.Named assertion. Go 1.23+
				// materialises `type SpeakConfig = ModalityOverride` as
				// *types.Alias, which an unwary assertion skips in
				// silence — the field then reads as dead forever.
				t := types.Unalias(tv.Type)
				for {
					ptr, ok := t.(*types.Pointer)
					if !ok {
						break
					}
					t = types.Unalias(ptr.Elem())
				}
				named, ok := t.(*types.Named)
				if !ok || named.Obj().Pkg() == nil || named.Obj().Pkg().Path() != cfgPkg {
					return true
				}
				if key := named.Obj().Name() + "." + sel.Sel.Name; declared[key] {
					read[key] = true
				}
				return true
			})
		}
	}
	return read
}
