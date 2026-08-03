package matrixas

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leat/mautrix-go-wechat/internal/config"
	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
	_ "github.com/leat/mautrix-go-wechat/internal/wechat"
)

func TestBuiltinEmojiStoreMissingStartsEmptyAndImportable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "builtin.json")
	store := newBuiltinEmojiStore(path)

	if err := store.LoadError(); err != nil {
		t.Fatalf("LoadError() = %v, want nil for absent file", err)
	}
	got := store.Snapshot()
	want := builtinEmojiTestData()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Snapshot() = %#v, want %#v", got, want)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("absent store was unexpectedly created: %v", err)
	}

	if err := store.PersistMerged(map[string]string{"wechat_smile": "mxc://example.com/smile"}); err != nil {
		t.Fatalf("PersistMerged() = %v", err)
	}
	if got, ok := store.Get("wechat_smile"); !ok || got != "mxc://example.com/smile" {
		t.Fatalf("Get(wechat_smile) = %q, %v", got, ok)
	}
}

func TestBuiltinEmojiStoreLoadsPartialManifestAndDefensivelyCopies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "builtin.json")
	writeBuiltinEmojiTestData(t, path, builtinEmojiTestData(
		builtinEmojiRecord{Shortcode: "wechat_grimace", MXC: "mxc://example.com/grimace"},
		builtinEmojiRecord{Shortcode: "wechat_smile", MXC: "mxc://example.com/smile"},
	))

	store := newBuiltinEmojiStore(path)
	if err := store.LoadError(); err != nil {
		t.Fatalf("LoadError() = %v", err)
	}
	got := store.Snapshot()
	if len(got.Images) != 2 || got.Images[0].Shortcode != "wechat_smile" || got.Images[1].Shortcode != "wechat_grimace" {
		t.Fatalf("loaded images are not in registry order: %#v", got.Images)
	}

	got.Images[0].MXC = "mxc://evil.example/changed"
	got.Images = append(got.Images, builtinEmojiRecord{Shortcode: "wechat_unknown", MXC: "mxc://evil.example/unknown"})
	images := store.Images()
	images["wechat_smile"] = "mxc://evil.example/map-change"
	delete(images, "wechat_grimace")

	again := store.Snapshot()
	if len(again.Images) != 2 || again.Images[0].MXC != "mxc://example.com/smile" || again.Images[1].MXC != "mxc://example.com/grimace" {
		t.Fatalf("caller mutated store through returned data: %#v", again.Images)
	}
}

