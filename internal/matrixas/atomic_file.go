package matrixas

import (
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
)

type atomicFileHook func(stage string) error

// writeFileAtomic provides Unix-style atomic replacement by creating a temporary
// file in the destination directory, applying mode, writing and syncing its
// contents, closing it, renaming it over the destination, then syncing the
// directory. Rename is the commit point: every earlier error is returned with
// the old destination intact, while later directory open/sync/close errors are
// warning-only because the committed rename cannot be safely rolled back. This
// contract does not promise equivalent rename-over-existing semantics on Windows.
func writeFileAtomic(path string, data []byte, mode fs.FileMode) error {
	return writeFileAtomicWithHook(path, data, mode, nil)
}

func writeFileAtomicWithHook(path string, data []byte, mode fs.FileMode, hook atomicFileHook) error {
	runHook := func(stage string) error {
		if hook == nil {
			return nil
		}
		return hook(stage)
	}

	dir := filepath.Dir(path)
	base := filepath.Base(path)
	if err := runHook("create"); err != nil {
		return fmt.Errorf("create atomic temporary file: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "."+base+".*")
	if err != nil {
		return fmt.Errorf("create atomic temporary file: %w", err)
	}
	tmpPath := tmp.Name()
	closed := false
	defer func() {
		if !closed {
			_ = tmp.Close()
		}
		_ = os.Remove(tmpPath)
	}()

	if err = runHook("chmod"); err == nil {
		err = tmp.Chmod(mode)
	}
	if err != nil {
		return fmt.Errorf("chmod atomic temporary file: %w", err)
	}
	if err = runHook("write"); err != nil {
		return fmt.Errorf("write atomic temporary file: %w", err)
	}
	n, err := tmp.Write(data)
	if err != nil {
		return fmt.Errorf("write atomic temporary file: %w", err)
	}
	if n != len(data) {
		return fmt.Errorf("write atomic temporary file: %w", io.ErrShortWrite)
	}
	if err = runHook("file-sync"); err == nil {
		err = tmp.Sync()
	}
	if err != nil {
		return fmt.Errorf("sync atomic temporary file: %w", err)
	}
	if err = runHook("close"); err != nil {
		return fmt.Errorf("close atomic temporary file: %w", err)
	}
	if err = tmp.Close(); err != nil {
		closed = true
		return fmt.Errorf("close atomic temporary file: %w", err)
	}
	closed = true
	if err = runHook("rename"); err == nil {
		err = os.Rename(tmpPath, path)
	}
	if err != nil {
		return fmt.Errorf("rename atomic temporary file: %w", err)
	}

	// Rename is the commit point. Directory sync failures can only reduce crash
	// durability; returning an error now would falsely imply the old destination
	// is still authoritative even though the new file is already visible.
	if err = runHook("directory-sync"); err != nil {
		log.Printf("warning: atomic file %s committed but directory sync failed: %v", path, err)
		return nil
	}
	dirFile, err := os.Open(dir)
	if err != nil {
		log.Printf("warning: atomic file %s committed but opening parent directory for sync failed: %v", path, err)
		return nil
	}
	if err := dirFile.Sync(); err != nil {
		log.Printf("warning: atomic file %s committed but directory sync failed: %v", path, err)
	}
	if err := dirFile.Close(); err != nil {
		log.Printf("warning: atomic file %s committed but closing parent directory failed: %v", path, err)
	}
	return nil
}
