package matrixas

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/leat/mautrix-go-wechat/internal/wechat/textemoji"
)

const (
	builtinEmojiStoreSchemaVersion = 1
	builtinEmojiSourceRepository   = "https://github.com/airinghost/wechat-emoji"
	builtinEmojiSourceCommit       = "2896eb55348041f5c5a66e4eccfb4882ab9cda0c"
	builtinEmojiSourcePath         = "web.wechat.com/compressed-cwebp/lossless-z9"
	builtinEmojiQuarantineTime     = "20060102T150405.000000000Z"
)

type builtinEmojiSource struct {
	Repository string `json:"repository"`
	Commit     string `json:"commit"`
	Path       string `json:"path"`
}

type builtinEmojiStoreData struct {
	SchemaVersion int                  `json:"schema_version"`
	Source        builtinEmojiSource   `json:"source"`
	Images        []builtinEmojiRecord `json:"images"`
}

type builtinEmojiRecord struct {
	Shortcode string `json:"shortcode"`
	MXC       string `json:"mxc"`
}

type builtinEmojiStore struct {
	mu          sync.RWMutex
	path        string
	data        builtinEmojiStoreData
	loadErr     error
	quarantined bool
	persist     func(string, []byte, fs.FileMode) error
	rename      func(string, string) error
}

func newBuiltinEmojiStore(path string) *builtinEmojiStore {
	store := &builtinEmojiStore{
		path:    path,
		data:    emptyBuiltinEmojiStoreData(),
		persist: writeFileAtomic,
		rename:  os.Rename,
	}
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return store
	}
	if err != nil {
		store.loadErr = fmt.Errorf("inspect built-in emoji store %s: %w", path, err)
		return store
	}
	if !info.Mode().IsRegular() {
		store.loadErr = fmt.Errorf("load built-in emoji store %s: non-regular path (%s)", path, info.Mode())
		return store
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		store.loadErr = fmt.Errorf("read built-in emoji store %s: %w", path, err)
		return store
	}
	data, err := decodeBuiltinEmojiStoreData(raw)
	if err != nil {
		store.loadErr = fmt.Errorf("load built-in emoji store %s: %w", path, err)
		return store
	}
	store.data = data
	return store
}

func emptyBuiltinEmojiStoreData() builtinEmojiStoreData {
	return builtinEmojiStoreData{
		SchemaVersion: builtinEmojiStoreSchemaVersion,
		Source: builtinEmojiSource{
			Repository: builtinEmojiSourceRepository,
			Commit:     builtinEmojiSourceCommit,
			Path:       builtinEmojiSourcePath,
		},
		Images: []builtinEmojiRecord{},
	}
}

func decodeBuiltinEmojiStoreData(raw []byte) (builtinEmojiStoreData, error) {
	var data builtinEmojiStoreData
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&data); err != nil {
		return builtinEmojiStoreData{}, fmt.Errorf("decode JSON: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return builtinEmojiStoreData{}, err
	}
	if err := validateBuiltinEmojiStoreData(data); err != nil {
		return builtinEmojiStoreData{}, err
	}
	sortBuiltinEmojiRecords(data.Images)
	return cloneBuiltinEmojiStoreData(data), nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("decode JSON: unexpected trailing value")
		}
		return fmt.Errorf("decode JSON trailing data: %w", err)
	}
	return nil
}

func validateBuiltinEmojiStoreData(data builtinEmojiStoreData) error {
	if data.SchemaVersion != builtinEmojiStoreSchemaVersion {
		return fmt.Errorf("unsupported schema version %d", data.SchemaVersion)
	}
	wantSource := emptyBuiltinEmojiStoreData().Source
	if data.Source.Repository != wantSource.Repository {
		return fmt.Errorf("unexpected source repository %q", data.Source.Repository)
	}
	if data.Source.Commit != wantSource.Commit {
		return fmt.Errorf("unexpected source commit %q", data.Source.Commit)
	}
	if data.Source.Path != wantSource.Path {
		return fmt.Errorf("unexpected source path %q", data.Source.Path)
	}
	if data.Images == nil {
		return fmt.Errorf("images must be an array")
	}

	registry := builtinEmojiRegistryIndexes()
	seen := make(map[string]struct{}, len(data.Images))
	for _, record := range data.Images {
		if _, ok := registry[record.Shortcode]; !ok {
			return fmt.Errorf("shortcode %q is not in the built-in emoji registry", record.Shortcode)
		}
		if _, duplicate := seen[record.Shortcode]; duplicate {
			return fmt.Errorf("duplicate shortcode %q", record.Shortcode)
		}
		seen[record.Shortcode] = struct{}{}
		if _, ok := validMXC(record.MXC); !ok {
			return fmt.Errorf("shortcode %q has invalid MXC %q", record.Shortcode, record.MXC)
		}
	}
	return nil
}