func TestBuiltinEmojiStoreRejectsInvalidFilesWithoutTouchingThem(t *testing.T) {
	valid := builtinEmojiTestData(builtinEmojiRecord{Shortcode: "wechat_smile", MXC: "mxc://example.com/smile"})
	tests := map[string][]byte{
		"corrupt JSON":          []byte(`{"schema_version":`),
		"unsupported schema":    marshalBuiltinEmojiTestData(t, withBuiltinEmojiSchema(valid, builtinEmojiStoreSchemaVersion+1)),
		"wrong repository":      marshalBuiltinEmojiTestData(t, withBuiltinEmojiSource(valid, builtinEmojiSource{Repository: "https://example.com/wrong", Commit: builtinEmojiSourceCommit, Path: builtinEmojiSourcePath})),
		"wrong commit":          marshalBuiltinEmojiTestData(t, withBuiltinEmojiSource(valid, builtinEmojiSource{Repository: builtinEmojiSourceRepository, Commit: strings.Repeat("0", 40), Path: builtinEmojiSourcePath})),
		"wrong path":            marshalBuiltinEmojiTestData(t, withBuiltinEmojiSource(valid, builtinEmojiSource{Repository: builtinEmojiSourceRepository, Commit: builtinEmojiSourceCommit, Path: "wrong/path"})),
		"duplicate shortcode":   marshalBuiltinEmojiTestData(t, builtinEmojiTestData(valid.Images[0], valid.Images[0])),
		"unknown shortcode":     marshalBuiltinEmojiTestData(t, builtinEmojiTestData(builtinEmojiRecord{Shortcode: "wechat_not_in_registry", MXC: "mxc://example.com/unknown"})),
		"empty shortcode":       marshalBuiltinEmojiTestData(t, builtinEmojiTestData(builtinEmojiRecord{Shortcode: "", MXC: "mxc://example.com/empty"})),
		"invalid MXC":           marshalBuiltinEmojiTestData(t, builtinEmojiTestData(builtinEmojiRecord{Shortcode: "wechat_smile", MXC: "https://example.com/not-mxc"})),
		"MXC with whitespace":   marshalBuiltinEmojiTestData(t, builtinEmojiTestData(builtinEmojiRecord{Shortcode: "wechat_smile", MXC: " mxc://example.com/smile"})),
		"missing images":        []byte(`{"schema_version":1,"source":{"repository":"https://github.com/airinghost/wechat-emoji","commit":"2896eb55348041f5c5a66e4eccfb4882ab9cda0c","path":"web.wechat.com/compressed-cwebp/lossless-z9"}}`),
		"unknown schema member": []byte(`{"schema_version":1,"source":{"repository":"https://github.com/airinghost/wechat-emoji","commit":"2896eb55348041f5c5a66e4eccfb4882ab9cda0c","path":"web.wechat.com/compressed-cwebp/lossless-z9"},"images":[],"unexpected":true}`),
	}

	for name, original := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "builtin.json")
			if err := os.WriteFile(path, original, 0o600); err != nil {
				t.Fatal(err)
			}
			store := newBuiltinEmojiStore(path)
			if store.LoadError() == nil {
				t.Fatal("LoadError() = nil, want fail-closed error")
			}
			if got := store.Snapshot(); len(got.Images) != 0 {
				t.Fatalf("invalid file published records: %#v", got.Images)
			}
			if err := store.PersistMerged(map[string]string{"wechat_smile": "mxc://example.com/new"}); err == nil {
				t.Fatal("PersistMerged() = nil on invalid loaded store")
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(original) {
				t.Fatalf("invalid file was modified:\n got %q\nwant %q", after, original)
			}
		})
	}
}

