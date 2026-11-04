package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFirstExistingPrefersWorkingDirectory(t *testing.T) {
	exeDir := t.TempDir()
	t.Chdir(t.TempDir())
	orig := ExecutableDir
	ExecutableDir = func() (string, error) { return exeDir, nil }
	t.Cleanup(func() { ExecutableDir = orig })

	if _, ok := FirstExisting("missing.txt"); ok {
		t.Fatal("found a file that does not exist")
	}

	// A file only present next to the executable must still be found.
	exeFile := filepath.Join(exeDir, "data.txt")
	if err := os.WriteFile(exeFile, []byte("exe"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, ok := FirstExisting("data.txt")
	if !ok || got != exeFile {
		t.Fatalf("expected executable-dir fallback %q, got %q (%v)", exeFile, got, ok)
	}

	// The working directory wins when both locations exist.
	if err := os.WriteFile("data.txt", []byte("cwd"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, ok = FirstExisting("data.txt")
	if !ok || got != "data.txt" {
		t.Fatalf("expected working directory to win, got %q (%v)", got, ok)
	}
}

func TestCandidatesKeepsAbsolutePaths(t *testing.T) {
	got := Candidates("/etc/cascade/config.yaml")
	if len(got) != 1 || got[0] != "/etc/cascade/config.yaml" {
		t.Fatalf("absolute path must not be re-resolved: %v", got)
	}
}
