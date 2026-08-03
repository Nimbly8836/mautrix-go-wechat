package matrixas

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

type rawPackStateKey struct {
	room      id.RoomID
	eventType string
	stateKey  string
}

type fakeRawImagePackState struct {
	mu         sync.Mutex
	states     map[rawPackStateKey]json.RawMessage
	loads      int
	saves      int
	saveCalls  []rawPackStateKey
	saveErrors map[int]error

	loadErrors map[int]error
	beforeLoad func(int)
}

func newFakeRawImagePackState() *fakeRawImagePackState {
	return &fakeRawImagePackState{states: make(map[rawPackStateKey]json.RawMessage)}
}

func (f *fakeRawImagePackState) key(room id.RoomID, eventType event.Type, stateKey string) rawPackStateKey {
	return rawPackStateKey{room: room, eventType: eventType.Type, stateKey: stateKey}
}

func (f *fakeRawImagePackState) Load(_ context.Context, room id.RoomID, eventType event.Type, stateKey string) (json.RawMessage, error) {
	f.mu.Lock()
	f.loads++
	call := f.loads
	f.mu.Unlock()
	if f.beforeLoad != nil {
		f.beforeLoad(call)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.loadErrors[call]; err != nil {
		return nil, err
	}
	raw, ok := f.states[f.key(room, eventType, stateKey)]
	if !ok {
		return nil, errImagePackStateNotFound
	}
	return append(json.RawMessage(nil), raw...), nil
}

func (f *fakeRawImagePackState) Save(_ context.Context, room id.RoomID, eventType event.Type, stateKey string, raw json.RawMessage) error {
	f.mu.Lock()
	f.saves++
	call := f.saves
	key := f.key(room, eventType, stateKey)
	f.saveCalls = append(f.saveCalls, key)
	err := f.saveErrors[call]
	f.mu.Unlock()
	if err != nil {
		return err
	}
	f.mu.Lock()
	f.states[key] = append(json.RawMessage(nil), raw...)
	f.mu.Unlock()
	return nil
}

func (f *fakeRawImagePackState) set(room id.RoomID, eventType event.Type, stateKey string, raw string) {
	f.mu.Lock()
	f.states[f.key(room, eventType, stateKey)] = json.RawMessage(raw)
	f.mu.Unlock()
}

func (f *fakeRawImagePackState) get(room id.RoomID, eventType event.Type, stateKey string) json.RawMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append(json.RawMessage(nil), f.states[f.key(room, eventType, stateKey)]...)
}