func TestBuiltinEmojiStoreRejectsDirectoryWithoutReadingOrQuarantining(t *testing.T) {
	path := filepath.Join(t.TempDir(), "builtin.json")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(path, "marker")
	if err := os.WriteFile(marker, []byte("preserve me"), 0o600); err != nil {
		t.Fatal(err)
	}

	store := newBuiltinEmojiStore(path)
	if err := store.LoadError(); err == nil || !strings.Contains(err.Error(), "non-regular") {
		t.Fatalf("LoadError() = %v, want non-regular path error", err)
	}
	if err := store.ResetForAcknowledgedImport(time.Now()); err == nil || !strings.Contains(err.Error(), "non-regular") {
		t.Fatalf("ResetForAcknowledgedImport() = %v, want non-regular path error", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatalf("path mode after reset = %v, want directory", info.Mode())
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != "preserve me" {
		t.Fatalf("directory contents after reset = %q, %v", got, err)
	}
}

func TestBuiltinEmojiStoreRejectsSymlinkWithoutFollowingOrQuarantining(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	original := marshalBuiltinEmojiTestData(t, builtinEmojiTestData(
		builtinEmojiRecord{Shortcode: "wechat_smile", MXC: "mxc://example.com/smile"},
	))
	if err := os.WriteFile(target, original, 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "builtin.json")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}

	store := newBuiltinEmojiStore(path)
	if err := store.LoadError(); err == nil || !strings.Contains(err.Error(), "non-regular") {
		t.Fatalf("LoadError() = %v, want non-regular path error", err)
	}
	if got := store.Snapshot(); len(got.Images) != 0 {
		t.Fatalf("symlink target was published: %#v", got.Images)
	}
	if err := store.ResetForAcknowledgedImport(time.Now()); err == nil || !strings.Contains(err.Error(), "non-regular") {
		t.Fatalf("ResetForAcknowledgedImport() = %v, want non-regular path error", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("path mode after reset = %v, want symlink", info.Mode())
	}
	if got, err := os.ReadFile(target); err != nil || !reflect.DeepEqual(got, original) {
		t.Fatalf("symlink target after reset = %q, %v; want %q", got, err, original)
	}
}

func TestBuiltinEmojiStorePersistMergedValidatesMergesAndSorts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "builtin.json")
	writeBuiltinEmojiTestData(t, path, builtinEmojiTestData(
		builtinEmojiRecord{Shortcode: "wechat_grimace", MXC: "mxc://example.com/old-grimace"},
	))
	store := newBuiltinEmojiStore(path)

	if err := store.PersistMerged(map[string]string{
		"wechat_smile":       "mxc://example.com/smile",
		"wechat_drool":       "mxc://example.com/drool",
		"wechat_grimace":     "mxc://example.com/new-grimace",
		"wechat_not_allowed": "mxc://example.com/unknown",
	}); err == nil {
		t.Fatal("PersistMerged() accepted unknown shortcode")
	}
	if got, _ := store.Get("wechat_grimace"); got != "mxc://example.com/old-grimace" {
		t.Fatalf("failed validation changed memory: %q", got)
	}

	if err := store.PersistMerged(map[string]string{"wechat_smile": "not-an-mxc"}); err == nil {
		t.Fatal("PersistMerged() accepted invalid MXC")
	}
	if err := store.PersistMerged(map[string]string{
		"wechat_smile":   "mxc://example.com/smile",
		"wechat_drool":   "mxc://example.com/drool",
		"wechat_grimace": "mxc://example.com/new-grimace",
	}); err != nil {
		t.Fatalf("PersistMerged() = %v", err)
	}

	got := readBuiltinEmojiTestData(t, path)
	wantShortcodes := []string{"wechat_smile", "wechat_grimace", "wechat_drool"}
	gotShortcodes := make([]string, len(got.Images))
	for i, record := range got.Images {
		gotShortcodes[i] = record.Shortcode
	}
	if !reflect.DeepEqual(gotShortcodes, wantShortcodes) {
		t.Fatalf("persisted shortcode order = %v, want %v", gotShortcodes, wantShortcodes)
	}
	if got.Images[1].MXC != "mxc://example.com/new-grimace" {
		t.Fatalf("merge did not replace existing MXC: %#v", got.Images[1])
	}
	if got.SchemaVersion != builtinEmojiStoreSchemaVersion || got.Source != builtinEmojiTestData().Source {
		t.Fatalf("persisted schema/provenance = %#v", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("persisted mode = %o, want 600", info.Mode().Perm())
	}
}

func TestBuiltinEmojiStorePersistFailureDoesNotPublishMemoryOrDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "builtin.json")
	writeBuiltinEmojiTestData(t, path, builtinEmojiTestData(
		builtinEmojiRecord{Shortcode: "wechat_smile", MXC: "mxc://example.com/original"},
	))
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	store := newBuiltinEmojiStore(path)
	injected := errors.New("injected atomic rename failure")
	store.persist = func(path string, data []byte, mode fs.FileMode) error {
		return writeFileAtomicWithHook(path, data, mode, func(stage string) error {
			if stage == "rename" {
				return injected
			}
			return nil
		})
	}

	err = store.PersistMerged(map[string]string{
		"wechat_smile":   "mxc://example.com/replaced",
		"wechat_grimace": "mxc://example.com/grimace",
	})
	if !errors.Is(err, injected) {
		t.Fatalf("PersistMerged() error = %v, want injected error", err)
	}
	got := store.Snapshot()
	if len(got.Images) != 1 || got.Images[0].MXC != "mxc://example.com/original" {
		t.Fatalf("persistence failure changed memory: %#v", got.Images)
	}
	after, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(after) != string(before) {
		t.Fatalf("persistence failure changed disk:\n got %s\nwant %s", after, before)
	}
}

