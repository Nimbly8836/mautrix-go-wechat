package matrixas

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/id"

	"github.com/leat/mautrix-go-wechat/internal/config"
	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

func TestDuplicateAccountFolderChildrenFindsExtraNamedFolders(t *testing.T) {
	folders := map[string]id.RoomID{
		spaceFolderContacts: id.RoomID("!contacts-current:example.com"),
		spaceFolderOfficial: id.RoomID("!official-current:example.com"),
		spaceFolderGroups:   id.RoomID("!groups-current:example.com"),
	}
	childNames := map[id.RoomID]string{
		id.RoomID("!contacts-current:example.com"): "联系人",
		id.RoomID("!contacts-old:example.com"):     "联系人",
		id.RoomID("!portal:example.com"):           "Some WeChat Group",
		id.RoomID("!groups-current:example.com"):   "群聊",
		id.RoomID("!groups-old:example.com"):       "群聊",
	}
	childSpaces := map[id.RoomID]bool{
		id.RoomID("!contacts-current:example.com"): true,
		id.RoomID("!contacts-old:example.com"):     true,
		id.RoomID("!portal:example.com"):           false,
		id.RoomID("!groups-current:example.com"):   true,
		id.RoomID("!groups-old:example.com"):       true,
	}

	duplicates := duplicateAccountFolderChildren(folders, childNames, childSpaces)

	if len(duplicates) != 2 || duplicates[0] != "!contacts-old:example.com" || duplicates[1] != "!groups-old:example.com" {
		t.Fatalf("unexpected duplicate folder children: %#v", duplicates)
	}
}

func TestDuplicateAccountFolderChildrenDoesNotRemoveCurrentFolders(t *testing.T) {
	folders := map[string]id.RoomID{
		spaceFolderContacts: id.RoomID("!contacts-current:example.com"),
	}
	childNames := map[id.RoomID]string{
		id.RoomID("!contacts-current:example.com"): "联系人",
	}
	childSpaces := map[id.RoomID]bool{
		id.RoomID("!contacts-current:example.com"): true,
	}

	duplicates := duplicateAccountFolderChildren(folders, childNames, childSpaces)

	if len(duplicates) != 0 {
		t.Fatalf("expected no duplicate folder children, got %#v", duplicates)
	}
}

func TestDuplicateAccountFolderChildrenDoesNotRemovePortalNamedLikeFolder(t *testing.T) {
	folders := map[string]id.RoomID{
		spaceFolderGroups: id.RoomID("!groups-current:example.com"),
	}
	childNames := map[id.RoomID]string{
		id.RoomID("!groups-current:example.com"): "群聊",
		id.RoomID("!portal:example.com"):         "群聊",
	}
	childSpaces := map[id.RoomID]bool{
		id.RoomID("!groups-current:example.com"): true,
		id.RoomID("!portal:example.com"):         false,
	}

	duplicates := duplicateAccountFolderChildren(folders, childNames, childSpaces)

	if len(duplicates) != 0 {
		t.Fatalf("expected portal named like folder to be preserved, got %#v", duplicates)
	}
}

func TestEnsureAccountSpaceReturnsRootPersistenceError(t *testing.T) {
	accounts, path := seededAccountStore(t)
	before := readTestFile(t, path)
	injected := errors.New("persist root Space failed")
	accounts.persist = func(string, []byte, fs.FileMode) error { return injected }
	s, matrix := newSpacePersistenceTestServerWithRecorder(t, accounts)

	for attempt := 1; attempt <= 2; attempt++ {
		_, err := s.ensureAccountSpace(context.Background(), remoteapi.UserID("wxid_other"))
		if !errors.Is(err, injected) {
			t.Fatalf("ensureAccountSpace attempt %d error = %v, want persistence failure", attempt, err)
		}
		wantRoom := id.RoomID(fmt.Sprintf("!created-%d:example.com", attempt))
		if !strings.Contains(err.Error(), wantRoom.String()) {
			t.Fatalf("error does not identify newly created room %s: %v", wantRoom, err)
		}
	}
	if acc, _ := accounts.get("wxid_other"); acc.SpaceRoom != "" {
		t.Fatalf("failed root Space was published: %#v", acc)
	}
	if after := readTestFile(t, path); !bytes.Equal(after, before) {
		t.Fatal("root Space persistence failure changed disk")
	}
	if got := matrix.leaveAttempts(); !equalRoomIDs(got, []id.RoomID{"!created-1:example.com", "!created-2:example.com"}) {
		t.Fatalf("root cleanup leave attempts = %#v", got)
	}
}

func TestEnsureAccountSpaceReturnsFolderPersistenceError(t *testing.T) {
	accounts, path := seededAccountStore(t)
	if err := accounts.setSpace("wxid_other", "!root:example.com"); err != nil {
		t.Fatal(err)
	}
	before := readTestFile(t, path)
	injected := errors.New("persist folder Space failed")
	accounts.persist = func(string, []byte, fs.FileMode) error { return injected }
	s, matrix := newSpacePersistenceTestServerWithRecorder(t, accounts)

	_, err := s.ensureAccountSpace(context.Background(), remoteapi.UserID("wxid_other"))
	if !errors.Is(err, injected) {
		t.Fatalf("ensureAccountSpace folder error = %v, want persistence failure", err)
	}
	if acc, _ := accounts.get("wxid_other"); len(acc.SpaceFolders) != 0 {
		t.Fatalf("failed folder Space was published: %#v", acc.SpaceFolders)
	}
	if after := readTestFile(t, path); !bytes.Equal(after, before) {
		t.Fatal("folder Space persistence failure changed disk")
	}
	if got := matrix.leaveAttempts(); !equalRoomIDs(got, []id.RoomID{"!created-1:example.com"}) {
		t.Fatalf("folder cleanup leave attempts = %#v", got)
	}
	if !strings.Contains(err.Error(), "!created-1:example.com") {
		t.Fatalf("folder persistence error does not identify created room: %v", err)
	}
}

