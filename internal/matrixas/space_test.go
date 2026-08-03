package matrixas

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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

	_, err := s.rebuildAccountSpace(context.Background(), "wxid_old", "")
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

type countingPortalClassificationAPI struct {
	remoteapi.PuppetAPI
	mu    sync.Mutex
	calls []remoteapi.ChatID
}

func (api *countingPortalClassificationAPI) GetChat(_ context.Context, chatID remoteapi.ChatID) (remoteapi.Chat, error) {
	api.mu.Lock()
	api.calls = append(api.calls, chatID)
	api.mu.Unlock()
	return remoteapi.Chat{ID: chatID}, nil
}

func (api *countingPortalClassificationAPI) callCount() int {
	api.mu.Lock()
	defer api.mu.Unlock()
	return len(api.calls)
}

func TestRepairAccountSpaceScansFoldersOnceForPortalBatch(t *testing.T) {
	accounts := newAccountStore(filepath.Join(t.TempDir(), "accounts.json"))
	if err := accounts.put(savedAccount{ID: "wxid_self", SpaceRoom: "!space:example.org", SpaceFolders: builtinEmojiTestFolders("repair")}); err != nil {
		t.Fatal(err)
	}
	s, matrix := newSpacePersistenceTestServerWithRecorder(t, accounts)
	api := &countingPortalClassificationAPI{}
	s.api = api
	s.rawImagePackState = newFakeRawImagePackState()
	s.portals.data.Portals = map[string]portalRecord{
		"one":   {ChatID: "one", RoomID: "!one:example.org"},
		"two":   {ChatID: "two", RoomID: "!two:example.org"},
		"three": {ChatID: "three", RoomID: "!three:example.org"},
	}

	result, err := s.repairAccountSpace(context.Background(), "wxid_self", "")
	if err != nil {
		t.Fatalf("repair account Space: %v", err)
	}
	if scans := matrix.stateReadCount(result.Space); scans != 1 {
		t.Fatalf("repair root-state scans = %d, want one for %d captured portals", scans, result.PortalCount)
	}
	if calls := api.callCount(); calls != result.PortalCount {
		t.Fatalf("portal classifications = %d, want exactly captured PortalCount %d", calls, result.PortalCount)
	}
}

func TestRepairAccountSpaceUsesOnePortalSnapshotDuringConcurrentStoreChange(t *testing.T) {
	accounts := newAccountStore(filepath.Join(t.TempDir(), "accounts.json"))
	if err := accounts.put(savedAccount{ID: "wxid_self", SpaceRoom: "!space:example.org", SpaceFolders: builtinEmojiTestFolders("snapshot")}); err != nil {
		t.Fatal(err)
	}
	s, _ := newSpacePersistenceTestServerWithRecorder(t, accounts)
	api := &countingPortalClassificationAPI{}
	s.api = api
	s.rawImagePackState = newFakeRawImagePackState()
	s.portals.data.Portals = map[string]portalRecord{
		"captured": {ChatID: "captured", RoomID: "!captured:example.org"},
	}
	snapshotCount := 0
	s.afterPortalSnapshot = func(_ []portalRecord) {
		snapshotCount++
		if snapshotCount != 2 {
			return
		}
		changed := make(chan struct{})
		go func() {
			s.portals.mu.Lock()
			s.portals.data.Portals["late"] = portalRecord{ChatID: "late", RoomID: "!late:example.org"}
			s.portals.mu.Unlock()
			close(changed)
		}()
		<-changed
	}

	result, err := s.repairAccountSpace(context.Background(), "wxid_self", "")
	if err != nil {
		t.Fatalf("repair account Space: %v", err)
	}
	if result.PortalCount != 1 {
		t.Fatalf("PortalCount = %d, want exact captured snapshot length 1", result.PortalCount)
	}
	if calls := api.callCount(); calls != result.PortalCount {
		t.Fatalf("attached portals = %d, want exact captured snapshot count %d", calls, result.PortalCount)
	}
	if got := len(s.portals.list()); got != 2 {
		t.Fatalf("concurrent store change did not add late portal: got %d records", got)
	}
}

func TestAttachPortalBatchRejectsStaleAccountSpaceBeforeStructuralWrites(t *testing.T) {
	accounts := newAccountStore(filepath.Join(t.TempDir(), "accounts.json"))
	const account = remoteapi.UserID("wxid_self")
	const capturedSpace = id.RoomID("!captured-space:example.org")
	if err := accounts.put(savedAccount{ID: account, SpaceRoom: capturedSpace.String(), SpaceFolders: builtinEmojiTestFolders("captured")}); err != nil {
		t.Fatal(err)
	}
	s, matrix := newSpacePersistenceTestServerWithRecorder(t, accounts)
	s.rawImagePackState = newFakeRawImagePackState()
	s.portals.data.Portals = map[string]portalRecord{
		"captured": {ChatID: "captured", RoomID: "!portal:example.org"},
	}
	snapshotCount := 0
	s.afterPortalSnapshot = func(_ []portalRecord) {
		snapshotCount++
		if snapshotCount == 2 {
			matrix.mu.Lock()
			matrix.structuralWrites = nil
			matrix.mu.Unlock()
			if err := accounts.replaceSpace(account, "!replacement-space:example.org", builtinEmojiTestFolders("replacement")); err != nil {
				t.Fatalf("replace captured Space: %v", err)
			}
		}
	}

	_, err := s.repairAccountSpace(context.Background(), account, "")
	if err == nil || !strings.Contains(err.Error(), "stale account Space transition") {
		t.Fatalf("repair error = %v, want stale-transition error", err)
	}
	if writes := matrix.structuralWritePaths(); len(writes) != 0 {
		t.Fatalf("stale portal batch performed structural writes: %#v", writes)
	}
}