func TestBuiltinEmojiStoreResetQuarantinesCorruptContentAndCreatesEmptyStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "builtin.json")
	corrupt := []byte(`{"not":"valid store"}`)
	if err := os.WriteFile(path, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	store := newBuiltinEmojiStore(path)
	if store.LoadError() == nil {
		t.Fatal("precondition: corrupt store did not expose LoadError")
	}
	now := time.Date(2026, 8, 3, 12, 34, 56, 0, time.FixedZone("offset", 2*60*60))
	if err := store.ResetForAcknowledgedImport(now); err != nil {
		t.Fatalf("ResetForAcknowledgedImport() = %v", err)
	}

	quarantine := path + ".corrupt-20260803T103456.000000000Z"
	quarantined, err := os.ReadFile(quarantine)
	if err != nil {
		t.Fatalf("read quarantine: %v", err)
	}
	if string(quarantined) != string(corrupt) {
		t.Fatalf("quarantine content = %q, want %q", quarantined, corrupt)
	}
	if err := store.LoadError(); err != nil {
		t.Fatalf("LoadError() after reset = %v", err)
	}
	got := readBuiltinEmojiTestData(t, path)
	if !reflect.DeepEqual(got, builtinEmojiTestData()) {
		t.Fatalf("reset file = %#v, want empty canonical store", got)
	}

	if err := store.PersistMerged(map[string]string{"wechat_smile": "mxc://example.com/smile"}); err != nil {
		t.Fatal(err)
	}
	if err := store.ResetForAcknowledgedImport(now.Add(time.Hour)); err != nil {
		t.Fatalf("idempotent reset = %v", err)
	}
	if got, ok := store.Get("wechat_smile"); !ok || got != "mxc://example.com/smile" {
		t.Fatalf("reset erased valid data: %q, %v", got, ok)
	}
	if _, err := os.Stat(path + ".corrupt-20260803T113456.000000000Z"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("valid store was unexpectedly quarantined: %v", err)
	}
}

func TestBuiltinEmojiStoreResetPreservesExistingQuarantinesWithDeterministicSuffixes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "builtin.json")
	now := time.Date(2026, 8, 3, 1, 2, 3, 123456789, time.UTC)
	base := path + ".corrupt-20260803T010203.123456789Z"
	originalDiagnostic := []byte("existing diagnostic")
	if err := os.WriteFile(base, originalDiagnostic, 0o600); err != nil {
		t.Fatal(err)
	}

	for attempt, corrupt := range [][]byte{[]byte("first corrupt store"), []byte("second corrupt store")} {
		if err := os.WriteFile(path, corrupt, 0o600); err != nil {
			t.Fatal(err)
		}
		store := newBuiltinEmojiStore(path)
		if err := store.ResetForAcknowledgedImport(now); err != nil {
			t.Fatalf("attempt %d ResetForAcknowledgedImport() = %v", attempt+1, err)
		}
		quarantinePath := fmt.Sprintf("%s-%d", base, attempt+1)
		got, err := os.ReadFile(quarantinePath)
		if err != nil || !reflect.DeepEqual(got, corrupt) {
			t.Fatalf("attempt %d quarantine = %q, %v; want %q", attempt+1, got, err, corrupt)
		}
	}

	got, err := os.ReadFile(base)
	if err != nil || !reflect.DeepEqual(got, originalDiagnostic) {
		t.Fatalf("preexisting quarantine = %q, %v; want unchanged %q", got, err, originalDiagnostic)
	}
}