func TestImagePackGenericLoadErrorAbortsMutationWithoutSave(t *testing.T) {
	fake := newFakeRawImagePackState()
	account := remoteapi.UserID("wxid_self")
	room := id.RoomID("!space:example.org")
	stateKey := wechatStickerPackStateKey(account)
	fake.set(room, event.StateImagePack, stateKey, `{"images":{}}`)
	fake.set(room, event.StateUnstableImagePack, "", `{"images":{}}`)
	fake.loadErrors = map[int]error{2: context.DeadlineExceeded}
	s := &Server{rawImagePackState: fake}

	_, err := s.addStickerToAccountSpace(context.Background(), account, room, wechatStickerImage{
		Shortcode: "new",
		Body:      "New",
		URL:       "mxc://example.org/new",
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("add error = %v, want context deadline exceeded", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.saves != 0 {
		t.Fatalf("generic load error caused %d saves, want zero", fake.saves)
	}
}

func TestImagePackMissingStateCreatesWechatBase(t *testing.T) {
	fake := newFakeRawImagePackState()
	account := remoteapi.UserID("wxid_self")
	room := id.RoomID("!space:example.org")
	s := &Server{rawImagePackState: fake}

	count, err := s.addStickerToAccountSpace(context.Background(), account, room, wechatStickerImage{Shortcode: "new", URL: "mxc://example.org/new"})
	if err != nil {
		t.Fatalf("add sticker to missing pack: %v", err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
	assertRawPackPaths(t, fake.get(room, event.StateImagePack, wechatStickerPackStateKey(account)), map[string]any{
		"pack.display_name": "WeChat wxid_self stickers",
		"images.new.url":    "mxc://example.org/new",
	})
	assertCompatibilityPackHasNoMetadata(t, fake.get(room, event.StateUnstableImagePack, ""))
}

func TestImagePackAddCountsPreparedCanonicalWithoutReload(t *testing.T) {
	fake := newFakeRawImagePackState()
	account := remoteapi.UserID("wxid_self")
	room := id.RoomID("!space:example.org")
	stateKey := wechatStickerPackStateKey(account)
	fake.set(room, event.StateImagePack, stateKey, `{"images":{}}`)
	fake.set(room, event.StateUnstableImagePack, "", `{"images":{}}`)
	s := &Server{rawImagePackState: fake}

	count, err := s.addStickerToAccountSpace(context.Background(), account, room, wechatStickerImage{Shortcode: "new", URL: "mxc://example.org/new"})
	if err != nil {
		t.Fatalf("add sticker: %v", err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.loads != 2 {
		t.Fatalf("load calls = %d, want exactly the two pre-write canonical loads", fake.loads)
	}
}

func TestImagePackCanonicalWritesPrecedeAliasCleanupAndReportPartialFailures(t *testing.T) {
	account := remoteapi.UserID("wxid_self")
	room := id.RoomID("!space:example.org")
	stateKey := wechatStickerPackStateKey(account)
	wantCalls := []rawPackStateKey{
		{room: room, eventType: event.StateImagePack.Type, stateKey: stateKey},
		{room: room, eventType: event.StateUnstableImagePack.Type, stateKey: ""},
		{room: room, eventType: event.StateUnstableImagePack.Type, stateKey: stateKey},
	}
	tests := []struct {
		name          string
		failureAt     int
		wantCommitted string
		wantPartial   string
	}{
		{name: "stable canonical", failureAt: 1, wantCommitted: "canonical targets committed: 0/2", wantPartial: "partial state: no"},
		{name: "unstable canonical", failureAt: 2, wantCommitted: "canonical targets committed: 1/2", wantPartial: "partial state: yes"},
		{name: "alias cleanup", failureAt: 3, wantCommitted: "canonical writes committed: 2/2", wantPartial: "partial state: alias cleanup incomplete"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := newFakeRawImagePackState()
			fake.set(room, event.StateImagePack, stateKey, `{"images":{}}`)
			fake.set(room, event.StateUnstableImagePack, "", `{"images":{}}`)
			fake.set(room, event.StateUnstableImagePack, stateKey, `{"images":{"legacy":{"url":"mxc://example.org/legacy"}}}`)
			fake.saveErrors = map[int]error{test.failureAt: errors.New("deterministic save failure")}
			s := &Server{rawImagePackState: fake}

			_, err := s.addStickerToAccountSpace(context.Background(), account, room, wechatStickerImage{
				Shortcode: "new",
				Body:      "New",
				URL:       "mxc://example.org/new",
			})
			if err == nil {
				t.Fatal("expected deterministic save failure")
			}
			for _, want := range []string{test.wantCommitted, test.wantPartial} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err, want)
				}
			}

			fake.mu.Lock()
			gotCalls := append([]rawPackStateKey(nil), fake.saveCalls...)
			fake.mu.Unlock()
			if len(gotCalls) != test.failureAt {
				t.Fatalf("save calls = %#v, want first %d ordered calls", gotCalls, test.failureAt)
			}
			for index := range gotCalls {
				if gotCalls[index] != wantCalls[index] {
					t.Errorf("save call %d = %#v, want %#v", index+1, gotCalls[index], wantCalls[index])
				}
			}
			if images := parsePackImages(fake.get(room, event.StateUnstableImagePack, stateKey)); images["legacy"].URL == "" {
				t.Fatalf("alias was cleared before all canonical writes and cleanup succeeded: %#v", images)
			}
		})
	}
}

func TestImagePackBlockedOperationsSerializeAtContestedLock(t *testing.T) {
	account := remoteapi.UserID("wxid_self")
	room := id.RoomID("!space:example.org")
	portal := id.RoomID("!portal:example.org")
	stateKey := wechatStickerPackStateKey(account)
	tests := []struct {
		name      string
		contender func(*Server) error
	}{
		{
			name: "manual add versus Msg47 shared add path",
			contender: func(s *Server) error {
				_, err := s.addStickerToAccountSpace(context.Background(), account, room, wechatStickerImage{Shortcode: "second", Body: "Second", URL: "mxc://example.org/second"})
				return err
			},
		},
		{
			name: "add versus remove",
			contender: func(s *Server) error {
				s.removeStickerPackFromRoom(context.Background(), account, room)
				return nil
			},
		},
		{
			name: "add versus repair",
			contender: func(s *Server) error {
				s.repairStickerPacksForAccount(context.Background(), account, room, "")
				return nil
			},
		},
		{
			name: "add versus mirror",
			contender: func(s *Server) error {
				s.mirrorStickerPackToElementPortal(context.Background(), account, room, portal)
				return nil
			},
		},
		{
			name: "status read versus mutation",
			contender: func(s *Server) error {
				s.stickerStatusText(context.Background(), account)
				return nil
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := newFakeRawImagePackState()
			fake.set(room, event.StateImagePack, stateKey, `{"pack":{"display_name":"Legacy"},"images":{}}`)
			fake.set(room, event.StateUnstableImagePack, "", `{"pack":{"display_name":"Legacy"},"images":{}}`)
			fake.set(portal, event.StateUnstableImagePack, "", `{"images":{}}`)
			firstLoadStarted := make(chan struct{})
			releaseFirstLoad := make(chan struct{})
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(releaseFirstLoad) }) }
			defer release()
			fake.beforeLoad = func(call int) {
				if call == 1 {
					close(firstLoadStarted)
					<-releaseFirstLoad
				}
			}
			lockArrivals := make(chan struct{}, 2)
			s := &Server{
				accounts:          &accountStore{data: accountStoreData{Accounts: map[string]savedAccount{string(account): {ID: account, SpaceRoom: room.String()}}}},
				portals:           &portalStore{data: portalState{Portals: map[string]portalRecord{"chat": {ChatID: "chat", RoomID: portal}}}},
				rawImagePackState: fake,
				beforePackLock:    func() { lockArrivals <- struct{}{} },
			}

			firstDone := make(chan error, 1)
			go func() {
				_, err := s.addStickerToAccountSpace(context.Background(), account, room, wechatStickerImage{Shortcode: "first", Body: "First", URL: "mxc://example.org/first"})
				firstDone <- err
			}()
			waitImagePackSignal(t, lockArrivals, "first mutation reaching packMu")
			waitImagePackSignal(t, firstLoadStarted, "first mutation blocking inside image-pack load")

			contenderDone := make(chan error, 1)
			go func() { contenderDone <- test.contender(s) }()
			waitImagePackSignal(t, lockArrivals, "contender reaching contested packMu")

			fake.mu.Lock()
			loads, saves := fake.loads, fake.saves
			fake.mu.Unlock()
			if loads != 1 || saves != 0 {
				release()
				t.Fatalf("contender reached image-pack storage before lock release: loads=%d saves=%d", loads, saves)
			}
			release()
			waitImagePackResult(t, firstDone, "first mutation")
			waitImagePackResult(t, contenderDone, "contender")

			if test.name == "manual add versus Msg47 shared add path" {
				images := parsePackImages(fake.get(room, event.StateImagePack, stateKey))
				if images["first"].URL == "" || images["second"].URL == "" {
					t.Fatalf("serialized shared add path lost an image: %#v", images)
				}
			}
		})
	}
}

