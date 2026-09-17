package matrixas

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

func TestSolitaireCommandClaimIsExclusiveAndPersistent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "portals.json")
	var claimed atomic.Int32
	var workers sync.WaitGroup
	for range 16 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			ok, err := claimSolitaireCommand(path, "!room:example.com", "$command")
			if err != nil {
				t.Errorf("claim: %v", err)
			}
			if ok {
				claimed.Add(1)
			}
		}()
	}
	workers.Wait()
	if claimed.Load() != 1 {
		t.Fatalf("claimed %d times", claimed.Load())
	}
	if ok, err := claimSolitaireCommand(path, "!room:example.com", "$command"); ok || err != nil {
		t.Fatalf("existing durable claim lost: ok=%v err=%v", ok, err)
	}
	if ok, err := claimSolitaireCommand(path, "!room:example.com", "$new-command"); !ok || err != nil {
		t.Fatalf("explicit new command rejected: ok=%v err=%v", ok, err)
	}
	entries, err := os.ReadDir(path + ".solitaire-claims")
	if err != nil || len(entries) != 2 {
		t.Fatalf("claim files: %v %v", entries, err)
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("claim permissions: %v err=%v", info, err)
		}
	}
}

func TestSolitaireCommandClaimFailsClosedWithoutStorage(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(parent, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"", filepath.Join(parent, "portals.json")} {
		if ok, err := claimSolitaireCommand(path, "!room:example.com", "$command"); ok || err == nil {
			t.Fatalf("claim succeeded without persistence: ok=%v err=%v", ok, err)
		}
	}
}