func builtinEmojiRegistryIndexes() map[string]int {
	entries := textemoji.Entries()
	indexes := make(map[string]int, len(entries))
	for _, entry := range entries {
		indexes[entry.Shortcode] = entry.Index
	}
	return indexes
}

func sortBuiltinEmojiRecords(records []builtinEmojiRecord) {
	indexes := builtinEmojiRegistryIndexes()
	sort.Slice(records, func(i, j int) bool {
		return indexes[records[i].Shortcode] < indexes[records[j].Shortcode]
	})
}

func cloneBuiltinEmojiStoreData(data builtinEmojiStoreData) builtinEmojiStoreData {
	clone := data
	clone.Images = make([]builtinEmojiRecord, len(data.Images))
	copy(clone.Images, data.Images)
	return clone
}

func marshalBuiltinEmojiStoreData(data builtinEmojiStoreData) ([]byte, error) {
	encoded, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode built-in emoji store: %w", err)
	}
	return append(encoded, '\n'), nil
}

func (store *builtinEmojiStore) LoadError() error {
	store.mu.RLock()
	defer store.mu.RUnlock()
	return store.loadErr
}

func (store *builtinEmojiStore) Snapshot() builtinEmojiStoreData {
	store.mu.RLock()
	defer store.mu.RUnlock()
	return cloneBuiltinEmojiStoreData(store.data)
}

func (store *builtinEmojiStore) Images() map[string]string {
	store.mu.RLock()
	defer store.mu.RUnlock()
	images := make(map[string]string, len(store.data.Images))
	for _, record := range store.data.Images {
		images[record.Shortcode] = record.MXC
	}
	return images
}

func (store *builtinEmojiStore) Get(shortcode string) (string, bool) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	for _, record := range store.data.Images {
		if record.Shortcode == shortcode {
			return record.MXC, true
		}
	}
	return "", false
}

func (store *builtinEmojiStore) PersistMerged(replacements map[string]string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.loadErr != nil {
		return fmt.Errorf("built-in emoji store is unavailable: %w", store.loadErr)
	}

	merged := make(map[string]string, len(store.data.Images)+len(replacements))
	for _, record := range store.data.Images {
		merged[record.Shortcode] = record.MXC
	}
	for shortcode, mxc := range replacements {
		merged[shortcode] = mxc
	}

	next := emptyBuiltinEmojiStoreData()
	next.Images = make([]builtinEmojiRecord, 0, len(merged))
	for shortcode, mxc := range merged {
		next.Images = append(next.Images, builtinEmojiRecord{Shortcode: shortcode, MXC: mxc})
	}
	if err := validateBuiltinEmojiStoreData(next); err != nil {
		return fmt.Errorf("validate built-in emoji merge: %w", err)
	}
	sortBuiltinEmojiRecords(next.Images)
	encoded, err := marshalBuiltinEmojiStoreData(next)
	if err != nil {
		return err
	}
	if err := store.persist(store.path, encoded, 0o600); err != nil {
		return fmt.Errorf("persist built-in emoji store: %w", err)
	}
	store.data = cloneBuiltinEmojiStoreData(next)
	return nil
}

func (store *builtinEmojiStore) nextQuarantinePath(now time.Time) (string, error) {
	base := store.path + ".corrupt-" + now.UTC().Format(builtinEmojiQuarantineTime)
	for suffix := 0; ; suffix++ {
		candidate := base
		if suffix > 0 {
			candidate = fmt.Sprintf("%s-%d", base, suffix)
		}
		_, err := os.Lstat(candidate)
		if errors.Is(err, fs.ErrNotExist) {
			return candidate, nil
		}
		if err != nil {
			return "", fmt.Errorf("inspect built-in emoji quarantine destination %s: %w", candidate, err)
		}
	}
}

func (store *builtinEmojiStore) ResetForAcknowledgedImport(now time.Time) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.loadErr == nil {
		return nil
	}

	if !store.quarantined {
		info, err := os.Lstat(store.path)
		if err != nil {
			return fmt.Errorf("inspect invalid built-in emoji store before quarantine: %w", err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refuse to quarantine non-regular built-in emoji store %s (%s)", store.path, info.Mode())
		}
		quarantinePath, err := store.nextQuarantinePath(now)
		if err != nil {
			return err
		}
		if err := store.rename(store.path, quarantinePath); err != nil {
			return fmt.Errorf("quarantine invalid built-in emoji store: %w", err)
		}
		store.quarantined = true
	}
	empty := emptyBuiltinEmojiStoreData()
	encoded, err := marshalBuiltinEmojiStoreData(empty)
	if err != nil {
		return err
	}
	if err := store.persist(store.path, encoded, 0o600); err != nil {
		return fmt.Errorf("persist reset built-in emoji store: %w", err)
	}
	store.data = cloneBuiltinEmojiStoreData(empty)
	store.loadErr = nil
	store.quarantined = false
	return nil
}