func TestImagePackLockOrderSpaceThenPackAndPackDoesNotAcquireSpace(t *testing.T) {
	account := remoteapi.UserID("wxid_self")
	room := id.RoomID("!space:example.org")
	stateKey := wechatStickerPackStateKey(account)
	fake := newFakeRawImagePackState()
	fake.set(room, event.StateImagePack, stateKey, `{"images":{}}`)
	fake.set(room, event.StateUnstableImagePack, "", `{"images":{}}`)
	lockArrivals := make(chan struct{}, 2)
	s := &Server{rawImagePackState: fake, beforePackLock: func() { lockArrivals <- struct{}{} }}

	spaceThenPackDone := make(chan error, 1)
	go func() {
		s.spaceMu.Lock()
		_, err := s.addStickerToAccountSpace(context.Background(), account, room, wechatStickerImage{Shortcode: "ordered", URL: "mxc://example.org/ordered"})
		s.spaceMu.Unlock()
		spaceThenPackDone <- err
	}()
	waitImagePackSignal(t, lockArrivals, "spaceMu holder reaching packMu")
	waitImagePackResult(t, spaceThenPackDone, "spaceMu -> packMu operation")

	s.spaceMu.Lock()
	defer s.spaceMu.Unlock()
	packOnlyDone := make(chan error, 1)
	go func() {
		_, err := s.addStickerToAccountSpace(context.Background(), account, room, wechatStickerImage{Shortcode: "pack_only", URL: "mxc://example.org/pack-only"})
		packOnlyDone <- err
	}()
	waitImagePackSignal(t, lockArrivals, "pack-only operation reaching packMu while spaceMu is held")
	waitImagePackResult(t, packOnlyDone, "pack-only operation while spaceMu remains held")
}

