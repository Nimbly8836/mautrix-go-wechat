package matrixas

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leat/mautrix-go-wechat/internal/config"
	"github.com/leat/mautrix-go-wechat/internal/connector"
	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

func TestAccountStoreMutationFailuresLeaveMemoryAndDiskUnchanged(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*accountStore) error
	}{
		{"put", func(as *accountStore) error { return as.put(savedAccount{ID: "wxid_new", Session: []byte("new")}) }},
		{"set-space", func(as *accountStore) error { return as.setSpace("wxid_old", "!new:example.com") }},
		{"set-space-folder", func(as *accountStore) error { return as.setSpaceFolder("wxid_old", "groups", "!groups:example.com") }},
		{"replace-space", func(as *accountStore) error {
			return as.replaceSpace("wxid_old", "!replacement:example.com", map[string]string{"contacts": "!contacts:example.com"})
		}},
		{"set-current", func(as *accountStore) error {
			changed, err := as.setCurrent("wxid_other")
			if changed {
				t.Fatal("setCurrent reported success on persistence failure")
			}
			return err
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			as, path := seededAccountStore(t)
			beforeMemory := cloneAccountStoreData(as.data)
			beforeDisk, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			injected := errors.New("persistence failed")
			as.persist = func(string, []byte, fs.FileMode) error { return injected }

			err = tc.mutate(as)
			if !errors.Is(err, injected) {
				t.Fatalf("error = %v, want persistence failure", err)
			}
			if !reflect.DeepEqual(as.data, beforeMemory) {
				t.Fatalf("memory changed after failed mutation:\n got %#v\nwant %#v", as.data, beforeMemory)
			}
			afterDisk, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if !bytes.Equal(afterDisk, beforeDisk) {
				t.Fatalf("disk changed after failed mutation:\n got %q\nwant %q", afterDisk, beforeDisk)
			}
		})
	}
}

func TestAccountStoreGettersReturnDeepCopies(t *testing.T) {
	as, _ := seededAccountStore(t)

	got, ok := as.get("wxid_old")
	if !ok {
		t.Fatal("missing seeded account")
	}
	got.Session[0] = 'X'
	got.SpaceFolders["contacts"] = "changed"

	current, ok := as.current()
	if !ok {
		t.Fatal("missing current account")
	}
	current.Session[0] = 'Y'
	current.SpaceFolders["contacts"] = "changed-again"

	list := as.list()
	for i := range list {
		if list[i].ID == "wxid_old" {
			list[i].Session[0] = 'Z'
			list[i].SpaceFolders["contacts"] = "changed-from-list"
		}
	}

	unchanged, _ := as.get("wxid_old")
	if string(unchanged.Session) != "old-session" {
		t.Fatalf("stored session mutated through getter: %q", unchanged.Session)
	}
	if unchanged.SpaceFolders["contacts"] != "!old-contacts:example.com" {
		t.Fatalf("stored folders mutated through getter: %#v", unchanged.SpaceFolders)
	}
}

