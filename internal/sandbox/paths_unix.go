//go:build unix

package sandbox

import (
	"os"
	"syscall"
)

// statNlink extracts st_nlink from a FileInfo on Unix. Returns false
// on platforms (or file types) where Nlink isn't available.
func statNlink(info os.FileInfo) (uint64, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	// NOT a redundant conversion, despite what unconvert says when it
	// analyses this file on linux/amd64 alone. This file is //go:build
	// unix, and syscall.Stat_t.Nlink is uint64 only on some platforms —
	// it is uint32 on linux/386 and linux/arm, and uint16 on darwin.
	// Dropping the conversion breaks the build everywhere except the
	// arch the linter happened to run on.
	return uint64(stat.Nlink), true //nolint:unconvert // Nlink width is platform-dependent

}