func TestStickerRepairDeadlinesReleasePackLock(t *testing.T) {
	account := remoteapi.UserID("wxid_self")
	room := id.RoomID("!space:example.org")
	for _, test := range []struct {
		name      string
		operation func(*Server)
	}{
		{
			name: "full account repair",
			operation: func(s *Server) {
				s.repairStickerPacksForAccount(context.Background(), account, room, "")
			},
		},
		{
			name: "compatibility repair",
			operation: func(s *Server) {
				s.repairStickerPackCompatibility(context.Background(), account, room, "")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			started := make(chan struct{}, 1)
			release := make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			s := &Server{
				rawImagePackState:      &contextBlockingRawImagePackState{started: started, release: release},
				matrixOperationTimeout: 20 * time.Millisecond,
			}
			operationDone := make(chan struct{})
			go func() {
				test.operation(s)
				close(operationDone)
			}()
			waitImagePackSignal(t, started, "repair reaching blocked Matrix state operation")

			lockAcquired := make(chan struct{})
			go func() {
				s.packMu.Lock()
				close(lockAcquired)
				s.packMu.Unlock()
			}()
			select {
			case <-lockAcquired:
			case <-time.After(250 * time.Millisecond):
				unblock()
				<-operationDone
				t.Fatal("packMu remained held after the configured sticker repair deadline")
			}
			select {
			case <-operationDone:
			case <-time.After(250 * time.Millisecond):
				unblock()
				t.Fatal("sticker repair did not return after its Matrix operation deadline")
			}
		})
	}
}

type contextBlockingSaveImagePackState struct {
	delegate  *fakeRawImagePackState
	blockRoom id.RoomID
	started   chan<- struct{}
	release   <-chan struct{}
}

func (state *contextBlockingSaveImagePackState) Load(ctx context.Context, room id.RoomID, eventType event.Type, stateKey string) (json.RawMessage, error) {
	return state.delegate.Load(ctx, room, eventType, stateKey)
}

func (state *contextBlockingSaveImagePackState) Save(ctx context.Context, room id.RoomID, eventType event.Type, stateKey string, raw json.RawMessage) error {
	if room != state.blockRoom {
		return state.delegate.Save(ctx, room, eventType, stateKey, raw)
	}
	select {
	case state.started <- struct{}{}:
	default:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-state.release:
		return errors.New("released blocking save")
	}
}

