package matrixas

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
	"maunium.net/go/mautrix/id"
)

const referenceTextLimit = 4096
const maxRelatedReferenceEvents = 32

type matrixReferenceKey struct {
	Account remoteapi.UserID
	Chat    remoteapi.ChatID
	Room    id.RoomID
	Event   id.EventID
}

type referenceRecord struct {
	Version   int              `json:"version"`
	Account   remoteapi.UserID `json:"account"`
	Chat      remoteapi.ChatID `json:"chat"`
	Room      id.RoomID        `json:"room"`
	Keys      []string         `json:"keys"`
	Reference messageReference `json:"reference"`
}

// One bridge process owns a journal. A newline is the commit boundary: an
// interrupted final append is discarded on load, never joined to the next write.
// Complete malformed records fail closed. Indices are never globally evicted.
type messageReferenceStore struct {
	mu      sync.Mutex
	path    string
	err     error
	forward map[messageReferenceKey]messageReference
	reverse map[matrixReferenceKey]messageReference
}

func referenceSnapshot(text string) string {
	text = strings.ToValidUTF8(text, "�")
	if len(text) <= referenceTextLimit {
		return text
	}
	text = text[:referenceTextLimit]
	for !utf8.ValidString(text) {
		text = text[:len(text)-1]
	}
	return text
}

func validStrongMessageID(value string) bool {
	n, err := strconv.ParseUint(value, 10, 64)
	return err == nil && n > 0 && strconv.FormatUint(n, 10) == value
}

func validReferenceKey(key string) bool {
	kind, value, ok := strings.Cut(key, ":")
	if !ok || value == "" || value == "0" || len(value) > 1024 || strings.ContainsAny(value, "\x00\r\n") {
		return false
	}
	return (kind == "new" && validStrongMessageID(value)) || kind == "msg" || kind == "id"
}

func validateReferenceRecord(r referenceRecord) error {
	if r.Version != 1 || r.Account == "" || r.Chat == "" || r.Room == "" || r.Reference.EventID == "" || len(r.Keys) == 0 || len(r.Keys) > 32 {
		return fmt.Errorf("invalid message reference identity/version")
	}
	if len(r.Reference.Text) > referenceTextLimit || !utf8.ValidString(r.Reference.Text) || len(r.Reference.SenderName) > referenceTextLimit || r.Reference.Type < remoteapi.MsgText || r.Reference.Type > remoteapi.MsgApp {
		return fmt.Errorf("invalid message reference snapshot")
	}
	for _, identity := range []string{string(r.Account), string(r.Chat), string(r.Room), string(r.Reference.EventID), string(r.Reference.MatrixSender), string(r.Reference.Sender)} {
		if len(identity) > 1024 || strings.ContainsAny(identity, "\x00\r\n") {
			return fmt.Errorf("invalid message reference identity")
		}
	}
	for _, key := range r.Keys {
		if !validReferenceKey(key) || (strings.HasPrefix(key, "new:") && key != "new:"+string(r.Reference.NewID)) {
			return fmt.Errorf("invalid message reference key")
		}
	}
	if len(r.Reference.QuoteText) > referenceTextLimit || !utf8.ValidString(r.Reference.QuoteText) {
		return fmt.Errorf("invalid message reference quote preview")
	}
	if r.Reference.NewID != "" {
		if !validStrongMessageID(string(r.Reference.NewID)) {
			return fmt.Errorf("invalid strong message reference")
		}
		found := false
		for _, key := range r.Keys {
			found = found || key == "new:"+string(r.Reference.NewID)
		}
		if !found {
			return fmt.Errorf("strong reference lacks qualified key")
		}
	}
	if len(r.Reference.RelatedEventIDs) > maxRelatedReferenceEvents {
		return fmt.Errorf("too many related Matrix events")
	}
	seenRelated := map[id.EventID]bool{r.Reference.EventID: true}
	for _, eventID := range r.Reference.RelatedEventIDs {
		if eventID == "" || len(eventID) > 1024 || strings.ContainsAny(string(eventID), "\x00\r\n") || seenRelated[eventID] {
			return fmt.Errorf("invalid related Matrix event")
		}
		seenRelated[eventID] = true
	}
	if r.Reference.BridgeSender && r.Reference.MatrixSender == "" {
		return fmt.Errorf("missing original Matrix sender")
	}
	return nil
}

