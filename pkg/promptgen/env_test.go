package promptgen

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Regression: on WSL2 the Windows PATH is inherited and drvfs reports
// mode 0777 for every file, so the executable-bit check passes on
// thousands of .dll/.dat entries. That put 124 KB (~31k tokens) of
// Windows filenames into the system prompt on every single turn.
func TestEnumerateSkipsOversizedSystemDirectories(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for i := range maxDirEntries + 50 {
		p := filepath.Join(dir, fmt.Sprintf("lib%04d.dll", i))
		if err := os.WriteFile(p, []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got := enumerateSpecialtyPath(dir)
	if len(got) != 0 {
		t.Errorf("got %d names from a %d-entry directory; want 0 (not a curated tool dir)",
			len(got), maxDirEntries+50)
	}
}

func TestEnumerateRejectsNonExecutableExtensions(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, name := range []string{"HyperV.dll", "mssrch.sys", "data.dat", "notes.txt", "realtool"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got := enumerateSpecialtyPath(dir)
	if len(got) != 1 || got[0] != "realtool" {
		t.Errorf("got %v; want just [realtool] — Windows artefacts should be filtered", got)
	}
}

func TestSpecialtyListIsCappedByCount(t *testing.T) {
	t.Parallel()
	names := make([]string, 0, maxSpecialtyCommands+100)
	for i := range maxSpecialtyCommands + 100 {
		names = append(names, fmt.Sprintf("t%d", i))
	}
	got := capSpecialty(names)
	if len(got) > maxSpecialtyCommands {
		t.Errorf("got %d names; cap is %d", len(got), maxSpecialtyCommands)
	}
}

func TestSpecialtyListIsCappedByBytes(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("n", 300)
	names := make([]string, 0, 100)
	for i := range 100 {
		names = append(names, fmt.Sprintf("%s%d", long, i))
	}
	got := capSpecialty(names)
	total := 0
	for _, n := range got {
		total += len(n) + 2
	}
	if total > maxSpecialtyBytes {
		t.Errorf("rendered %d bytes; cap is %d", total, maxSpecialtyBytes)
	}
}

// The whole point of the section: a normal tool directory still gets
// advertised.
func TestEnumerateStillFindsRealSpecialtyBinaries(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, name := range []string{"rtk", "himalaya", "ls"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got := enumerateSpecialtyPath(dir)
	want := map[string]bool{"rtk": true, "himalaya": true}
	for _, g := range got {
		if g == "ls" {
			t.Error("common unix command should not be advertised")
		}
		delete(want, g)
	}
	if len(want) != 0 {
		t.Errorf("missing specialty binaries: %v (got %v)", want, got)
	}
}

func TestIsForeignMountDetectsWSLDrives(t *testing.T) {
	t.Parallel()
	foreign := []string{"/mnt/c/Windows/System32", "/mnt/c/Program Files/Git/cmd", "/mnt/d/tools"}
	for _, d := range foreign {
		if !isForeignMount(d) {
			t.Errorf("%s should be treated as a foreign mount", d)
		}
	}
	native := []string{"/usr/local/bin", "/home/james/.local/bin", "/opt/tools", "/mnt/data/bin", "/mntfoo/bin"}
	for _, d := range native {
		if isForeignMount(d) {
			t.Errorf("%s is a native path and must still be scanned", d)
		}
	}
}
