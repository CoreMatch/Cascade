// Package paths resolves runtime file locations so the server works both
// from a source checkout (go run) and as a standalone binary started from
// an arbitrary working directory.
package paths

import (
	"os"
	"path/filepath"
)

// ExecutableDir returns the directory containing the running binary. It is
// a variable so tests can substitute a fake install location.
var ExecutableDir = func() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Dir(exe), nil
}

// Candidates returns the ordered locations considered for p: the current
// working directory first, then the directory of the executable. Absolute
// paths are returned unchanged.
func Candidates(p string) []string {
	if filepath.IsAbs(p) {
		return []string{p}
	}
	candidates := []string{p}
	if dir, err := ExecutableDir(); err == nil {
		candidates = append(candidates, filepath.Join(dir, p))
	}
	return candidates
}

// FirstExisting returns the first candidate for p that exists on disk.
// The boolean result reports whether any candidate was found; when false,
// the returned path is empty.
func FirstExisting(p string) (string, bool) {
	for _, candidate := range Candidates(p) {
		if _, err := os.Stat(candidate); err == nil {
			return candidate, true
		}
	}
	return "", false
}