func TestBuiltinEmojiStoreResetAbortsWhenQuarantineRenameFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "builtin.json")
	corrupt := []byte(`corrupt`)
	if err := os.WriteFile(path, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	store := newBuiltinEmojiStore(path)
	injected := errors.New("rename denied")
	persistCalled := false
	store.rename = func(string, string) error { return injected }
	store.persist = func(string, []byte, fs.FileMode) error {
		persistCalled = true
		return nil
	}

	err := store.ResetForAcknowledgedImport(time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC))
	if !errors.Is(err, injected) {
		t.Fatalf("ResetForAcknowledgedImport() error = %v, want injected error", err)
	}
	if persistCalled {
		t.Fatal("empty store persisted after quarantine rename failed")
	}
	if store.LoadError() == nil {
		t.Fatal("rename failure cleared LoadError")
	}
	after, readErr := os.ReadFile(path)
	if readErr != nil || string(after) != string(corrupt) {
		t.Fatalf("corrupt source changed after rename failure: %q, %v", after, readErr)
	}
}

func TestBuiltinEmojiStoreResetPersistenceFailureKeepsQuarantineAndLoadError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "builtin.json")
	corrupt := []byte(`corrupt persistence case`)
	if err := os.WriteFile(path, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	store := newBuiltinEmojiStore(path)
	injected := errors.New("atomic write denied")
	store.persist = func(string, []byte, fs.FileMode) error { return injected }
	now := time.Date(2026, 8, 3, 1, 2, 3, 0, time.UTC)

	err := store.ResetForAcknowledgedImport(now)
	if !errors.Is(err, injected) {
		t.Fatalf("ResetForAcknowledgedImport() error = %v, want injected error", err)
	}
	if store.LoadError() == nil {
		t.Fatal("persistence failure cleared LoadError")
	}
	if got := store.Snapshot(); len(got.Images) != 0 {
		t.Fatalf("persistence failure published records: %#v", got.Images)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("invalid source remains at canonical path after quarantine: %v", err)
	}
	quarantined, readErr := os.ReadFile(path + ".corrupt-20260803T010203.000000000Z")
	if readErr != nil || string(quarantined) != string(corrupt) {
		t.Fatalf("quarantine after persist failure = %q, %v", quarantined, readErr)
	}

	store.persist = writeFileAtomic
	if err := store.ResetForAcknowledgedImport(now.Add(time.Second)); err != nil {
		t.Fatalf("retry ResetForAcknowledgedImport() = %v", err)
	}
	if err := store.LoadError(); err != nil {
		t.Fatalf("retry did not clear LoadError: %v", err)
	}
	if got := readBuiltinEmojiTestData(t, path); !reflect.DeepEqual(got, builtinEmojiTestData()) {
		t.Fatalf("retry reset file = %#v, want empty canonical store", got)
	}
}

func TestBuiltinEmojiStoreResetIsSafeForMissingAndValidStores(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "builtin.json")
		store := newBuiltinEmojiStore(path)
		if err := store.ResetForAcknowledgedImport(time.Now()); err != nil {
			t.Fatalf("ResetForAcknowledgedImport() = %v", err)
		}
		if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("missing store reset had side effect: %v", err)
		}
	})

	t.Run("valid", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "builtin.json")
		writeBuiltinEmojiTestData(t, path, builtinEmojiTestData(
			builtinEmojiRecord{Shortcode: "wechat_smile", MXC: "mxc://example.com/smile"},
		))
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		store := newBuiltinEmojiStore(path)
		if err := store.ResetForAcknowledgedImport(time.Now()); err != nil {
			t.Fatalf("ResetForAcknowledgedImport() = %v", err)
		}
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != string(before) {
			t.Fatalf("valid store changed during reset:\n got %s\nwant %s", after, before)
		}
	})
}