func TestStickerCompatibilitySaveDeadlineReleasesPackLock(t *testing.T) {
	account := remoteapi.UserID("wxid_self")
	room := id.RoomID("!space:example.org")
	stateKey := wechatStickerPackStateKey(account)
	delegate := newFakeRawImagePackState()
	delegate.set(room, event.StateImagePack, stateKey, `{"images":{"one":{"url":"mxc://example.org/one"}}}`)
	delegate.set(room, event.StateUnstableImagePack, "", `{"images":{}}`)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	defer close(release)
	s := &Server{
		rawImagePackState:      &contextBlockingSaveImagePackState{delegate: delegate, blockRoom: room, started: started, release: release},
		matrixOperationTimeout: 20 * time.Millisecond,
	}
	done := make(chan struct{})
	go func() {
		s.repairStickerPackCompatibility(context.Background(), account, room, "")
		close(done)
	}()
	waitImagePackSignal(t, started, "compatibility repair reaching blocked state save")
	select {
	case <-done:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("compatibility repair state save ignored Matrix operation deadline")
	}
	lockAcquired := make(chan struct{})
	go func() {
		s.packMu.Lock()
		close(lockAcquired)
		s.packMu.Unlock()
	}()
	waitImagePackSignal(t, lockAcquired, "packMu release after compatibility state save deadline")
}

func TestStickerRepairJoinedRoomsDeadlineReleasesPackLock(t *testing.T) {
	account := remoteapi.UserID("wxid_self")
	room := id.RoomID("!space:example.org")
	stateKey := wechatStickerPackStateKey(account)
	state := newFakeRawImagePackState()
	state.set(room, event.StateImagePack, stateKey, `{"images":{"one":{"url":"mxc://example.org/one"}}}`)
	state.set(room, event.StateUnstableImagePack, "", `{"images":{}}`)
	started := make(chan struct{}, 1)
	homeserver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/joined_rooms") {
			started <- struct{}{}
			<-r.Context().Done()
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer homeserver.Close()
	s := &Server{
		as:                     testAppservice(t, homeserver.URL),
		rawImagePackState:      state,
		matrixOperationTimeout: 20 * time.Millisecond,
	}
	done := make(chan struct{})
	go func() {
		s.repairStickerPacksForAccount(context.Background(), account, room, "")
		close(done)
	}()
	waitImagePackSignal(t, started, "sticker repair reaching blocked joined-rooms request")
	select {
	case <-done:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("sticker repair JoinedRooms ignored Matrix operation deadline")
	}
	lockAcquired := make(chan struct{})
	go func() {
		s.packMu.Lock()
		close(lockAcquired)
		s.packMu.Unlock()
	}()
	waitImagePackSignal(t, lockAcquired, "packMu release after JoinedRooms deadline")
}

func TestStickerRepairMirrorSaveDeadlineReleasesPackLock(t *testing.T) {
	account := remoteapi.UserID("wxid_self")
	space := id.RoomID("!space:example.org")
	portal := id.RoomID("!portal:example.org")
	stateKey := wechatStickerPackStateKey(account)
	delegate := newFakeRawImagePackState()
	delegate.set(space, event.StateImagePack, stateKey, `{"images":{"one":{"url":"mxc://example.org/one"}}}`)
	delegate.set(space, event.StateUnstableImagePack, "", `{"images":{}}`)
	delegate.set(portal, event.StateUnstableImagePack, "", `{"images":{}}`)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	defer close(release)
	s := &Server{
		rawImagePackState:      &contextBlockingSaveImagePackState{delegate: delegate, blockRoom: portal, started: started, release: release},
		portals:                &portalStore{data: portalState{Portals: map[string]portalRecord{"chat": {ChatID: "chat", RoomID: portal}}}},
		matrixOperationTimeout: 20 * time.Millisecond,
	}
	done := make(chan struct{})
	go func() {
		s.repairStickerPacksForAccount(context.Background(), account, space, "")
		close(done)
	}()
	waitImagePackSignal(t, started, "sticker repair reaching blocked mirror save")
	select {
	case <-done:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("sticker repair mirror save ignored Matrix operation deadline")
	}
	lockAcquired := make(chan struct{})
	go func() {
		s.packMu.Lock()
		close(lockAcquired)
		s.packMu.Unlock()
	}()
	waitImagePackSignal(t, lockAcquired, "packMu release after mirror save deadline")
}