func TestConcurrentRebuildRejectsStalePortalBatch(t *testing.T) {
	accounts := newAccountStore(filepath.Join(t.TempDir(), "accounts.json"))
	const account = remoteapi.UserID("wxid_self")
	if err := accounts.put(savedAccount{ID: account, SpaceRoom: "!old-space:example.org", SpaceFolders: builtinEmojiTestFolders("old")}); err != nil {
		t.Fatal(err)
	}
	s, _ := newSpacePersistenceTestServerWithRecorder(t, accounts)
	s.rawImagePackState = newFakeRawImagePackState()
	s.portals.data.Portals = map[string]portalRecord{
		"captured": {ChatID: "captured", RoomID: "!portal:example.org"},
	}
	var hookMu sync.Mutex
	hookCount := 0
	secondDone := make(chan struct {
		result spaceTransitionResult
		err    error
	}, 1)
	var concurrent spaceTransitionResult
	var concurrentErr error
	s.afterPortalSnapshot = func(_ []portalRecord) {
		hookMu.Lock()
		hookCount++
		startConcurrent := hookCount == 1
		hookMu.Unlock()
		if !startConcurrent {
			return
		}
		go func() {
			result, err := s.rebuildAccountSpace(context.Background(), account, "")
			secondDone <- struct {
				result spaceTransitionResult
				err    error
			}{result: result, err: err}
		}()
		finished := <-secondDone
		concurrent = finished.result
		concurrentErr = finished.err
	}

	first, err := s.rebuildAccountSpace(context.Background(), account, "")
	if err == nil || !strings.Contains(err.Error(), "stale account Space transition") {
		t.Fatalf("first rebuild result=%#v error=%v, want stale-transition error", first, err)
	}
	if concurrentErr != nil {
		t.Fatalf("concurrent rebuild result=%#v error=%v", concurrent, concurrentErr)
	}
	current, _ := accounts.get(account)
	if current.SpaceRoom != concurrent.Space.String() || current.SpaceRoom == first.Space.String() {
		t.Fatalf("current Space = %s, first=%s concurrent=%s", current.SpaceRoom, first.Space, concurrent.Space)
	}
}

func TestRebuildAccountSpaceDoesNotRescanRootForPortalBatch(t *testing.T) {
	accounts := newAccountStore(filepath.Join(t.TempDir(), "accounts.json"))
	if err := accounts.put(savedAccount{ID: "wxid_self", SpaceRoom: "!old-space:example.org", SpaceFolders: builtinEmojiTestFolders("old")}); err != nil {
		t.Fatal(err)
	}
	s, matrix := newSpacePersistenceTestServerWithRecorder(t, accounts)
	api := &countingPortalClassificationAPI{}
	s.api = api
	s.rawImagePackState = newFakeRawImagePackState()
	s.portals.data.Portals = map[string]portalRecord{
		"one":   {ChatID: "one", RoomID: "!one:example.org"},
		"two":   {ChatID: "two", RoomID: "!two:example.org"},
		"three": {ChatID: "three", RoomID: "!three:example.org"},
	}

	result, err := s.rebuildAccountSpace(context.Background(), "wxid_self", "")
	if err != nil {
		t.Fatalf("rebuild account Space: %v", err)
	}
	if scans := matrix.stateReadCount(result.Space); scans != 0 {
		t.Fatalf("rebuilt root-state scans = %d, want zero after folders were captured during rebuild", scans)
	}
	if calls := api.callCount(); calls != result.PortalCount {
		t.Fatalf("portal classifications = %d, want exactly captured PortalCount %d", calls, result.PortalCount)
	}
}

type contextBlockingPortalClassificationAPI struct {
	remoteapi.PuppetAPI
	started chan<- struct{}
	release <-chan struct{}
}

func (api contextBlockingPortalClassificationAPI) GetChat(ctx context.Context, chatID remoteapi.ChatID) (remoteapi.Chat, error) {
	api.started <- struct{}{}
	select {
	case <-ctx.Done():
		return remoteapi.Chat{}, ctx.Err()
	case <-api.release:
		return remoteapi.Chat{ID: chatID}, nil
	}
}