func TestBuiltinEmojiStoreConcurrentAccessPublishesOnlyCompleteStates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "builtin.json")
	if err := os.WriteFile(path, []byte("corrupt concurrent store"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := newBuiltinEmojiStore(path)

	const (
		readerCount = 8
		iterations  = 200
	)
	start := make(chan struct{})
	resetDone := make(chan struct{})
	errCh := make(chan error, readerCount*iterations+iterations)
	var writers sync.WaitGroup
	writers.Add(2)

	go func() {
		defer writers.Done()
		<-start
		if err := store.ResetForAcknowledgedImport(time.Date(2026, 8, 3, 4, 5, 6, 7, time.UTC)); err != nil {
			errCh <- fmt.Errorf("initial reset: %w", err)
			close(resetDone)
			return
		}
		close(resetDone)
		for i := 0; i < iterations; i++ {
			if err := store.ResetForAcknowledgedImport(time.Now()); err != nil {
				errCh <- fmt.Errorf("reset %d: %w", i, err)
				return
			}
		}
	}()
	go func() {
		defer writers.Done()
		<-start
		initialErr := store.PersistMerged(map[string]string{
			"wechat_smile":   "mxc://example.com/smile-a",
			"wechat_grimace": "mxc://example.com/grimace-a",
		})
		if initialErr != nil && !strings.Contains(initialErr.Error(), "store is unavailable") {
			errCh <- fmt.Errorf("initial concurrent persist: %w", initialErr)
			return
		}
		<-resetDone
		for i := 0; i < iterations; i++ {
			version := "a"
			if i%2 == 1 {
				version = "b"
			}
			if err := store.PersistMerged(map[string]string{
				"wechat_smile":   "mxc://example.com/smile-" + version,
				"wechat_grimace": "mxc://example.com/grimace-" + version,
			}); err != nil {
				errCh <- fmt.Errorf("persist %d: %w", i, err)
				return
			}
		}
	}()

	var readers sync.WaitGroup
	readers.Add(readerCount)
	for reader := 0; reader < readerCount; reader++ {
		go func(reader int) {
			defer readers.Done()
			<-start
			for i := 0; i < iterations; i++ {
				snapshot := store.Snapshot()
				if len(snapshot.Images) != 0 {
					if len(snapshot.Images) != 2 || snapshot.Images[0].Shortcode != "wechat_smile" || snapshot.Images[1].Shortcode != "wechat_grimace" {
						errCh <- fmt.Errorf("reader %d saw partial snapshot: %#v", reader, snapshot.Images)
						return
					}
					smile, grimace := snapshot.Images[0].MXC, snapshot.Images[1].MXC
					if !((smile == "mxc://example.com/smile-a" && grimace == "mxc://example.com/grimace-a") ||
						(smile == "mxc://example.com/smile-b" && grimace == "mxc://example.com/grimace-b")) {
						errCh <- fmt.Errorf("reader %d saw mixed snapshot: %#v", reader, snapshot.Images)
						return
					}
				}
				if got, ok := store.Get("wechat_smile"); ok && got != "mxc://example.com/smile-a" && got != "mxc://example.com/smile-b" {
					errCh <- fmt.Errorf("reader %d Get returned partial value %q", reader, got)
					return
				}
			}
		}(reader)
	}
	close(start)
	writers.Wait()
	readers.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}

	final := store.Snapshot()
	if len(final.Images) != 2 || final.Images[0].MXC != "mxc://example.com/smile-b" || final.Images[1].MXC != "mxc://example.com/grimace-b" {
		t.Fatalf("final snapshot = %#v, want complete final state", final.Images)
	}
}