func waitImagePackSignal(t *testing.T, signal <-chan struct{}, operation string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatalf("%s deadlocked", operation)
	}
}

func waitImagePackResult(t *testing.T, result <-chan error, operation string) {
	t.Helper()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("%s failed: %v", operation, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("%s deadlocked", operation)
	}
}

func TestRawImagePackFieldsSurviveAddRepairAndMirror(t *testing.T) {
	fake := newFakeRawImagePackState()
	account := remoteapi.UserID("wxid_self")
	stateKey := wechatStickerPackStateKey(account)
	space := id.RoomID("!space:example.org")
	portal := id.RoomID("!portal:example.org")
	stable := `{
		"pack":{"display_name":"Legacy","example_metadata":{"keep":true}},
		"example_top":{"keep":true},
		"images":{"old":{"url":"mxc://example.org/old","body":"Old","example_image":{"keep":true}}}
	}`
	fake.set(space, event.StateImagePack, stateKey, stable)
	fake.set(space, event.StateUnstableImagePack, "", stable)
	fake.set(portal, event.StateUnstableImagePack, "", `{
		"pack":{"display_name":"Portal","portal_metadata":1},
		"portal_top":2,
		"images":{"old":{"url":"mxc://example.org/portal-old","portal_image":3}}
	}`)
	s := &Server{rawImagePackState: fake}

	if _, err := s.addStickerToAccountSpace(context.Background(), account, space, wechatStickerImage{Shortcode: "old", Body: "Updated", URL: "mxc://example.org/updated"}); err != nil {
		t.Fatalf("update existing sticker failed: %v", err)
	}
	if _, err := s.addStickerToAccountSpace(context.Background(), account, space, wechatStickerImage{Shortcode: "new", Body: "New", URL: "mxc://example.org/new"}); err != nil {
		t.Fatalf("add new sticker failed: %v", err)
	}
	assertRawPackPaths(t, fake.get(space, event.StateImagePack, stateKey), map[string]any{
		"example_top.keep":              true,
		"pack.example_metadata.keep":    true,
		"images.old.url":                "mxc://example.org/updated",
		"images.old.example_image.keep": true,
	})

	if count := s.repairStickerPacksForAccount(context.Background(), account, space, ""); count != 2 {
		t.Fatalf("repair count = %d, want 2", count)
	}
	assertRawPackPaths(t, fake.get(space, event.StateImagePack, stateKey), map[string]any{
		"example_top.keep":              true,
		"pack.example_metadata.keep":    true,
		"images.old.example_image.keep": true,
	})

	s.mirrorStickerPackToElementPortal(context.Background(), account, space, portal)
	assertRawPackPaths(t, fake.get(portal, event.StateUnstableImagePack, ""), map[string]any{
		"portal_top":                    float64(2),
		"pack.portal_metadata":          float64(1),
		"images.old.portal_image":       float64(3),
		"images.old.example_image.keep": true,
	})
	images := parsePackImages(fake.get(portal, event.StateUnstableImagePack, ""))
	if len(images) != 2 || images["new"].URL != "mxc://example.org/new" {
		t.Fatalf("mirror did not reconcile both source images: %#v", images)
	}
}