func TestRebuildAccountSpaceReturnsReplacementPersistenceError(t *testing.T) {
	accounts, path := seededAccountStore(t)
	beforeAccount, _ := accounts.get("wxid_old")
	before := readTestFile(t, path)
	injected := errors.New("persist replacement Space failed")
	accounts.persist = func(string, []byte, fs.FileMode) error { return injected }
	s, matrix := newSpacePersistenceTestServerWithRecorder(t, accounts)

	_, _, _, err := s.rebuildAccountSpace(context.Background(), "wxid_old", "")
	if !errors.Is(err, injected) {
		t.Fatalf("rebuildAccountSpace error = %v, want persistence failure", err)
	}
	afterAccount, _ := accounts.get("wxid_old")
	if afterAccount.SpaceRoom != beforeAccount.SpaceRoom || len(afterAccount.SpaceFolders) != len(beforeAccount.SpaceFolders) {
		t.Fatalf("failed rebuilt Space was published: got %#v want %#v", afterAccount, beforeAccount)
	}
	if after := readTestFile(t, path); !bytes.Equal(after, before) {
		t.Fatal("replacement Space persistence failure changed disk")
	}
	created := []id.RoomID{"!created-1:example.com", "!created-2:example.com", "!created-3:example.com", "!created-4:example.com"}
	if got := matrix.leaveAttempts(); !equalRoomIDs(got, []id.RoomID{created[1], created[2], created[3], created[0]}) {
		t.Fatalf("rebuild cleanup leave attempts = %#v", got)
	}
	if got := matrix.removedLinks(); len(got) != 6 {
		t.Fatalf("rebuild cleanup removed %d links, want 6: %#v", len(got), got)
	}
	for _, room := range created {
		if !strings.Contains(err.Error(), room.String()) {
			t.Fatalf("rebuild persistence error does not identify created room %s: %v", room, err)
		}
	}
}

func TestSpacePersistenceErrorIncludesCleanupFailures(t *testing.T) {
	accounts, _ := seededAccountStore(t)
	injected := errors.New("persist root Space failed")
	accounts.persist = func(string, []byte, fs.FileMode) error { return injected }
	s, matrix := newSpacePersistenceTestServerWithRecorder(t, accounts)
	matrix.failLeave = true

	_, err := s.ensureAccountSpace(context.Background(), "wxid_other")
	if !errors.Is(err, injected) {
		t.Fatalf("ensureAccountSpace error = %v, want persistence failure", err)
	}
	if !strings.Contains(err.Error(), "cleanup failures") || !strings.Contains(err.Error(), "!created-1:example.com") {
		t.Fatalf("cleanup failure is not actionable: %v", err)
	}
}

type matrixCleanupRecorder struct {
	mu           sync.Mutex
	created      []id.RoomID
	leaves       []id.RoomID
	linkRemovals []string
	failLeave    bool
}

func (rec *matrixCleanupRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodPost && r.URL.Path == "/_matrix/client/v3/createRoom" {
		rec.mu.Lock()
		room := id.RoomID(fmt.Sprintf("!created-%d:example.com", len(rec.created)+1))
		rec.created = append(rec.created, room)
		rec.mu.Unlock()
		_, _ = fmt.Fprintf(w, `{"room_id":%q}`, room)
		return
	}
	if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/leave") {
		parts := strings.Split(r.URL.Path, "/")
		room, _ := url.PathUnescape(parts[len(parts)-2])
		rec.mu.Lock()
		rec.leaves = append(rec.leaves, id.RoomID(room))
		fail := rec.failLeave
		rec.mu.Unlock()
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"errcode":"M_UNKNOWN","error":"leave failed"}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
		return
	}
	if r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/state/") {
		body, _ := io.ReadAll(r.Body)
		if strings.TrimSpace(string(body)) == "{}" {
			rec.mu.Lock()
			rec.linkRemovals = append(rec.linkRemovals, r.URL.Path)
			rec.mu.Unlock()
		}
	}
	_, _ = w.Write([]byte(`{"event_id":"$event"}`))
}

func (rec *matrixCleanupRecorder) leaveAttempts() []id.RoomID {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return append([]id.RoomID(nil), rec.leaves...)
}

func (rec *matrixCleanupRecorder) removedLinks() []string {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return append([]string(nil), rec.linkRemovals...)
}

func equalRoomIDs(got, want []id.RoomID) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func newSpacePersistenceTestServerWithRecorder(t *testing.T, accounts *accountStore) (*Server, *matrixCleanupRecorder) {
	t.Helper()
	matrix := &matrixCleanupRecorder{}
	homeserver := httptest.NewServer(matrix)
	t.Cleanup(homeserver.Close)
	as := appservice.Create()
	as.Registration = &appservice.Registration{SenderLocalpart: "wechatbot", AppToken: "test-token"}
	as.HomeserverDomain = "example.com"
	if err := as.SetHomeserverURL(homeserver.URL); err != nil {
		t.Fatalf("set homeserver URL: %v", err)
	}
	return &Server{
		cfg:      &config.Config{Homeserver: config.Homeserver{Domain: "example.com"}},
		as:       as,
		accounts: accounts,
		portals:  &portalStore{data: portalState{Portals: map[string]portalRecord{}, Pending: map[string]pendingPortal{}}},
	}, matrix
}

func readTestFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