func TestNewRejectsBuiltinEmojiStoreRegistrationPathCollision(t *testing.T) {
	dir := t.TempDir()
	regPath := filepath.Join(dir, "registration.yaml")
	cfg := &config.Config{
		Homeserver: config.Homeserver{Address: "http://localhost:8008", Domain: "example.com"},
		Appservice: config.Appservice{
			Address: "http://localhost:29318", Hostname: "127.0.0.1", Port: 29318,
			ID: "wechat-test", BotUsername: "wechatbot", ASToken: "as-token", HSToken: "hs-token",
		},
		Network: config.Network{Protocol: "wechat", Receive: config.Receive{Mode: "poll"}},
		Bridge: config.Bridge{
			SessionFile:            filepath.Join(dir, "session.json"),
			PortalFile:             filepath.Join(dir, "portals.json"),
			AccountFile:            filepath.Join(dir, "accounts.json"),
			ContactFile:            filepath.Join(dir, "contacts.json"),
			EmojiDir:               filepath.Join(dir, "emojis"),
			WechatBuiltinEmojiFile: filepath.Join(dir, ".", "registration.yaml"),
		},
	}
	if err := cfg.GenerateRegistration().Save(regPath); err != nil {
		t.Fatal(err)
	}

	server, err := New(cfg, regPath, remoteapi.Config{BaseURL: "http://wechat.invalid"})
	if err == nil || !strings.Contains(err.Error(), "registration") || !strings.Contains(err.Error(), "wechat_builtin_emoji_file") {
		t.Fatalf("New() = %#v, %v; want registration path collision", server, err)
	}
}

func TestNewLoadsCorruptBuiltinEmojiStoreWithoutFailingStartup(t *testing.T) {
	dir := t.TempDir()
	builtinPath := filepath.Join(dir, "builtin.json")
	if err := os.WriteFile(builtinPath, []byte(`corrupt`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Homeserver: config.Homeserver{Address: "http://localhost:8008", Domain: "example.com"},
		Appservice: config.Appservice{
			Address: "http://localhost:29318", Hostname: "127.0.0.1", Port: 29318,
			ID: "wechat-test", BotUsername: "wechatbot", ASToken: "as-token", HSToken: "hs-token",
		},
		Network: config.Network{Protocol: "wechat", Receive: config.Receive{Mode: "poll"}},
		Bridge: config.Bridge{
			SessionFile:            filepath.Join(dir, "session.json"),
			PortalFile:             filepath.Join(dir, "portals.json"),
			AccountFile:            filepath.Join(dir, "accounts.json"),
			ContactFile:            filepath.Join(dir, "contacts.json"),
			WechatBuiltinEmojiFile: builtinPath,
		},
	}
	regPath := filepath.Join(dir, "registration.yaml")
	if err := cfg.GenerateRegistration().Save(regPath); err != nil {
		t.Fatal(err)
	}

	server, err := New(cfg, regPath, remoteapi.Config{BaseURL: "http://wechat.invalid"})
	if err != nil {
		t.Fatalf("New() rejected corrupt built-in emoji store: %v", err)
	}
	if server.builtinEmojiStore == nil {
		t.Fatal("New() did not construct built-in emoji store")
	}
	if server.builtinEmojiStore.LoadError() == nil {
		t.Fatal("New() did not retain built-in emoji store LoadError")
	}
}

func builtinEmojiTestData(records ...builtinEmojiRecord) builtinEmojiStoreData {
	if records == nil {
		records = []builtinEmojiRecord{}
	}
	return builtinEmojiStoreData{
		SchemaVersion: builtinEmojiStoreSchemaVersion,
		Source: builtinEmojiSource{
			Repository: builtinEmojiSourceRepository,
			Commit:     builtinEmojiSourceCommit,
			Path:       builtinEmojiSourcePath,
		},
		Images: records,
	}
}

func withBuiltinEmojiSchema(data builtinEmojiStoreData, schema int) builtinEmojiStoreData {
	data.SchemaVersion = schema
	return data
}

func withBuiltinEmojiSource(data builtinEmojiStoreData, source builtinEmojiSource) builtinEmojiStoreData {
	data.Source = source
	return data
}

func marshalBuiltinEmojiTestData(t *testing.T, data builtinEmojiStoreData) []byte {
	t.Helper()
	encoded, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func writeBuiltinEmojiTestData(t *testing.T, path string, data builtinEmojiStoreData) {
	t.Helper()
	if err := os.WriteFile(path, marshalBuiltinEmojiTestData(t, data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readBuiltinEmojiTestData(t *testing.T, path string) builtinEmojiStoreData {
	t.Helper()
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var data builtinEmojiStoreData
	if err := json.Unmarshal(encoded, &data); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return data
}