func TestMalformedImagePackMutationStartsFromWechatBase(t *testing.T) {
	fake := newFakeRawImagePackState()
	account := remoteapi.UserID("wxid_self")
	room := id.RoomID("!space:example.org")
	stateKey := wechatStickerPackStateKey(account)
	fake.set(room, event.StateImagePack, stateKey, `not-json`)
	fake.set(room, event.StateUnstableImagePack, "", `[]`)
	s := &Server{rawImagePackState: fake}

	if _, err := s.addStickerToAccountSpace(context.Background(), account, room, wechatStickerImage{Shortcode: "new", Body: "New", URL: "mxc://example.org/new"}); err != nil {
		t.Fatalf("addStickerToAccountSpace failed: %v", err)
	}
	assertRawPackPaths(t, fake.get(room, event.StateImagePack, stateKey), map[string]any{
		"pack.display_name": "WeChat wxid_self stickers",
	})
	assertCompatibilityPackHasNoMetadata(t, fake.get(room, event.StateUnstableImagePack, ""))
}

func assertCompatibilityPackHasNoMetadata(t *testing.T, raw json.RawMessage) {
	t.Helper()
	var content map[string]json.RawMessage
	if err := json.Unmarshal(raw, &content); err != nil {
		t.Fatalf("decode compatibility pack: %v", err)
	}
	if _, exists := content["pack"]; exists {
		t.Fatalf("compatibility pack unexpectedly contains metadata: %s", raw)
	}
	if _, exists := content["images"]; !exists {
		t.Fatalf("compatibility pack is missing images: %s", raw)
	}
}

func TestCompositeStickerPackOperationsDoNotDeadlock(t *testing.T) {
	account := remoteapi.UserID("wxid_self")
	stateKey := wechatStickerPackStateKey(account)
	space := id.RoomID("!space:example.org")
	portal := id.RoomID("!portal:example.org")
	newServer := func() *Server {
		fake := newFakeRawImagePackState()
		fake.set(space, event.StateImagePack, stateKey, `{"images":{"old":{"url":"mxc://example.org/old"}}}`)
		fake.set(space, event.StateUnstableImagePack, "", `{"images":{"old":{"url":"mxc://example.org/old"}}}`)
		fake.set(portal, event.StateUnstableImagePack, "", `{"images":{}}`)
		return &Server{
			rawImagePackState: fake,
			portals: &portalStore{data: portalState{Portals: map[string]portalRecord{
				"chat": {ChatID: "chat", RoomID: portal},
			}}},
		}
	}

	tests := map[string]func(*Server){
		"sync portals": func(s *Server) {
			s.syncStickerToElementPortals(context.Background(), account, wechatStickerImage{Shortcode: "new", Body: "New", URL: "mxc://example.org/new"})
		},
		"repair and mirror": func(s *Server) {
			s.repairStickerPacksForAccount(context.Background(), account, space, "")
		},
		"compatibility repair": func(s *Server) {
			s.repairStickerPackCompatibility(context.Background(), account, space, "")
		},
	}
	for name, operation := range tests {
		t.Run(name, func(t *testing.T) {
			done := make(chan struct{})
			go func() {
				operation(newServer())
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("composite pack operation deadlocked")
			}
		})
	}
}

func assertRawPackPaths(t *testing.T, raw json.RawMessage, paths map[string]any) {
	t.Helper()
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatalf("invalid raw pack JSON %q: %v", raw, err)
	}
	for path, want := range paths {
		var got any = root
		for _, part := range splitPath(path) {
			object, ok := got.(map[string]any)
			if !ok {
				t.Fatalf("path %s traversed non-object %#v in %s", path, got, raw)
			}
			got, ok = object[part]
			if !ok {
				t.Fatalf("path %s missing part %s in %s", path, part, raw)
			}
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("path %s = %#v, want %#v in %s", path, got, want, raw)
		}
	}
}

func splitPath(path string) []string {
	var parts []string
	for path != "" {
		index := -1
		for i, char := range path {
			if char == '.' {
				index = i
				break
			}
		}
		if index < 0 {
			return append(parts, path)
		}
		parts = append(parts, path[:index])
		path = path[index+1:]
	}
	return parts
}