func TestAccountStoreReplaceSpacePersistsRetiredRoomAndDeepCopies(t *testing.T) {
	as, path := seededAccountStore(t)
	const oldSpace = "!old-space:example.com"
	const newSpace = "!replacement:example.com"
	account, _ := as.get("wxid_old")
	account.SpaceRoom = oldSpace
	account.RetiredSpaceRooms = []string{"!previously-retired:example.com"}
	if err := as.put(account); err != nil {
		t.Fatal(err)
	}

	if err := as.replaceSpace("wxid_old", newSpace, map[string]string{"contacts": "!contacts:example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := as.put(savedAccount{ID: "wxid_old", Session: []byte("fresh-session")}); err != nil {
		t.Fatal(err)
	}
	got, _ := as.get("wxid_old")
	want := []string{"!previously-retired:example.com", oldSpace}
	if !reflect.DeepEqual(got.RetiredSpaceRooms, want) {
		t.Fatalf("retired Spaces = %#v, want %#v", got.RetiredSpaceRooms, want)
	}
	got.RetiredSpaceRooms[0] = "mutated"
	unchanged, _ := as.get("wxid_old")
	if !reflect.DeepEqual(unchanged.RetiredSpaceRooms, want) {
		t.Fatalf("retired Spaces mutated through getter: %#v", unchanged.RetiredSpaceRooms)
	}

	reloaded := newAccountStore(path)
	persisted, _ := reloaded.get("wxid_old")
	if !reflect.DeepEqual(persisted.RetiredSpaceRooms, want) {
		t.Fatalf("reloaded retired Spaces = %#v, want %#v", persisted.RetiredSpaceRooms, want)
	}
}

func TestAccountStoreClearsRetiredRoomsAtomicallyAfterPersistence(t *testing.T) {
	as, path := seededAccountStore(t)
	account, _ := as.get("wxid_old")
	account.RetiredSpaceRooms = []string{"!one:example.com", "!two:example.com"}
	if err := as.put(account); err != nil {
		t.Fatal(err)
	}

	injected := errors.New("clear retired persistence failed")
	originalPersist := as.persist
	as.persist = func(string, []byte, fs.FileMode) error { return injected }
	if err := as.clearRetiredSpaceRooms([]string{"!one:example.com"}); !errors.Is(err, injected) {
		t.Fatalf("clear error = %v, want persistence failure", err)
	}
	pending, _ := as.get("wxid_old")
	if !reflect.DeepEqual(pending.RetiredSpaceRooms, []string{"!one:example.com", "!two:example.com"}) {
		t.Fatalf("failed clear published pending IDs: %#v", pending.RetiredSpaceRooms)
	}

	as.persist = originalPersist
	if err := as.clearRetiredSpaceRooms([]string{"!one:example.com", "!two:example.com"}); err != nil {
		t.Fatal(err)
	}
	reloaded := newAccountStore(path)
	cleared, _ := reloaded.get("wxid_old")
	if len(cleared.RetiredSpaceRooms) != 0 {
		t.Fatalf("reloaded retired Spaces = %#v, want empty", cleared.RetiredSpaceRooms)
	}
}

func TestAccountStorePutCopiesCallerOwnedData(t *testing.T) {
	as, _ := seededAccountStore(t)
	acc := savedAccount{ID: "wxid_new", Session: []byte("session"), SpaceFolders: map[string]string{"groups": "!groups:example.com"}}
	if err := as.put(acc); err != nil {
		t.Fatal(err)
	}
	acc.Session[0] = 'X'
	acc.SpaceFolders["groups"] = "changed"
	got, _ := as.get("wxid_new")
	if string(got.Session) != "session" || got.SpaceFolders["groups"] != "!groups:example.com" {
		t.Fatalf("caller mutated published account: %#v", got)
	}
}

func TestAccountStoreSuccessfulMutationsReload(t *testing.T) {
	as, path := seededAccountStore(t)
	if err := as.put(savedAccount{ID: "wxid_new", Protocol: "wechat", Session: []byte("new-session")}); err != nil {
		t.Fatal(err)
	}
	if err := as.setSpace("wxid_new", "!space:example.com"); err != nil {
		t.Fatal(err)
	}
	if err := as.setSpaceFolder("wxid_new", "groups", "!groups:example.com"); err != nil {
		t.Fatal(err)
	}
	if err := as.replaceSpace("wxid_new", "!replacement:example.com", map[string]string{"contacts": "!contacts:example.com"}); err != nil {
		t.Fatal(err)
	}
	changed, err := as.setCurrent("wxid_old")
	if err != nil || !changed {
		t.Fatalf("setCurrent = %v, %v", changed, err)
	}

	reloaded := newAccountStore(path)
	if current, ok := reloaded.current(); !ok || current.ID != "wxid_old" {
		t.Fatalf("reloaded current = %#v, %v", current, ok)
	}
	got, ok := reloaded.get("wxid_new")
	if !ok || got.SpaceRoom != "!replacement:example.com" || got.SpaceFolders["contacts"] != "!contacts:example.com" {
		t.Fatalf("reloaded account = %#v, %v", got, ok)
	}
}

func TestAccountStorePersistsBeforePublishingCurrent(t *testing.T) {
	as, _ := seededAccountStore(t)
	as.persist = func(_ string, data []byte, _ fs.FileMode) error {
		if as.data.Current != "wxid_old" {
			t.Fatalf("live current published before persistence: %s", as.data.Current)
		}
		var pending accountStoreData
		if err := json.Unmarshal(data, &pending); err != nil {
			t.Fatal(err)
		}
		if pending.Current != "wxid_other" {
			t.Fatalf("persisted current = %s, want wxid_other", pending.Current)
		}
		return nil
	}
	changed, err := as.setCurrent("wxid_other")
	if err != nil || !changed {
		t.Fatalf("setCurrent = %v, %v", changed, err)
	}
	if current, _ := as.current(); current.ID != "wxid_other" {
		t.Fatalf("published current = %s", current.ID)
	}
}

func TestAccountStoreSetCurrentUnknownDoesNotPersist(t *testing.T) {
	as, _ := seededAccountStore(t)
	called := false
	as.persist = func(string, []byte, fs.FileMode) error { called = true; return nil }
	changed, err := as.setCurrent("missing")
	if err != nil || changed {
		t.Fatalf("setCurrent missing = %v, %v", changed, err)
	}
	if called {
		t.Fatal("unknown current account triggered persistence")
	}
}

type accountSessionAPI struct {
	remoteapi.PuppetAPI
	self        remoteapi.UserID
	session     []byte
	dumpCalled  chan struct{}
	dumpRelease <-chan struct{}
}

func (api *accountSessionAPI) Self(context.Context) (remoteapi.UserID, error) {
	return api.self, nil
}

func (api *accountSessionAPI) DumpSession() ([]byte, error) {
	if api.dumpCalled != nil {
		api.dumpCalled <- struct{}{}
	}
	if api.dumpRelease != nil {
		<-api.dumpRelease
	}
	return append([]byte(nil), api.session...), nil
}

func TestUseSavedAccountPersistenceFailureKeepsLiveConnectorAndCurrentAccount(t *testing.T) {
	as, _ := seededAccountStore(t)
	injected := errors.New("persist current failed")
	as.persist = func(string, []byte, fs.FileMode) error { return injected }
	oldConn := &connector.Connector{}
	oldAPI := &accountSessionAPI{self: "wxid_old", session: []byte("old-session")}
	s := &Server{
		accounts: as,
		conn:     oldConn,
		api:      oldAPI,
		newConnector: func(string, remoteapi.Config, connector.EventSink) (*connector.Connector, error) {
			return &connector.Connector{}, nil
		},
	}

	err := s.useSavedAccount("wxid_other")
	if !errors.Is(err, injected) {
		t.Fatalf("useSavedAccount error = %v, want persistence failure", err)
	}
	if s.conn != oldConn || s.api != oldAPI {
		t.Fatal("live connector or API changed after persistence failure")
	}
	current, ok := as.current()
	if !ok || current.ID != "wxid_old" {
		t.Fatalf("current account changed after persistence failure: %#v, %v", current, ok)
	}
}

func TestUseSavedAccountPersistsCurrentBeforeReplacingConnector(t *testing.T) {
	as, _ := seededAccountStore(t)
	oldConn := &connector.Connector{}
	newConn := &connector.Connector{}
	oldAPI := &accountSessionAPI{self: "wxid_old", session: []byte("old-session")}
	s := &Server{
		accounts: as,
		conn:     oldConn,
		api:      oldAPI,
		cfg:      &config.Config{Network: config.Network{Protocol: "wechat"}},
		newConnector: func(string, remoteapi.Config, connector.EventSink) (*connector.Connector, error) {
			return newConn, nil
		},
	}
	originalPersist := as.persist
	as.persist = func(path string, data []byte, mode fs.FileMode) error {
		if s.conn != oldConn || s.api != oldAPI {
			t.Fatal("live connector changed before current account persistence")
		}
		var pending accountStoreData
		if err := json.Unmarshal(data, &pending); err != nil {
			t.Fatal(err)
		}
		if pending.Current != "wxid_other" {
			t.Fatalf("pending current = %s, want wxid_other", pending.Current)
		}
		return originalPersist(path, data, mode)
	}

	if err := s.useSavedAccount("wxid_other"); err != nil {
		t.Fatalf("useSavedAccount: %v", err)
	}
	if s.conn != newConn {
		t.Fatal("prepared connector was not published after persistence")
	}
	if current, ok := as.current(); !ok || current.ID != "wxid_other" {
		t.Fatalf("current account = %#v, %v", current, ok)
	}
}

func TestUseSavedAccountConnectorBuildFailureKeepsLiveConnectorAndCurrentAccount(t *testing.T) {
	as, _ := seededAccountStore(t)
	other, _ := as.get("wxid_other")
	other.Protocol = "unregistered-protocol"
	if err := as.put(other); err != nil {
		t.Fatal(err)
	}
	if changed, err := as.setCurrent("wxid_old"); err != nil || !changed {
		t.Fatalf("restore current = %v, %v", changed, err)
	}
	oldConn := &connector.Connector{}
	oldAPI := &accountSessionAPI{self: "wxid_old", session: []byte("old-session")}
	s := &Server{
		accounts: as,
		conn:     oldConn,
		api:      oldAPI,
		cfg:      &config.Config{Network: config.Network{Protocol: "wechat"}},
		newConnector: func(string, remoteapi.Config, connector.EventSink) (*connector.Connector, error) {
			return nil, errors.New("connector build failed")
		},
	}

	if err := s.useSavedAccount("wxid_other"); err == nil {
		t.Fatal("expected connector build failure")
	}
	if s.conn != oldConn || s.api != oldAPI {
		t.Fatal("live connector or API changed after connector build failure")
	}
	current, ok := as.current()
	if !ok || current.ID != "wxid_old" {
		t.Fatalf("current account changed after connector build failure: %#v, %v", current, ok)
	}
}

func TestAccountTransitionBlocksSessionSnapshotUntilSavedAccountPersistenceFinishes(t *testing.T) {
	as, _ := seededAccountStore(t)
	persistStarted := make(chan struct{})
	releasePersist := make(chan struct{})
	injected := errors.New("persist current failed")
	var persistCalls atomic.Int32
	originalPersist := as.persist
	as.persist = func(path string, data []byte, mode fs.FileMode) error {
		if persistCalls.Add(1) == 1 {
			close(persistStarted)
			<-releasePersist
			return injected
		}
		return originalPersist(path, data, mode)
	}

	dumpCalled := make(chan struct{}, 1)
	oldConn := &connector.Connector{}
	oldAPI := &accountSessionAPI{self: "wxid_old", session: []byte("fresh-old-session"), dumpCalled: dumpCalled}
	s := &Server{
		accounts: as,
		conn:     oldConn,
		api:      oldAPI,
		cfg:      &config.Config{Network: config.Network{Protocol: "wechat"}},
		newConnector: func(string, remoteapi.Config, connector.EventSink) (*connector.Connector, error) {
			return &connector.Connector{}, nil
		},
	}

	useDone := make(chan error, 1)
	go func() { useDone <- s.useSavedAccount("wxid_other") }()
	select {
	case <-persistStarted:
	case <-time.After(time.Second):
		t.Fatal("useSavedAccount did not reach blocked Current persistence")
	}

	saveDone := make(chan error, 1)
	go func() { saveDone <- s.saveSession("cache") }()
	select {
	case <-dumpCalled:
		t.Fatal("saveSession read the live API while account transition persistence was blocked")
	case <-time.After(50 * time.Millisecond):
	}

	close(releasePersist)
	if err := <-useDone; !errors.Is(err, injected) {
		t.Fatalf("useSavedAccount error = %v, want persistence failure", err)
	}
	select {
	case <-dumpCalled:
	case <-time.After(time.Second):
		t.Fatal("saveSession did not resume after account transition finished")
	}
	if err := <-saveDone; err != nil {
		t.Fatalf("saveSession: %v", err)
	}

	if current, ok := as.current(); !ok || current.ID != "wxid_old" {
		t.Fatalf("failed transition crossed durable Current: %#v, %v", current, ok)
	}
	if s.conn != oldConn || s.api != oldAPI {
		t.Fatal("failed transition changed the live connector snapshot")
	}
	old, _ := as.get("wxid_old")
	if string(old.Session) != "fresh-old-session" || old.Protocol != "wechat" {
		t.Fatalf("saveSession did not persist one old-API snapshot: %#v", old)
	}
}

func TestAccountTransitionBlocksSavedAccountSwitchUntilAPISnapshotPersists(t *testing.T) {
	as, _ := seededAccountStore(t)
	other, _ := as.get("wxid_other")
	other.Protocol = "new-protocol"
	if err := as.put(other); err != nil {
		t.Fatal(err)
	}
	if changed, err := as.setCurrent("wxid_old"); err != nil || !changed {
		t.Fatalf("restore current = %v, %v", changed, err)
	}

	dumpCalled := make(chan struct{}, 1)
	releaseDump := make(chan struct{})
	buildCalled := make(chan struct{}, 1)
	oldAPI := &accountSessionAPI{
		self:        "wxid_old",
		session:     []byte("fresh-old-session"),
		dumpCalled:  dumpCalled,
		dumpRelease: releaseDump,
	}
	newConn := &connector.Connector{}
	s := &Server{
		accounts: as,
		conn:     &connector.Connector{},
		api:      oldAPI,
		cfg:      &config.Config{Network: config.Network{Protocol: "wechat"}},
		newConnector: func(string, remoteapi.Config, connector.EventSink) (*connector.Connector, error) {
			buildCalled <- struct{}{}
			return newConn, nil
		},
	}

	saveDone := make(chan error, 1)
	go func() { saveDone <- s.saveSession("cache") }()
	select {
	case <-dumpCalled:
	case <-time.After(time.Second):
		t.Fatal("saveSession did not begin the live API snapshot")
	}

	useDone := make(chan error, 1)
	go func() { useDone <- s.useSavedAccount("wxid_other") }()
	select {
	case <-buildCalled:
		t.Fatal("saved-account transition began before the old API snapshot persisted")
	case <-time.After(50 * time.Millisecond):
	}

	close(releaseDump)
	if err := <-saveDone; err != nil {
		t.Fatalf("saveSession: %v", err)
	}
	select {
	case <-buildCalled:
	case <-time.After(time.Second):
		t.Fatal("saved-account transition did not resume after session persistence")
	}
	if err := <-useDone; err != nil {
		t.Fatalf("useSavedAccount: %v", err)
	}

	old, _ := as.get("wxid_old")
	if string(old.Session) != "fresh-old-session" || old.Protocol != "wechat" {
		t.Fatalf("persisted session mixed API/protocol snapshots: %#v", old)
	}
	if current, ok := as.current(); !ok || current.ID != "wxid_other" {
		t.Fatalf("current account after transition = %#v, %v", current, ok)
	}
	if s.conn != newConn || s.api == oldAPI {
		t.Fatal("saved-account connector was not published after snapshot persistence")
	}
}

func TestSaveSessionReturnsPersistenceFailureWithoutPublishing(t *testing.T) {
	as, path := seededAccountStore(t)
	beforeDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("persist session failed")
	as.persist = func(string, []byte, fs.FileMode) error { return injected }
	s := &Server{
		accounts: as,
		api:      &accountSessionAPI{self: "wxid_new", session: []byte("new-session")},
		cfg:      &config.Config{Network: config.Network{Protocol: "wechat"}},
	}

	err = s.saveSession("cache")
	if !errors.Is(err, injected) {
		t.Fatalf("saveSession error = %v, want persistence failure", err)
	}
	if _, ok := as.get("wxid_new"); ok {
		t.Fatal("failed session persistence published account in memory")
	}
	afterDisk, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(afterDisk, beforeDisk) {
		t.Fatal("failed session persistence changed account store on disk")
	}
}

func seededAccountStore(t *testing.T) (*accountStore, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "accounts.json")
	as := newAccountStore(path)
	if err := as.put(savedAccount{
		ID: "wxid_old", Protocol: "wechat", Session: []byte("old-session"),
		SpaceRoom: "!old:example.com", SpaceFolders: map[string]string{"contacts": "!old-contacts:example.com"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := as.put(savedAccount{ID: "wxid_other", Protocol: "wechat", Session: []byte("other-session")}); err != nil {
		t.Fatal(err)
	}
	changed, err := as.setCurrent(remoteapi.UserID("wxid_old"))
	if err != nil || !changed {
		t.Fatalf("seed current = %v, %v", changed, err)
	}
	return as, path
}
