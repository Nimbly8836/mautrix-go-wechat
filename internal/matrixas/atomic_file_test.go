package matrixas

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteFileAtomicReloadModeAndCleanup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := writeFileAtomic(path, []byte("new state"), 0o600); err != nil {
		t.Fatalf("writeFileAtomic: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reload destination: %v", err)
	}
	if string(got) != "new state" {
		t.Fatalf("destination = %q, want new state", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat destination: %v", err)
	}
	if info.Mode().Perm() != fs.FileMode(0o600) {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
	assertNoAtomicTemps(t, dir, filepath.Base(path))
}

func TestWriteFileAtomicPreRenameFailuresPreserveDestinationAndCleanup(t *testing.T) {
	for _, stage := range []string{"chmod", "write", "file-sync", "close", "rename"} {
		t.Run(stage, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "state.json")
			if err := os.WriteFile(path, []byte("old state"), 0o640); err != nil {
				t.Fatal(err)
			}
			injected := errors.New("injected " + stage + " failure")
			err := writeFileAtomicWithHook(path, []byte("new state"), 0o600, func(got string) error {
				if got == stage {
					return injected
				}
				return nil
			})
			if !errors.Is(err, injected) {
				t.Fatalf("error = %v, want injected error", err)
			}
			got, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatalf("read destination: %v", readErr)
			}
			if string(got) != "old state" {
				t.Fatalf("destination changed on %s failure: %q", stage, got)
			}
			assertNoAtomicTemps(t, dir, filepath.Base(path))
		})
	}
}

func TestWriteFileAtomicCreateFailurePreservesDestination(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("old state"), 0o600); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("create failed")
	err := writeFileAtomicWithHook(path, []byte("new state"), 0o600, func(stage string) error {
		if stage == "create" {
			return injected
		}
		return nil
	})
	if !errors.Is(err, injected) {
		t.Fatalf("error = %v, want injected error", err)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil || string(got) != "old state" {
		t.Fatalf("destination after create failure = %q, %v", got, readErr)
	}
	assertNoAtomicTemps(t, dir, filepath.Base(path))
}

func TestWriteFileAtomicDirectorySyncFailureIsAfterCommit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("old state"), 0o600); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("directory sync unsupported")
	err := writeFileAtomicWithHook(path, []byte("new state"), 0o600, func(stage string) error {
		if stage == "directory-sync" {
			return injected
		}
		return nil
	})
	if err != nil {
		t.Fatalf("post-rename directory sync error must be warning-only, got %v", err)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil || string(got) != "new state" {
		t.Fatalf("committed destination = %q, %v", got, readErr)
	}
	assertNoAtomicTemps(t, dir, filepath.Base(path))
}

func assertNoAtomicTemps(t *testing.T, dir, base string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != base && strings.HasPrefix(entry.Name(), "."+base+".") {
			t.Fatalf("temporary file was not cleaned up: %s", entry.Name())
		}
	}
}