func TestPortalSpaceFolderDeadlineReleasesSpaceLock(t *testing.T) {
	accounts := newAccountStore(filepath.Join(t.TempDir(), "accounts.json"))
	if err := accounts.put(savedAccount{ID: "wxid_self", SpaceRoom: "!space:example.org", SpaceFolders: builtinEmojiTestFolders("deadline")}); err != nil {
		t.Fatal(err)
	}
	s, _ := newSpacePersistenceTestServerWithRecorder(t, accounts)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	s.api = contextBlockingPortalClassificationAPI{started: started, release: release}
	s.rawImagePackState = newFakeRawImagePackState()
	s.matrixOperationTimeout = 20 * time.Millisecond
	done := make(chan error, 1)
	go func() {
		done <- s.addPortalToAccountSpace(context.Background(), "wxid_self", portalRecord{ChatID: "wxid_friend", RoomID: "!portal:example.org"})
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("portal classification did not call the remote API")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("attach portal after classification deadline: %v", err)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("portal classification ignored the operation deadline while holding spaceMu")
	}

	lockReleased := make(chan struct{})
	go func() {
		s.spaceMu.Lock()
		s.spaceMu.Unlock()
		close(lockReleased)
	}()
	select {
	case <-lockReleased:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("spaceMu remained held after portal classification deadline")
	}
}

func TestSpaceMatrixOperationDeadlineReleasesSpaceLock(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var blockOnce sync.Once
	homeserver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		blockOnce.Do(func() {
			started <- struct{}{}
			select {
			case <-r.Context().Done():
			case <-release:
			}
		})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"event_id":"$event"}`))
	}))
	t.Cleanup(homeserver.Close)
	s := &Server{
		cfg:                    &config.Config{Homeserver: config.Homeserver{Domain: "example.com"}},
		as:                     testAppservice(t, homeserver.URL),
		matrixOperationTimeout: 20 * time.Millisecond,
	}

	operationDone := make(chan struct{})
	go func() {
		s.spaceMu.Lock()
		s.addSpaceChild(context.Background(), "!space:example.com", "!child:example.com", true)
		s.spaceMu.Unlock()
		close(operationDone)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("Matrix operation did not reach deterministic barrier")
	}

	lockAcquired := make(chan struct{})
	go func() {
		s.spaceMu.Lock()
		close(lockAcquired)
		s.spaceMu.Unlock()
	}()
	select {
	case <-lockAcquired:
	case <-time.After(250 * time.Millisecond):
		close(release)
		<-operationDone
		t.Fatal("spaceMu remained held after the configured Matrix operation deadline")
	}
	close(release)
	<-operationDone
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
	mu                    sync.Mutex
	created               []id.RoomID
	leaves                []id.RoomID
	linkRemovals          []string
	structuralWrites      []string
	notices               []string
	stateReads            map[id.RoomID]int
	failLeave             bool
	accountDataGetStarted chan<- struct{}
	accountDataGetRelease <-chan struct{}
}

func (rec *matrixCleanupRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if strings.Contains(r.URL.Path, "/account_data/") {
		if r.Method == http.MethodGet {
			if rec.accountDataGetStarted != nil {
				select {
				case rec.accountDataGetStarted <- struct{}{}:
				default:
				}
			}
			if rec.accountDataGetRelease != nil {
				select {
				case <-r.Context().Done():
					return
				case <-rec.accountDataGetRelease:
				}
			}
			_, _ = w.Write([]byte(`{"rooms":{}}`))
			return
		}
		if r.Method == http.MethodPut {
			_, _ = w.Write([]byte(`{}`))
			return
		}
	}
	if r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/send/m.room.message/") {
		var content struct {
			MsgType string `json:"msgtype"`
			Body    string `json:"body"`
		}
		if err := json.NewDecoder(r.Body).Decode(&content); err == nil && content.MsgType == "m.notice" {
			rec.mu.Lock()
			rec.notices = append(rec.notices, content.Body)
			rec.mu.Unlock()
		}
		_, _ = w.Write([]byte(`{"event_id":"$event"}`))
		return
	}
	if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/state") {
		parts := strings.Split(r.URL.Path, "/")
		room, _ := url.PathUnescape(parts[len(parts)-2])
		rec.mu.Lock()
		if rec.stateReads == nil {
			rec.stateReads = map[id.RoomID]int{}
		}
		rec.stateReads[id.RoomID(room)]++
		rec.mu.Unlock()
		_, _ = w.Write([]byte(`[]`))
		return
	}
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
		rec.mu.Lock()
		if strings.Contains(r.URL.Path, "/m.space.child/") || strings.Contains(r.URL.Path, "/m.space.parent/") {
			rec.structuralWrites = append(rec.structuralWrites, r.URL.Path)
		}
		if strings.TrimSpace(string(body)) == "{}" {
			rec.linkRemovals = append(rec.linkRemovals, r.URL.Path)
		}
		rec.mu.Unlock()
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

func (rec *matrixCleanupRecorder) structuralWritePaths() []string {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return append([]string(nil), rec.structuralWrites...)
}

func (rec *matrixCleanupRecorder) noticeBodies() []string {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return append([]string(nil), rec.notices...)
}

func (rec *matrixCleanupRecorder) stateReadCount(room id.RoomID) int {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return rec.stateReads[room]
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
		cfg:      &config.Config{Homeserver: config.Homeserver{Address: homeserver.URL, Domain: "example.com"}},
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