func openMessageReferenceStore(path string) (*messageReferenceStore, error) {
	store := &messageReferenceStore{path: path, forward: make(map[messageReferenceKey]messageReference), reverse: make(map[matrixReferenceKey]messageReference)}
	// An unconfigured store is useful for embedded/test Servers. Configured
	// deployments always derive this path from their mandatory portal file.
	if path == "" {
		return store, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	// O_EXCL publishes only an empty, private journal, never a partially encoded
	// initial document. Do not follow a pre-existing symlink.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	created := err == nil
	if os.IsExist(err) {
		info, statErr := os.Lstat(path)
		if statErr != nil {
			return nil, statErr
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("reference journal is not a regular file: %s", path)
		}
		f, err = os.OpenFile(path, os.O_RDWR, 0600)
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if err = f.Chmod(0600); err != nil {
		return nil, err
	}
	if created {
		if err = f.Sync(); err != nil {
			return nil, err
		}
		dir, openErr := os.Open(filepath.Dir(path))
		if openErr != nil {
			return nil, openErr
		}
		syncErr := dir.Sync()
		closeErr := dir.Close()
		if syncErr != nil {
			return nil, syncErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
	}
	reader := bufio.NewReaderSize(f, 128*1024)
	var committed int64
	for {
		line, readErr := reader.ReadSlice('\n')
		if readErr == io.EOF {
			if len(line) > 0 {
				if err = f.Truncate(committed); err != nil {
					return nil, err
				}
				if err = f.Sync(); err != nil {
					return nil, err
				}
				log.Printf("message reference journal: discarded incomplete trailing record in %s", path)
			}
			break
		}
		if readErr != nil {
			return nil, readErr
		}
		if len(line) > 128*1024 {
			return nil, fmt.Errorf("oversized reference journal record at %d", committed)
		}
		var record referenceRecord
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(&record); err != nil {
			return nil, fmt.Errorf("reference journal at %d: %w", committed, err)
		}
		var extra any
		if err = decoder.Decode(&extra); err != io.EOF {
			return nil, fmt.Errorf("extra data in reference journal at %d", committed)
		}
		if err = validateReferenceRecord(record); err != nil {
			return nil, fmt.Errorf("reference journal at %d: %w", committed, err)
		}
		if err = store.checkConflict(record); err != nil {
			return nil, err
		}
		store.index(record)
		committed += int64(len(line))
	}
	return store, nil
}

func (s *messageReferenceStore) checkConflict(r referenceRecord) error {
	for _, key := range r.Keys {
		old := s.forward[messageReferenceKey{r.Account, r.Chat, r.Room, key}]
		if strings.HasPrefix(key, "new:") && old.EventID != "" && old.EventID != r.Reference.EventID {
			// The authoritative send response can arrive after a self-echo was
			// bridged. Only that scoped, qualified confirmation may replace the
			// echo with the human-authored Matrix original. Never replace a human
			// event with an echo or merge different senders by body similarity.
			confirmedOwnEcho := old.BridgeSender && !r.Reference.BridgeSender && old.Sender == r.Account && r.Reference.Sender == r.Account
			if !confirmedOwnEcho {
				return fmt.Errorf("strong message reference already belongs to a different Matrix event")
			}
		}
	}
	old := s.reverse[matrixReferenceKey{r.Account, r.Chat, r.Room, r.Reference.EventID}]
	if old.NewID != "" && r.Reference.NewID != "" && old.NewID != r.Reference.NewID {
		return fmt.Errorf("Matrix event already belongs to a different strong message ID")
	}
	return nil
}

func (s *messageReferenceStore) index(r referenceRecord) {
	matrixKey := matrixReferenceKey{r.Account, r.Chat, r.Room, r.Reference.EventID}
	original := s.reverse[matrixKey]
	// Learning an additional short alias must not downgrade an already proven
	// server ID or replace its original sender/snapshot with a partial echo.
	if original.NewID != "" && r.Reference.NewID == "" {
		r.Reference = original
	}
	// Incremental attachment confirmations must not be erased by an older
	// root-only write. They are only recall targets, not reverse quote sources.
	r.Reference.RelatedEventIDs = mergeRelatedEventIDs(original.RelatedEventIDs, r.Reference.RelatedEventIDs)
	for _, key := range r.Keys {
		s.forward[messageReferenceKey{r.Account, r.Chat, r.Room, key}] = r.Reference
	}
	s.reverse[matrixKey] = r.Reference
}

func mergeRelatedEventIDs(previous, next []id.EventID) []id.EventID {
	merged := append([]id.EventID(nil), previous...)
	seen := make(map[id.EventID]bool, len(previous)+len(next))
	for _, eventID := range previous {
		seen[eventID] = true
	}
	for _, eventID := range next {
		if !seen[eventID] {
			merged = append(merged, eventID)
			seen[eventID] = true
		}
	}
	return merged
}

func (s *messageReferenceStore) append(r referenceRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	previous := s.reverse[matrixReferenceKey{r.Account, r.Chat, r.Room, r.Reference.EventID}]
	r.Reference.RelatedEventIDs = mergeRelatedEventIDs(previous.RelatedEventIDs, r.Reference.RelatedEventIDs)
	if err := validateReferenceRecord(r); err != nil {
		return err
	}
	if err := s.checkConflict(r); err != nil {
		return err
	}
	if s.path != "" {
		data, err := json.Marshal(r)
		if err != nil {
			return err
		}
		data = append(data, '\n')
		if len(data) > 128*1024 {
			return fmt.Errorf("encoded reference journal record is too large")
		}
		f, err := os.OpenFile(s.path, os.O_WRONLY|os.O_APPEND, 0600)
		if err == nil {
			var n int
			n, err = f.Write(data)
			if err == nil && n != len(data) {
				err = io.ErrShortWrite
			}
			if err == nil {
				err = f.Sync()
			}
			if closeErr := f.Close(); err == nil {
				err = closeErr
			}
		}
		if err != nil {
			s.err = fmt.Errorf("persist message reference: %w", err)
			return s.err // Do not append beyond an uncertain/partial write until reload.
		}
	}
	s.index(r)
	return nil
}

func (s *Server) referenceStore() (*messageReferenceStore, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.referenceStoreErr != nil {
		return nil, s.referenceStoreErr
	}
	if s.references == nil {
		path := ""
		if s.portals != nil && s.portals.path != "" {
			path = s.portals.path + ".messages.jsonl"
		}
		var err error
		s.references, err = openMessageReferenceStore(path)
		if err != nil {
			s.referenceStoreErr = fmt.Errorf("load message reference journal: %w", err)
			return nil, s.referenceStoreErr
		}
	}
	s.references.mu.Lock()
	err := s.references.err
	s.references.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return s.references, nil
}
