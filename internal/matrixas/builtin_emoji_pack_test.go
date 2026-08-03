package matrixas

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leat/mautrix-go-wechat/internal/config"
	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

func TestCloneImagePackRoomsPreservesNilPreimage(t *testing.T) {
	if cloned := cloneImagePackRooms(event.ImagePackRoomsEventContent{}); cloned.Rooms != nil {
		t.Fatalf("nil Rooms preimage cloned as %#v", cloned.Rooms)
	}
}

func TestMergeReconciliationResultsPreservesSkippedWarnings(t *testing.T) {
	first := reconciliationResult{Status: builtinPackStatusSuccess, AttemptedRooms: 1, ReconciledRooms: 1, Warnings: []string{"prepared warning"}}
	second := reconciliationResult{Status: builtinPackStatusSkipped, Warnings: []string{"global warning"}}

	merged := mergeReconciliationResults(first, second)

	if merged.Status != builtinPackStatusSuccess || !reflect.DeepEqual(merged.Warnings, []string{"prepared warning", "global warning"}) {
		t.Fatalf("merged result = %#v, want success with both warnings", merged)
	}
}

func TestBuiltinEmojiPackMetadataAndRegistryOrderAreStable(t *testing.T) {
	data := builtinEmojiTestData(
		builtinEmojiRecord{Shortcode: "wechat_grimace", MXC: "mxc://example.org/grimace"},
		builtinEmojiRecord{Shortcode: "wechat_smile", MXC: "mxc://example.org/smile"},
	)

	got := string(buildBuiltinEmojiPack(data))
	want := `{"images":{"wechat_smile":{"url":"mxc://example.org/smile"},"wechat_grimace":{"url":"mxc://example.org/grimace"}},"pack":{"display_name":"WeChat built-in emoji","usage":["sticker","emoticon"],"attribution":"Source repository: https://github.com/airinghost/wechat-emoji\nPinned commit: 2896eb55348041f5c5a66e4eccfb4882ab9cda0c\nSource path: web.wechat.com/compressed-cwebp/lossless-z9\nNo license found in the source repository.","fi.mau.bridged_pack":{"network":"wechat","url":"https://github.com/airinghost/wechat-emoji/tree/2896eb55348041f5c5a66e4eccfb4882ab9cda0c"}}}`
	if got != want {
		t.Fatalf("buildBuiltinEmojiPack() =\n%s\nwant\n%s", got, want)
	}
}

func TestBuiltinEmojiPackReconcilesEveryRoomPreservingPartialStoreAndUnrelatedContent(t *testing.T) {
	data := builtinEmojiTestData(
		builtinEmojiRecord{Shortcode: "wechat_smile", MXC: "mxc://example.org/new-smile"},
	)
	fake := newFakeRawImagePackState()
	rooms := []id.RoomID{
		"!a-space:example.org",
		"!b-portal:example.org",
		"!m-portal:example.org",
		"!z-space:example.org",
	}
	for _, room := range rooms {
		fake.set(room, event.StateImagePack, builtinEmojiStateKey, `{"images":{"party":{"url":"mxc://example.org/party","org.example.image":true},"wechat_smile":{"url":"mxc://example.org/old-smile","old":true},"wechat_grimace":{"url":"mxc://example.org/stale"}},"pack":{"display_name":"operator edit"},"org.example.top":true}`)
		fake.set(room, event.StateUnstableImagePack, "", `{"images":{"party":{"url":"mxc://example.org/party","org.example.image":true},"wechat_smile":{"url":"mxc://example.org/old-smile","old":true},"wechat_grimace":{"url":"mxc://example.org/stale"}},"pack":{"display_name":"shared metadata","org.example.pack":true},"org.example.top":{"keep":true}}`)
	}
	s := builtinEmojiPackTestServer(data, fake)
	s.accounts.data.Accounts = map[string]savedAccount{
		"z":     {ID: "z", SpaceRoom: rooms[3].String()},
		"a":     {ID: "a", SpaceRoom: rooms[0].String()},
		"empty": {ID: "empty"},
	}
	s.portals.data.Portals = map[string]portalRecord{
		"m": {ChatID: "m", RoomID: rooms[2]},
		"b": {ChatID: "b", RoomID: rooms[1]},
	}

	s.packMu.Lock()
	result := s.reconcileBuiltinEmojiPacksLocked(context.Background())
	s.packMu.Unlock()
	if result.Status != builtinPackStatusSuccess || result.ReconciledRooms != len(rooms) || result.FailedRooms != 0 {
		t.Fatalf("reconciliation result = %#v", result)
	}

	fake.mu.Lock()
	calls := append([]rawPackStateKey(nil), fake.saveCalls...)
	fake.mu.Unlock()
	var wantCalls []rawPackStateKey
	for _, room := range rooms {
		wantCalls = append(wantCalls,
			rawPackStateKey{room: room, eventType: event.StateImagePack.Type, stateKey: builtinEmojiStateKey},
			rawPackStateKey{room: room, eventType: event.StateUnstableImagePack.Type, stateKey: ""},
		)
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("save order = %#v, want %#v", calls, wantCalls)
	}

	for _, room := range rooms {
		stable := rawJSONObjectForTest(t, fake.get(room, event.StateImagePack, builtinEmojiStateKey))
		stableImages := rawJSONObjectForTest(t, stable["images"])
		assertJSONPathEqual(t, stableImages["party"], map[string]any{"url": "mxc://example.org/party", "org.example.image": true})
		assertJSONPathEqual(t, stableImages["wechat_smile"], map[string]any{"url": "mxc://example.org/new-smile"})
		assertJSONPathEqual(t, stableImages["wechat_grimace"], map[string]any{"url": "mxc://example.org/stale"})
		assertJSONPathEqual(t, stable["pack"], map[string]any{
			"display_name":        "WeChat built-in emoji",
			"usage":               []any{"sticker", "emoticon"},
			"attribution":         "Source repository: https://github.com/airinghost/wechat-emoji\nPinned commit: 2896eb55348041f5c5a66e4eccfb4882ab9cda0c\nSource path: web.wechat.com/compressed-cwebp/lossless-z9\nNo license found in the source repository.",
			"fi.mau.bridged_pack": map[string]any{"network": "wechat", "url": "https://github.com/airinghost/wechat-emoji/tree/2896eb55348041f5c5a66e4eccfb4882ab9cda0c"},
		})

		compat := rawJSONObjectForTest(t, fake.get(room, event.StateUnstableImagePack, ""))
		compatImages := rawJSONObjectForTest(t, compat["images"])
		assertJSONPathEqual(t, compatImages["party"], map[string]any{"url": "mxc://example.org/party", "org.example.image": true})
		assertJSONPathEqual(t, compatImages["wechat_smile"], map[string]any{"url": "mxc://example.org/new-smile", "old": true})
		assertJSONPathEqual(t, compatImages["wechat_grimace"], map[string]any{"url": "mxc://example.org/stale"})
		assertJSONPathEqual(t, compat["pack"], map[string]any{"display_name": "shared metadata", "org.example.pack": true})
		assertJSONPathEqual(t, compat["org.example.top"], map[string]any{"keep": true})
	}
}

func TestBuiltinEmojiPackInvalidOrEmptyStoreDoesNotReadOrWriteRooms(t *testing.T) {
	tests := []struct {
		name    string
		data    builtinEmojiStoreData
		loadErr error
	}{
		{name: "empty", data: builtinEmojiTestData()},
		{name: "load error", data: builtinEmojiTestData(), loadErr: errors.New("corrupt store")},
		{name: "invalid snapshot", data: withBuiltinEmojiSource(builtinEmojiTestData(builtinEmojiRecord{Shortcode: "wechat_smile", MXC: "mxc://example.org/smile"}), builtinEmojiSource{Repository: "https://example.org/wrong", Commit: builtinEmojiSourceCommit, Path: builtinEmojiSourcePath})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := newFakeRawImagePackState()
			fake.set("!space:example.org", event.StateImagePack, builtinEmojiStateKey, `{"images":{"wechat_smile":{"url":"mxc://example.org/from-matrix"}}}`)
			s := builtinEmojiPackTestServer(test.data, fake)
			s.builtinEmojiStore.loadErr = test.loadErr
			s.accounts.data.Accounts["a"] = savedAccount{ID: "a", SpaceRoom: "!space:example.org"}

			s.packMu.Lock()
			result := s.reconcileBuiltinEmojiPacksLocked(context.Background())
			s.packMu.Unlock()
			if result.Status != builtinPackStatusSkipped {
				t.Fatalf("result = %#v, want skipped", result)
			}
			fake.mu.Lock()
			loads, saves := fake.loads, fake.saves
			fake.mu.Unlock()
			if loads != 0 || saves != 0 {
				t.Fatalf("invalid/empty store caused loads=%d saves=%d", loads, saves)
			}
		})
	}
}

type fakeBuiltinPackVisibilityClient struct {
	members           map[id.RoomID]event.Membership
	memberErrors      map[id.RoomID]error
	memberCalls       []id.RoomID
	accountData       map[string]event.ImagePackRoomsEventContent
	getErrors         map[string]error
	setErrors         map[string]error
	setErrorsAt       map[int]error
	applyThenErrorsAt map[int]error
	ignoreSetsAt      map[int]bool
	setCalls          []string
	beforeGet         func(string)
	beforeSet         func(int)
	factoryUser       id.UserID
	factoryToken      string
	factoryCalled     int
}

func (f *fakeBuiltinPackVisibilityClient) Membership(_ context.Context, room id.RoomID, user id.UserID) (event.Membership, error) {
	f.memberCalls = append(f.memberCalls, room)
	if err := f.memberErrors[room]; err != nil {
		return "", err
	}
	return f.members[room], nil
}

func (f *fakeBuiltinPackVisibilityClient) GetAccountData(_ context.Context, eventType string, content *event.ImagePackRoomsEventContent) error {
	if f.beforeGet != nil {
		f.beforeGet(eventType)
	}
	if err := f.getErrors[eventType]; err != nil {
		return err
	}
	*content = cloneImagePackRoomsForTest(f.accountData[eventType])
	return nil
}

func (f *fakeBuiltinPackVisibilityClient) SetAccountData(_ context.Context, eventType string, content *event.ImagePackRoomsEventContent) error {
	f.setCalls = append(f.setCalls, eventType)
	call := len(f.setCalls)
	if f.beforeSet != nil {
		f.beforeSet(call)
	}
	if err := f.setErrorsAt[call]; err != nil {
		return err
	}
	if err := f.setErrors[eventType]; err != nil {
		return err
	}
	if f.ignoreSetsAt[call] {
		return nil
	}
	f.accountData[eventType] = cloneImagePackRoomsForTest(*content)
	return f.applyThenErrorsAt[call]
}

func TestBuiltinEmojiAccountDataUsesPersonalTokenMembershipAndDeterministicSpacePreference(t *testing.T) {
	data := builtinEmojiTestData(builtinEmojiRecord{Shortcode: "wechat_smile", MXC: "mxc://example.org/smile"})
	fakeState := newFakeRawImagePackState()
	rooms := []id.RoomID{"!a-space:example.org", "!z-space:example.org", "!b-portal:example.org", "!m-portal:example.org"}
	for _, room := range rooms {
		seedBuiltinPackRoom(t, fakeState, room, data)
	}
	visibility := &fakeBuiltinPackVisibilityClient{
		members: map[id.RoomID]event.Membership{
			rooms[0]: event.MembershipInvite,
			rooms[1]: event.MembershipJoin,
			rooms[2]: event.MembershipJoin,
			rooms[3]: event.MembershipJoin,
		},
		memberErrors: map[id.RoomID]error{},
		accountData: map[string]event.ImagePackRoomsEventContent{
			event.AccountDataImagePackRooms.Type: {Rooms: map[id.RoomID]map[string]struct{}{
				"!old:example.org": {builtinEmojiStateKey: {}, "other": {}},
			}},
			event.AccountDataUnstableImagePackRooms.Type: {Rooms: map[id.RoomID]map[string]struct{}{
				"!legacy-existing:example.org": {"": {}},
				"!legacy-stale:example.org":    {builtinEmojiStateKey: {}, "other": {}},
			}},
		},
		getErrors: map[string]error{},
		setErrors: map[string]error{},
	}
	s := builtinEmojiPackTestServer(data, fakeState)
	s.cfg = &config.Config{
		Homeserver: config.Homeserver{Address: "https://matrix.invalid"},
		Bridge: config.Bridge{
			PersonalStickerUserID:      "@personal:example.org",
			PersonalStickerAccessToken: "personal-token",
		},
	}
	s.accounts.data.Accounts = map[string]savedAccount{
		"z": {ID: "z", SpaceRoom: rooms[1].String()},
		"a": {ID: "a", SpaceRoom: rooms[0].String()},
	}
	s.portals.data.Portals = map[string]portalRecord{
		"m": {ChatID: "m", RoomID: rooms[3]},
		"b": {ChatID: "b", RoomID: rooms[2]},
	}
	s.newBuiltinPackVisibilityClient = func(user id.UserID, token string) (builtinPackVisibilityClient, error) {
		visibility.factoryCalled++
		visibility.factoryUser = user
		visibility.factoryToken = token
		return visibility, nil
	}

	s.packMu.Lock()
	result := s.registerBuiltinPackAccountDataLocked(context.Background())
	s.packMu.Unlock()
	if result.Status != builtinPackStatusSuccess || result.Room != rooms[1] || result.Writes != 2 {
		t.Fatalf("visibility result = %#v", result)
	}
	if visibility.factoryCalled != 1 || visibility.factoryUser != "@personal:example.org" || visibility.factoryToken != "personal-token" {
		t.Fatalf("visibility factory calls=%d user=%q token=%q", visibility.factoryCalled, visibility.factoryUser, visibility.factoryToken)
	}
	if !reflect.DeepEqual(visibility.memberCalls, []id.RoomID{rooms[0], rooms[1]}) {
		t.Fatalf("membership order = %#v", visibility.memberCalls)
	}
	if !reflect.DeepEqual(visibility.setCalls, []string{event.AccountDataImagePackRooms.Type, event.AccountDataUnstableImagePackRooms.Type}) {
		t.Fatalf("account-data write order = %#v", visibility.setCalls)
	}

	stable := visibility.accountData[event.AccountDataImagePackRooms.Type]
	if _, ok := stable.Rooms[rooms[1]][builtinEmojiStateKey]; !ok {
		t.Fatalf("stable account data missing selected source: %#v", stable.Rooms)
	}
	if _, ok := stable.Rooms["!old:example.org"][builtinEmojiStateKey]; ok {
		t.Fatalf("stable account data retained stale built-in source: %#v", stable.Rooms)
	}
	if _, ok := stable.Rooms["!old:example.org"]["other"]; !ok {
		t.Fatalf("stable account data removed unrelated source: %#v", stable.Rooms)
	}

	unstable := visibility.accountData[event.AccountDataUnstableImagePackRooms.Type]
	if _, ok := unstable.Rooms[rooms[1]][""]; !ok {
		t.Fatalf("unstable account data missing selected empty-key source: %#v", unstable.Rooms)
	}
	if _, ok := unstable.Rooms["!legacy-existing:example.org"][""]; !ok {
		t.Fatalf("unstable account data removed unrelated empty-key source: %#v", unstable.Rooms)
	}
	if _, ok := unstable.Rooms["!legacy-stale:example.org"][builtinEmojiStateKey]; ok {
		t.Fatalf("unstable account data retained stale stable-key alias: %#v", unstable.Rooms)
	}
	if _, ok := unstable.Rooms["!legacy-stale:example.org"]["other"]; !ok {
		t.Fatalf("unstable account data removed unrelated source: %#v", unstable.Rooms)
	}
}

func TestBuiltinEmojiAccountDataPrefersPreparedExtraSpace(t *testing.T) {
	data := builtinEmojiTestData(builtinEmojiRecord{Shortcode: "wechat_smile", MXC: "mxc://example.org/smile"})
	oldSpace := id.RoomID("!a-old-space:example.org")
	preparedSpace := id.RoomID("!z-prepared-space:example.org")
	fakeState := newFakeRawImagePackState()
	seedBuiltinPackRoom(t, fakeState, oldSpace, data)
	seedBuiltinPackRoom(t, fakeState, preparedSpace, data)
	visibility := &fakeBuiltinPackVisibilityClient{
		members: map[id.RoomID]event.Membership{
			oldSpace:      event.MembershipJoin,
			preparedSpace: event.MembershipJoin,
		},
		memberErrors: map[id.RoomID]error{}, accountData: map[string]event.ImagePackRoomsEventContent{},
		getErrors: map[string]error{}, setErrors: map[string]error{},
	}
	s := builtinEmojiPackTestServer(data, fakeState)
	s.cfg = &config.Config{Bridge: config.Bridge{PersonalStickerUserID: "@personal:example.org", PersonalStickerAccessToken: "token"}}
	s.accounts.data.Accounts["a"] = savedAccount{ID: "a", SpaceRoom: oldSpace.String()}
	s.newBuiltinPackVisibilityClient = func(id.UserID, string) (builtinPackVisibilityClient, error) { return visibility, nil }

	s.packMu.Lock()
	result := s.registerBuiltinPackAccountDataForSpacesLocked(context.Background(), []id.RoomID{preparedSpace})
	s.packMu.Unlock()
	if result.Room != preparedSpace {
		t.Fatalf("selected account-data source = %s, want prepared Space %s", result.Room, preparedSpace)
	}
	if !reflect.DeepEqual(visibility.memberCalls, []id.RoomID{preparedSpace}) {
		t.Fatalf("membership candidates = %#v, want prepared Space first", visibility.memberCalls)
	}
}

func TestBuiltinEmojiAccountDataRequiresBothPackStatesBeforeMembership(t *testing.T) {
	data := builtinEmojiTestData(builtinEmojiRecord{Shortcode: "wechat_smile", MXC: "mxc://example.org/smile"})
	fakeState := newFakeRawImagePackState()
	missingCompat := id.RoomID("!a-space:example.org")
	joined := id.RoomID("!b-portal:example.org")
	fakeState.set(missingCompat, event.StateImagePack, builtinEmojiStateKey, string(buildBuiltinEmojiPack(data)))
	seedBuiltinPackRoom(t, fakeState, joined, data)
	visibility := &fakeBuiltinPackVisibilityClient{
		members: map[id.RoomID]event.Membership{joined: event.MembershipJoin}, memberErrors: map[id.RoomID]error{},
		accountData: map[string]event.ImagePackRoomsEventContent{}, getErrors: map[string]error{}, setErrors: map[string]error{},
	}
	s := builtinEmojiPackTestServer(data, fakeState)
	s.cfg = &config.Config{Bridge: config.Bridge{PersonalStickerUserID: "@personal:example.org", PersonalStickerAccessToken: "token"}}
	s.accounts.data.Accounts["a"] = savedAccount{ID: "a", SpaceRoom: missingCompat.String()}
	s.portals.data.Portals["b"] = portalRecord{ChatID: "b", RoomID: joined}
	s.newBuiltinPackVisibilityClient = func(id.UserID, string) (builtinPackVisibilityClient, error) { return visibility, nil }

	s.packMu.Lock()
	result := s.registerBuiltinPackAccountDataLocked(context.Background())
	s.packMu.Unlock()
	if result.Status != builtinPackStatusSuccess || result.Room != joined {
		t.Fatalf("visibility result = %#v", result)
	}
	if !reflect.DeepEqual(visibility.memberCalls, []id.RoomID{joined}) {
		t.Fatalf("membership was queried for a room without both packs: %#v", visibility.memberCalls)
	}
}

type contextBlockingRawImagePackState struct {
	started chan struct{}
	release <-chan struct{}
}

func (f *contextBlockingRawImagePackState) Load(ctx context.Context, _ id.RoomID, _ event.Type, _ string) (json.RawMessage, error) {
	select {
	case f.started <- struct{}{}:
	default:
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-f.release:
		return nil, errors.New("released blocking load")
	}
}

func (f *contextBlockingRawImagePackState) Save(ctx context.Context, _ id.RoomID, _ event.Type, _ string, _ json.RawMessage) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-f.release:
		return errors.New("released blocking save")
	}
}

func TestBuiltinEmojiRoomDeadlineReleasesPackLock(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	s := builtinEmojiPackTestServer(
		builtinEmojiTestData(builtinEmojiRecord{Shortcode: "wechat_smile", MXC: "mxc://example.org/smile"}),
		newFakeRawImagePackState(),
	)
	s.rawImagePackState = &contextBlockingRawImagePackState{started: started, release: release}
	s.accounts.data.Accounts["a"] = savedAccount{ID: "a", SpaceRoom: "!space:example.org"}
	s.matrixOperationTimeout = 20 * time.Millisecond

	done := make(chan reconciliationResult, 1)
	go func() {
		reconciliation, _ := s.reconcileBuiltinEmojiPacks(context.Background())
		done <- reconciliation
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("room reconciliation did not reach deterministic barrier")
	}

	lockAcquired := make(chan struct{})
	go func() {
		s.packMu.Lock()
		close(lockAcquired)
		s.packMu.Unlock()
	}()
	select {
	case <-lockAcquired:
	case <-time.After(250 * time.Millisecond):
		close(release)
		<-done
		t.Fatal("packMu remained held after the configured room reconciliation deadline")
	}
	close(release)
	result := <-done
	if result.Status != builtinPackStatusFailed || !strings.Contains(strings.Join(result.Warnings, "; "), context.DeadlineExceeded.Error()) {
		t.Fatalf("deadline reconciliation result = %#v", result)
	}
}

type contextBlockingVisibilityClient struct {
	operation string
	started   chan struct{}
	release   <-chan struct{}
}

func (f *contextBlockingVisibilityClient) block(ctx context.Context, operation string) error {
	if f.operation != operation {
		return nil
	}
	select {
	case f.started <- struct{}{}:
	default:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-f.release:
		return errors.New("released blocking " + operation)
	}
}

func (f *contextBlockingVisibilityClient) Membership(ctx context.Context, _ id.RoomID, _ id.UserID) (event.Membership, error) {
	if err := f.block(ctx, "membership"); err != nil {
		return "", err
	}
	return event.MembershipJoin, nil
}

func (f *contextBlockingVisibilityClient) GetAccountData(ctx context.Context, _ string, content *event.ImagePackRoomsEventContent) error {
	if err := f.block(ctx, "get"); err != nil {
		return err
	}
	*content = event.ImagePackRoomsEventContent{}
	return nil
}

func (f *contextBlockingVisibilityClient) SetAccountData(ctx context.Context, _ string, _ *event.ImagePackRoomsEventContent) error {
	return f.block(ctx, "set")
}

func TestBuiltinEmojiVisibilityOperationDeadlinesReleasePackLock(t *testing.T) {
	for _, operation := range []string{"membership", "get", "set"} {
		t.Run(operation, func(t *testing.T) {
			data := builtinEmojiTestData(builtinEmojiRecord{Shortcode: "wechat_smile", MXC: "mxc://example.org/smile"})
			room := id.RoomID("!space:example.org")
			fakeState := newFakeRawImagePackState()
			seedBuiltinPackRoom(t, fakeState, room, data)
			started := make(chan struct{}, 2)
			release := make(chan struct{})
			visibility := &contextBlockingVisibilityClient{operation: operation, started: started, release: release}
			s := builtinEmojiPackTestServer(data, fakeState)
			s.cfg = &config.Config{Bridge: config.Bridge{PersonalStickerUserID: "@personal:example.org", PersonalStickerAccessToken: "token"}}
			s.accounts.data.Accounts["a"] = savedAccount{ID: "a", SpaceRoom: room.String()}
			s.newBuiltinPackVisibilityClient = func(id.UserID, string) (builtinPackVisibilityClient, error) { return visibility, nil }
			s.matrixOperationTimeout = 20 * time.Millisecond

			done := make(chan accountDataVisibilityResult, 1)
			go func() {
				_, accountData := s.reconcileBuiltinEmojiPacks(context.Background())
				done <- accountData
			}()
			select {
			case <-started:
			case <-time.After(time.Second):
				close(release)
				t.Fatalf("%s operation did not reach deterministic barrier", operation)
			}

			lockAcquired := make(chan struct{})
			go func() {
				s.packMu.Lock()
				close(lockAcquired)
				s.packMu.Unlock()
			}()
			select {
			case <-lockAcquired:
			case <-time.After(300 * time.Millisecond):
				close(release)
				<-done
				t.Fatalf("packMu remained held after the configured %s deadline", operation)
			}
			close(release)
			result := <-done
			if !strings.Contains(strings.Join(result.Warnings, "; "), context.DeadlineExceeded.Error()) {
				t.Fatalf("%s deadline missing from account-data result: %#v", operation, result)
			}
		})
	}
}

func TestBuiltinAccountDataTransactionalAcquisitionReleasesLockOnPanic(t *testing.T) {
	data := builtinEmojiTestData(builtinEmojiRecord{Shortcode: "wechat_smile", MXC: "mxc://example.org/smile"})
	room := id.RoomID("!space:example.org")
	state := newFakeRawImagePackState()
	seedBuiltinPackRoom(t, state, room, data)
	s := builtinEmojiPackTestServer(data, state)
	s.cfg.Bridge.PersonalStickerUserID = "@personal:example.org"
	s.cfg.Bridge.PersonalStickerAccessToken = "token"
	visibility := &fakeBuiltinPackVisibilityClient{
		members:      map[id.RoomID]event.Membership{room: event.MembershipJoin},
		memberErrors: map[id.RoomID]error{},
		accountData:  map[string]event.ImagePackRoomsEventContent{},
		getErrors:    map[string]error{},
		setErrors:    map[string]error{},
		beforeGet: func(string) {
			panic("account-data read panic")
		},
	}
	s.newBuiltinPackVisibilityClient = func(id.UserID, string) (builtinPackVisibilityClient, error) { return visibility, nil }

	didPanic := false
	s.packMu.Lock()
	func() {
		defer func() { didPanic = recover() != nil }()
		_, _ = s.registerBuiltinPackAccountDataForSpacesTransactionalLocked(context.Background(), []id.RoomID{room})
	}()
	s.packMu.Unlock()
	if !didPanic {
		t.Fatal("transactional account-data acquisition did not propagate panic")
	}
	assertMutexAvailable(t, &s.accountDataMu, "accountDataMu after transactional acquisition panic")
}

func TestBuiltinAccountDataCallerReleasesLockWhenPersistencePanics(t *testing.T) {
	data := builtinEmojiTestData(builtinEmojiRecord{Shortcode: "wechat_smile", MXC: "mxc://example.org/smile"})
	room := id.RoomID("!space:example.org")
	s := builtinEmojiPackTestServer(data, newFakeRawImagePackState())
	s.cfg.Bridge.PersonalStickerUserID = "@personal:example.org"
	s.cfg.Bridge.PersonalStickerAccessToken = "token"
	visibility := &fakeBuiltinPackVisibilityClient{
		members:      map[id.RoomID]event.Membership{room: event.MembershipJoin},
		memberErrors: map[id.RoomID]error{},
		accountData:  map[string]event.ImagePackRoomsEventContent{},
		getErrors:    map[string]error{},
		setErrors:    map[string]error{},
	}
	s.newBuiltinPackVisibilityClient = func(id.UserID, string) (builtinPackVisibilityClient, error) { return visibility, nil }

	didPanic := false
	func() {
		defer func() { didPanic = recover() != nil }()
		_, _, _ = s.bootstrapNewAccountSpaceLocked(context.Background(), room, false, func() error {
			panic("account persistence panic")
		})
	}()
	if !didPanic {
		t.Fatal("account Space persistence did not propagate panic")
	}
	assertMutexAvailable(t, &s.accountDataMu, "accountDataMu after caller panic")
	assertMutexAvailable(t, &s.packMu, "packMu after caller panic")
}

func assertMutexAvailable(t *testing.T, mutex *sync.Mutex, name string) {
	t.Helper()
	if !mutex.TryLock() {
		t.Fatalf("%s remained locked", name)
	}
	mutex.Unlock()
}

func TestBuiltinAccountDataRMWWaitsForManualTransaction(t *testing.T) {
	accounts := newAccountStore(filepath.Join(t.TempDir(), "accounts.json"))
	space := id.RoomID("!space:example.org")
	if err := accounts.put(savedAccount{ID: "a", SpaceRoom: space.String()}); err != nil {
		t.Fatal(err)
	}
	s, matrix := newSpacePersistenceTestServerWithRecorder(t, accounts)
	data := builtinEmojiTestData(builtinEmojiRecord{Shortcode: "wechat_smile", MXC: "mxc://example.org/smile"})
	state := newFakeRawImagePackState()
	seedBuiltinPackRoom(t, state, space, data)
	s.rawImagePackState = state
	s.builtinEmojiStore = &builtinEmojiStore{data: data}
	s.cfg.Bridge.PersonalStickerUserID = "@personal:example.org"
	s.cfg.Bridge.PersonalStickerAccessToken = "token"
	manualGetStarted := make(chan struct{}, 1)
	releaseManualGet := make(chan struct{})
	matrix.accountDataGetStarted = manualGetStarted
	matrix.accountDataGetRelease = releaseManualGet
	builtinGetStarted := make(chan struct{}, 1)
	visibility := &fakeBuiltinPackVisibilityClient{
		members: map[id.RoomID]event.Membership{space: event.MembershipJoin}, memberErrors: map[id.RoomID]error{},
		accountData: map[string]event.ImagePackRoomsEventContent{}, getErrors: map[string]error{}, setErrors: map[string]error{},
		beforeGet: func(string) {
			select {
			case builtinGetStarted <- struct{}{}:
			default:
			}
		},
	}
	s.newBuiltinPackVisibilityClient = func(id.UserID, string) (builtinPackVisibilityClient, error) { return visibility, nil }

	manualDone := make(chan struct{})
	go func() {
		s.ensureUserSeesImagePackRoom(context.Background(), "@personal:example.org", space, wechatStickerPackStateKey("a"))
		close(manualDone)
	}()
	select {
	case <-manualGetStarted:
	case <-time.After(time.Second):
		close(releaseManualGet)
		t.Fatal("manual account-data RMW did not reach blocked read")
	}
	builtinDone := make(chan struct{})
	go func() {
		s.lockImagePack()
		s.registerBuiltinPackAccountDataLocked(context.Background())
		s.packMu.Unlock()
		close(builtinDone)
	}()
	select {
	case <-builtinGetStarted:
		close(releaseManualGet)
		t.Fatal("built-in account-data RMW interleaved with blocked manual transaction")
	case <-time.After(75 * time.Millisecond):
	}
	close(releaseManualGet)
	for name, done := range map[string]<-chan struct{}{"manual": manualDone, "built-in": builtinDone} {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatalf("%s account-data transaction did not finish", name)
		}
	}
}

func TestManualAccountDataRMWWaitsUntilBuiltinRollbackFinishes(t *testing.T) {
	accounts, _ := seededAccountStore(t)
	injected := errors.New("persist replacement failed")
	accounts.persist = func(string, []byte, fs.FileMode) error { return injected }
	s, matrix := newSpacePersistenceTestServerWithRecorder(t, accounts)
	data := builtinEmojiTestData(builtinEmojiRecord{Shortcode: "wechat_smile", MXC: "mxc://example.org/smile"})
	s.rawImagePackState = newFakeRawImagePackState()
	s.builtinEmojiStore = &builtinEmojiStore{data: data}
	s.cfg.Bridge.PersonalStickerUserID = "@personal:example.org"
	s.cfg.Bridge.PersonalStickerAccessToken = "token"
	rollbackStarted := make(chan struct{})
	releaseRollback := make(chan struct{})
	var rollbackOnce sync.Once
	visibility := &fakeBuiltinPackVisibilityClient{
		members: map[id.RoomID]event.Membership{"!created-1:example.com": event.MembershipJoin}, memberErrors: map[id.RoomID]error{},
		accountData: map[string]event.ImagePackRoomsEventContent{}, getErrors: map[string]error{}, setErrors: map[string]error{},
		beforeSet: func(call int) {
			if call == 3 {
				rollbackOnce.Do(func() { close(rollbackStarted) })
				<-releaseRollback
			}
		},
	}
	s.newBuiltinPackVisibilityClient = func(id.UserID, string) (builtinPackVisibilityClient, error) { return visibility, nil }
	rebuildDone := make(chan error, 1)
	go func() {
		_, err := s.rebuildAccountSpace(context.Background(), "wxid_old", "")
		rebuildDone <- err
	}()
	select {
	case <-rollbackStarted:
	case <-time.After(time.Second):
		close(releaseRollback)
		t.Fatal("rebuild did not reach blocked account-data rollback")
	}

	manualGetStarted := make(chan struct{}, 1)
	matrix.accountDataGetStarted = manualGetStarted
	manualDone := make(chan struct{})
	go func() {
		s.ensureUserSeesImagePackRoom(context.Background(), "@personal:example.org", "!manual:example.org", wechatStickerPackStateKey("wxid_old"))
		close(manualDone)
	}()
	select {
	case <-manualGetStarted:
		close(releaseRollback)
		t.Fatal("manual account-data RMW interleaved with built-in rollback")
	case <-time.After(75 * time.Millisecond):
	}
	close(releaseRollback)
	if err := <-rebuildDone; !errors.Is(err, injected) {
		t.Fatalf("rebuild error = %v, want persistence failure", err)
	}
	select {
	case <-manualGetStarted:
	case <-time.After(time.Second):
		t.Fatal("manual account-data RMW did not resume after rollback")
	}
	select {
	case <-manualDone:
	case <-time.After(time.Second):
		t.Fatal("manual account-data RMW did not finish after rollback")
	}
}

func TestBuiltinEmojiAccountDataReportsRoomAndTypeLoadErrors(t *testing.T) {
	data := builtinEmojiTestData(builtinEmojiRecord{Shortcode: "wechat_smile", MXC: "mxc://example.org/smile"})
	room := id.RoomID("!space:example.org")
	fakeState := newFakeRawImagePackState()
	fakeState.loadErrors = map[int]error{1: context.DeadlineExceeded}
	visibility := &fakeBuiltinPackVisibilityClient{
		members: map[id.RoomID]event.Membership{}, memberErrors: map[id.RoomID]error{},
		accountData: map[string]event.ImagePackRoomsEventContent{}, getErrors: map[string]error{}, setErrors: map[string]error{},
	}
	s := builtinEmojiPackTestServer(data, fakeState)
	s.cfg = &config.Config{Bridge: config.Bridge{PersonalStickerUserID: "@personal:example.org", PersonalStickerAccessToken: "token"}}
	s.accounts.data.Accounts["a"] = savedAccount{ID: "a", SpaceRoom: room.String()}
	s.newBuiltinPackVisibilityClient = func(id.UserID, string) (builtinPackVisibilityClient, error) { return visibility, nil }

	s.packMu.Lock()
	result := s.registerBuiltinPackAccountDataLocked(context.Background())
	s.packMu.Unlock()
	warnings := strings.Join(result.Warnings, "; ")
	if !strings.Contains(warnings, room.String()) || !strings.Contains(warnings, event.StateImagePack.Type) || !strings.Contains(warnings, context.DeadlineExceeded.Error()) {
		t.Fatalf("registration warnings = %q, want room/type load error", warnings)
	}
	if len(visibility.memberCalls) != 0 {
		t.Fatalf("membership queried after pack load error: %#v", visibility.memberCalls)
	}
}

func TestBuiltinEmojiAccountDataMissingCredentialsJoinAndWritesAreBestEffort(t *testing.T) {
	data := builtinEmojiTestData(builtinEmojiRecord{Shortcode: "wechat_smile", MXC: "mxc://example.org/smile"})
	room := id.RoomID("!space:example.org")
	tests := []struct {
		name       string
		user       string
		token      string
		membership event.Membership
		setError   error
		wantStatus builtinPackStatus
		wantWrites int
	}{
		{name: "no user", token: "token", membership: event.MembershipJoin, wantStatus: builtinPackStatusSkipped},
		{name: "no token", user: "@personal:example.org", membership: event.MembershipJoin, wantStatus: builtinPackStatusSkipped},
		{name: "invite", user: "@personal:example.org", token: "token", membership: event.MembershipInvite, wantStatus: builtinPackStatusSkipped},
		{name: "write failure", user: "@personal:example.org", token: "token", membership: event.MembershipJoin, setError: errors.New("write failed"), wantStatus: builtinPackStatusFailed, wantWrites: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fakeState := newFakeRawImagePackState()
			seedBuiltinPackRoom(t, fakeState, room, data)
			visibility := &fakeBuiltinPackVisibilityClient{
				members: map[id.RoomID]event.Membership{room: test.membership}, memberErrors: map[id.RoomID]error{},
				accountData: map[string]event.ImagePackRoomsEventContent{}, getErrors: map[string]error{},
				setErrors: map[string]error{event.AccountDataImagePackRooms.Type: test.setError},
			}
			s := builtinEmojiPackTestServer(data, fakeState)
			s.cfg = &config.Config{Bridge: config.Bridge{PersonalStickerUserID: test.user, PersonalStickerAccessToken: test.token}}
			s.accounts.data.Accounts["a"] = savedAccount{ID: "a", SpaceRoom: room.String()}
			factoryCalls := 0
			s.newBuiltinPackVisibilityClient = func(user id.UserID, token string) (builtinPackVisibilityClient, error) {
				factoryCalls++
				if user != id.UserID(test.user) || token != test.token {
					t.Fatalf("factory got user=%q token=%q", user, token)
				}
				return visibility, nil
			}

			s.packMu.Lock()
			result := s.registerBuiltinPackAccountDataLocked(context.Background())
			s.packMu.Unlock()
			if result.Status != test.wantStatus || result.Writes != test.wantWrites {
				t.Fatalf("result = %#v, want status=%s writes=%d", result, test.wantStatus, test.wantWrites)
			}
			if (test.user == "" || test.token == "") && factoryCalls != 0 {
				t.Fatalf("missing credentials invoked visibility factory %d times", factoryCalls)
			}
		})
	}
}

func TestBuiltinEmojiPackAccountDataFailureDoesNotChangeReconciliationSuccess(t *testing.T) {
	data := builtinEmojiTestData(builtinEmojiRecord{Shortcode: "wechat_smile", MXC: "mxc://example.org/smile"})
	room := id.RoomID("!space:example.org")
	fakeState := newFakeRawImagePackState()
	s := builtinEmojiPackTestServer(data, fakeState)
	s.cfg = &config.Config{Bridge: config.Bridge{PersonalStickerUserID: "@personal:example.org", PersonalStickerAccessToken: "token"}}
	s.accounts.data.Accounts["a"] = savedAccount{ID: "a", SpaceRoom: room.String()}
	visibility := &fakeBuiltinPackVisibilityClient{
		members: map[id.RoomID]event.Membership{room: event.MembershipJoin}, memberErrors: map[id.RoomID]error{},
		accountData: map[string]event.ImagePackRoomsEventContent{}, getErrors: map[string]error{},
		setErrors: map[string]error{event.AccountDataImagePackRooms.Type: errors.New("write failed")},
	}
	s.newBuiltinPackVisibilityClient = func(id.UserID, string) (builtinPackVisibilityClient, error) { return visibility, nil }

	reconciliation, accountData := s.reconcileBuiltinEmojiPacks(context.Background())
	if reconciliation.Status != builtinPackStatusSuccess || reconciliation.ReconciledRooms != 1 {
		t.Fatalf("reconciliation = %#v", reconciliation)
	}
	if accountData.Status != builtinPackStatusFailed {
		t.Fatalf("account data = %#v", accountData)
	}
}

func TestBuiltinEmojiSpaceStableFailurePreservesOldSpaceID(t *testing.T) {
	accounts, _ := seededAccountStore(t)
	s, _ := newSpacePersistenceTestServerWithRecorder(t, accounts)
	fakeState := newFakeRawImagePackState()
	injected := errors.New("stable built-in pack write failed")
	fakeState.saveErrors = map[int]error{1: injected}
	s.rawImagePackState = fakeState
	s.builtinEmojiStore = &builtinEmojiStore{data: builtinEmojiTestData(builtinEmojiRecord{Shortcode: "wechat_smile", MXC: "mxc://example.org/smile"})}

	before, _ := accounts.get("wxid_old")
	result, err := s.rebuildAccountSpace(context.Background(), "wxid_old", "")
	if err == nil || !errors.Is(err, injected) || !strings.Contains(err.Error(), "stable built-in pack write failed") {
		t.Fatalf("rebuild result=%#v error=%v", result, err)
	}
	after, _ := accounts.get("wxid_old")
	if after.SpaceRoom != before.SpaceRoom {
		t.Fatalf("stable failure switched Space ID from %s to %s", before.SpaceRoom, after.SpaceRoom)
	}
}

func TestBuiltinEmojiSpaceCompatibilityFailurePreservesOldSpaceID(t *testing.T) {
	accounts, _ := seededAccountStore(t)
	s, _ := newSpacePersistenceTestServerWithRecorder(t, accounts)
	fakeState := newFakeRawImagePackState()
	injected := errors.New("compatibility built-in pack write failed")
	fakeState.saveErrors = map[int]error{2: injected}
	s.rawImagePackState = fakeState
	s.builtinEmojiStore = &builtinEmojiStore{data: builtinEmojiTestData(builtinEmojiRecord{Shortcode: "wechat_smile", MXC: "mxc://example.org/smile"})}

	before, _ := accounts.get("wxid_old")
	result, err := s.rebuildAccountSpace(context.Background(), "wxid_old", "")
	if err == nil || !errors.Is(err, injected) {
		t.Fatalf("rebuild result=%#v error=%v", result, err)
	}
	after, _ := accounts.get("wxid_old")
	if after.SpaceRoom != before.SpaceRoom {
		t.Fatalf("compatibility failure switched Space ID from %s to %s", before.SpaceRoom, after.SpaceRoom)
	}
}

func TestBuiltinEmojiSpaceAccountDataFailurePermitsSwitch(t *testing.T) {
	accounts, _ := seededAccountStore(t)
	s, _ := newSpacePersistenceTestServerWithRecorder(t, accounts)
	fakeState := newFakeRawImagePackState()
	s.rawImagePackState = fakeState
	s.builtinEmojiStore = &builtinEmojiStore{data: builtinEmojiTestData(builtinEmojiRecord{Shortcode: "wechat_smile", MXC: "mxc://example.org/smile"})}
	s.cfg.Bridge.PersonalStickerUserID = "@personal:example.org"
	s.cfg.Bridge.PersonalStickerAccessToken = "token"
	oldAccount, _ := accounts.get("wxid_old")
	oldSpace := id.RoomID(oldAccount.SpaceRoom)
	visibility := &fakeBuiltinPackVisibilityClient{
		members: map[id.RoomID]event.Membership{"!created-1:example.com": event.MembershipJoin}, memberErrors: map[id.RoomID]error{},
		accountData: map[string]event.ImagePackRoomsEventContent{
			event.AccountDataImagePackRooms.Type:         {Rooms: map[id.RoomID]map[string]struct{}{oldSpace: {builtinEmojiStateKey: {}}}},
			event.AccountDataUnstableImagePackRooms.Type: {Rooms: map[id.RoomID]map[string]struct{}{oldSpace: {"": {}}}},
		},
		getErrors: map[string]error{},
		setErrors: map[string]error{event.AccountDataImagePackRooms.Type: errors.New("account data failed")},
	}
	s.newBuiltinPackVisibilityClient = func(id.UserID, string) (builtinPackVisibilityClient, error) { return visibility, nil }

	result, err := s.rebuildAccountSpace(context.Background(), "wxid_old", "")
	if err != nil {
		t.Fatalf("rebuild blocked on account data: %v", err)
	}
	if result.Space != "!created-1:example.com" || result.AccountData.Status != builtinPackStatusFailed || len(result.Warnings) == 0 {
		t.Fatalf("transition result = %#v", result)
	}
	after, _ := accounts.get("wxid_old")
	if after.SpaceRoom != result.Space.String() {
		t.Fatalf("account data failure did not publish new Space: %#v", after)
	}
	if !reflect.DeepEqual(after.RetiredSpaceRooms, []string{oldSpace.String()}) {
		t.Fatalf("account data failure lost pending retired Space: %#v", after.RetiredSpaceRooms)
	}
}

func TestBuiltinEmojiAccountDataRollsBackWhenSpacePersistenceFails(t *testing.T) {
	for _, test := range []struct {
		name         string
		transition   func(*Server) (spaceTransitionResult, error)
		wantSetCalls int
	}{
		{
			name:         "create",
			wantSetCalls: 4,
			transition: func(s *Server) (spaceTransitionResult, error) {
				return s.ensureAccountSpaceTransition(context.Background(), "wxid_other")
			},
		},
		{
			name:         "rebuild",
			wantSetCalls: 4,
			transition: func(s *Server) (spaceTransitionResult, error) {
				return s.rebuildAccountSpace(context.Background(), "wxid_old", "")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			accounts, _ := seededAccountStore(t)
			injected := errors.New("persist Space failed")
			accounts.persist = func(string, []byte, fs.FileMode) error { return injected }
			s, _ := newSpacePersistenceTestServerWithRecorder(t, accounts)
			data := builtinEmojiTestData(builtinEmojiRecord{Shortcode: "wechat_smile", MXC: "mxc://example.org/smile"})
			s.rawImagePackState = newFakeRawImagePackState()
			s.builtinEmojiStore = &builtinEmojiStore{data: data}
			s.cfg.Bridge.PersonalStickerUserID = "@personal:example.org"
			s.cfg.Bridge.PersonalStickerAccessToken = "token"

			stableType := event.AccountDataImagePackRooms.Type
			unstableType := event.AccountDataUnstableImagePackRooms.Type
			visibility := &fakeBuiltinPackVisibilityClient{
				members:      map[id.RoomID]event.Membership{"!created-1:example.com": event.MembershipJoin},
				memberErrors: map[id.RoomID]error{},
				accountData: map[string]event.ImagePackRoomsEventContent{
					stableType: {Rooms: map[id.RoomID]map[string]struct{}{
						"!old-source:example.org": {builtinEmojiStateKey: {}, "other": {}},
					}},
					unstableType: {Rooms: map[id.RoomID]map[string]struct{}{
						"!old-compat-source:example.org": {builtinEmojiStateKey: {}, "other": {}},
					}},
				},
				getErrors: map[string]error{},
				setErrors: map[string]error{},
			}
			before := map[string]event.ImagePackRoomsEventContent{
				stableType:   cloneImagePackRoomsForTest(visibility.accountData[stableType]),
				unstableType: cloneImagePackRoomsForTest(visibility.accountData[unstableType]),
			}
			s.newBuiltinPackVisibilityClient = func(id.UserID, string) (builtinPackVisibilityClient, error) { return visibility, nil }

			result, err := test.transition(s)
			if !errors.Is(err, injected) {
				t.Fatalf("transition result=%#v error=%v, want persistence failure", result, err)
			}
			if !reflect.DeepEqual(visibility.accountData, before) {
				t.Fatalf("account data after rollback = %#v, want preimages %#v", visibility.accountData, before)
			}
			for _, eventType := range []string{stableType, unstableType} {
				if _, exists := visibility.accountData[eventType].Rooms["!created-1:example.com"]; exists {
					t.Fatalf("%s retained orphan new Space source: %#v", eventType, visibility.accountData[eventType].Rooms)
				}
			}
			if len(visibility.setCalls) != test.wantSetCalls {
				t.Fatalf("account-data writes = %#v, want %d transactional attempts plus rollback", visibility.setCalls, test.wantSetCalls)
			}
		})
	}
}

func TestRebuildAccountSpaceUpdatesAccountDataBeforePersistenceAndExcludesOldCandidate(t *testing.T) {
	accounts, _ := seededAccountStore(t)
	oldAccount, _ := accounts.get("wxid_old")
	oldSpace := id.RoomID(oldAccount.SpaceRoom)
	newSpace := id.RoomID("!created-1:example.com")
	injected := errors.New("persist replacement failed")
	s, _ := newSpacePersistenceTestServerWithRecorder(t, accounts)
	data := builtinEmojiTestData(builtinEmojiRecord{Shortcode: "wechat_smile", MXC: "mxc://example.org/smile"})
	s.rawImagePackState = newFakeRawImagePackState()
	s.builtinEmojiStore = &builtinEmojiStore{data: data}
	s.cfg.Bridge.PersonalStickerUserID = "@personal:example.org"
	s.cfg.Bridge.PersonalStickerAccessToken = "token"
	stableType := event.AccountDataImagePackRooms.Type
	unstableType := event.AccountDataUnstableImagePackRooms.Type
	visibility := &fakeBuiltinPackVisibilityClient{
		members:      map[id.RoomID]event.Membership{oldSpace: event.MembershipJoin, newSpace: event.MembershipJoin},
		memberErrors: map[id.RoomID]error{},
		accountData: map[string]event.ImagePackRoomsEventContent{
			stableType:   {Rooms: map[id.RoomID]map[string]struct{}{oldSpace: {builtinEmojiStateKey: {}, "keep": {}}}},
			unstableType: {Rooms: map[id.RoomID]map[string]struct{}{oldSpace: {"": {}, "keep": {}}}},
		},
		getErrors:         map[string]error{},
		setErrors:         map[string]error{},
		applyThenErrorsAt: map[int]error{2: context.DeadlineExceeded},
	}
	before := map[string]event.ImagePackRoomsEventContent{
		stableType:   cloneImagePackRoomsForTest(visibility.accountData[stableType]),
		unstableType: cloneImagePackRoomsForTest(visibility.accountData[unstableType]),
	}
	s.newBuiltinPackVisibilityClient = func(id.UserID, string) (builtinPackVisibilityClient, error) { return visibility, nil }
	var orderingErr error
	accounts.persist = func(_ string, pending []byte, _ fs.FileMode) error {
		var store accountStoreData
		if err := json.Unmarshal(pending, &store); err != nil {
			orderingErr = err
			return injected
		}
		updated := store.Accounts["wxid_old"]
		if updated.SpaceRoom != newSpace.String() || !reflect.DeepEqual(updated.RetiredSpaceRooms, []string{oldSpace.String()}) {
			orderingErr = fmt.Errorf("pending account = %#v", updated)
		}
		if hasImagePackRoomKey(visibility.accountData[stableType].Rooms[oldSpace], builtinEmojiStateKey) ||
			hasImagePackRoomKey(visibility.accountData[unstableType].Rooms[oldSpace], "") ||
			!hasImagePackRoomKey(visibility.accountData[stableType].Rooms[newSpace], builtinEmojiStateKey) ||
			!hasImagePackRoomKey(visibility.accountData[unstableType].Rooms[newSpace], "") {
			orderingErr = fmt.Errorf("account data was not switched before persistence: %#v", visibility.accountData)
		}
		return injected
	}

	_, err := s.rebuildAccountSpace(context.Background(), "wxid_old", "")
	if !errors.Is(err, injected) {
		t.Fatalf("rebuild error = %v, want persistence failure", err)
	}
	if orderingErr != nil {
		t.Fatal(orderingErr)
	}
	if !reflect.DeepEqual(visibility.memberCalls, []id.RoomID{newSpace}) {
		t.Fatalf("membership candidates = %#v, want only preferred new Space", visibility.memberCalls)
	}
	if !reflect.DeepEqual(visibility.accountData, before) {
		t.Fatalf("account data after rollback = %#v, want %#v", visibility.accountData, before)
	}
	if len(visibility.setCalls) != 6 {
		t.Fatalf("account-data calls = %#v, want three attempted document writes and three verified rollbacks", visibility.setCalls)
	}
}

func TestGlobalAccountDataRetirementPersistsPendingAcrossRestartAndRetriesClear(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounts.json")
	accounts := newAccountStore(path)
	oldSpace := id.RoomID("!retired:example.org")
	currentSpace := id.RoomID("!current:example.org")
	if err := accounts.put(savedAccount{ID: "a", SpaceRoom: currentSpace.String(), RetiredSpaceRooms: []string{oldSpace.String()}}); err != nil {
		t.Fatal(err)
	}
	data := builtinEmojiTestData(builtinEmojiRecord{Shortcode: "wechat_smile", MXC: "mxc://example.org/global"})
	state := newFakeRawImagePackState()
	seedBuiltinPackRoom(t, state, currentSpace, data)
	stableType := event.AccountDataImagePackRooms.Type
	unstableType := event.AccountDataUnstableImagePackRooms.Type
	visibility := &fakeBuiltinPackVisibilityClient{
		members: map[id.RoomID]event.Membership{currentSpace: event.MembershipInvite}, memberErrors: map[id.RoomID]error{},
		accountData: map[string]event.ImagePackRoomsEventContent{
			stableType:   {Rooms: map[id.RoomID]map[string]struct{}{oldSpace: {builtinEmojiStateKey: {}, "keep": {}}}},
			unstableType: {Rooms: map[id.RoomID]map[string]struct{}{oldSpace: {"": {}, "keep": {}}}},
		},
		getErrors: map[string]error{}, setErrors: map[string]error{},
	}
	s := builtinEmojiPackTestServer(data, state)
	s.accounts = accounts
	s.cfg.Bridge.PersonalStickerUserID = "@personal:example.org"
	s.cfg.Bridge.PersonalStickerAccessToken = "token"
	s.newBuiltinPackVisibilityClient = func(id.UserID, string) (builtinPackVisibilityClient, error) { return visibility, nil }
	injected := errors.New("persist retired clear failed")
	accounts.persist = func(string, []byte, fs.FileMode) error { return injected }
	_, first := s.reconcileBuiltinEmojiPacks(context.Background())
	if !strings.Contains(strings.Join(first.Warnings, "; "), injected.Error()) {
		t.Fatalf("clear persistence warning missing: %#v", first)
	}
	pending, _ := accounts.get("a")
	if !reflect.DeepEqual(pending.RetiredSpaceRooms, []string{oldSpace.String()}) {
		t.Fatalf("failed clear lost pending retired Space: %#v", pending.RetiredSpaceRooms)
	}
	if hasImagePackRoomKey(visibility.accountData[stableType].Rooms[oldSpace], builtinEmojiStateKey) || hasImagePackRoomKey(visibility.accountData[unstableType].Rooms[oldSpace], "") {
		t.Fatalf("successful Matrix retirement left built-in references: %#v", visibility.accountData)
	}

	reloaded := newAccountStore(path)
	reloadedPending, _ := reloaded.get("a")
	if !reflect.DeepEqual(reloadedPending.RetiredSpaceRooms, []string{oldSpace.String()}) {
		t.Fatalf("restart lost pending retired Space: %#v", reloadedPending.RetiredSpaceRooms)
	}
	retry := builtinEmojiPackTestServer(data, state)
	retry.accounts = reloaded
	retry.cfg.Bridge.PersonalStickerUserID = "@personal:example.org"
	retry.cfg.Bridge.PersonalStickerAccessToken = "token"
	retry.newBuiltinPackVisibilityClient = func(id.UserID, string) (builtinPackVisibilityClient, error) { return visibility, nil }
	_, second := retry.reconcileBuiltinEmojiPacks(context.Background())
	if strings.Contains(strings.Join(second.Warnings, "; "), "retired") && second.Status == builtinPackStatusFailed {
		t.Fatalf("retry result = %#v", second)
	}
	cleared, _ := newAccountStore(path).get("a")
	if len(cleared.RetiredSpaceRooms) != 0 {
		t.Fatalf("successful retry did not clear persisted pending IDs: %#v", cleared.RetiredSpaceRooms)
	}
}

func TestGlobalAccountDataRetirementRetriesTransientMatrixFailureAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounts.json")
	accounts := newAccountStore(path)
	oldSpace := id.RoomID("!retired:example.org")
	currentSpace := id.RoomID("!current:example.org")
	if err := accounts.put(savedAccount{ID: "a", SpaceRoom: currentSpace.String(), RetiredSpaceRooms: []string{oldSpace.String()}}); err != nil {
		t.Fatal(err)
	}
	data := builtinEmojiTestData(builtinEmojiRecord{Shortcode: "wechat_smile", MXC: "mxc://example.org/global"})
	state := newFakeRawImagePackState()
	seedBuiltinPackRoom(t, state, currentSpace, data)
	stableType := event.AccountDataImagePackRooms.Type
	unstableType := event.AccountDataUnstableImagePackRooms.Type
	transient := errors.New("transient Matrix retirement failure")
	visibility := &fakeBuiltinPackVisibilityClient{
		members: map[id.RoomID]event.Membership{currentSpace: event.MembershipInvite}, memberErrors: map[id.RoomID]error{},
		accountData: map[string]event.ImagePackRoomsEventContent{
			stableType:   {Rooms: map[id.RoomID]map[string]struct{}{oldSpace: {builtinEmojiStateKey: {}}}},
			unstableType: {Rooms: map[id.RoomID]map[string]struct{}{oldSpace: {"": {}}}},
		},
		getErrors: map[string]error{}, setErrors: map[string]error{}, setErrorsAt: map[int]error{1: transient},
	}
	firstServer := builtinEmojiPackTestServer(data, state)
	firstServer.accounts = accounts
	firstServer.cfg.Bridge.PersonalStickerUserID = "@personal:example.org"
	firstServer.cfg.Bridge.PersonalStickerAccessToken = "token"
	firstServer.newBuiltinPackVisibilityClient = func(id.UserID, string) (builtinPackVisibilityClient, error) { return visibility, nil }
	_, first := firstServer.reconcileBuiltinEmojiPacks(context.Background())
	if first.Status != builtinPackStatusFailed || !strings.Contains(strings.Join(first.Warnings, "; "), transient.Error()) {
		t.Fatalf("transient retirement result = %#v", first)
	}
	pending, _ := newAccountStore(path).get("a")
	if !reflect.DeepEqual(pending.RetiredSpaceRooms, []string{oldSpace.String()}) {
		t.Fatalf("transient retirement lost persisted pending ID: %#v", pending.RetiredSpaceRooms)
	}

	visibility.setErrorsAt = map[int]error{}
	reloaded := newAccountStore(path)
	retryServer := builtinEmojiPackTestServer(data, state)
	retryServer.accounts = reloaded
	retryServer.cfg.Bridge.PersonalStickerUserID = "@personal:example.org"
	retryServer.cfg.Bridge.PersonalStickerAccessToken = "token"
	retryServer.newBuiltinPackVisibilityClient = func(id.UserID, string) (builtinPackVisibilityClient, error) { return visibility, nil }
	_, retry := retryServer.reconcileBuiltinEmojiPacks(context.Background())
	if retry.Status == builtinPackStatusFailed {
		t.Fatalf("retry result = %#v", retry)
	}
	cleared, _ := newAccountStore(path).get("a")
	if len(cleared.RetiredSpaceRooms) != 0 {
		t.Fatalf("successful Matrix retry did not clear pending ID: %#v", cleared.RetiredSpaceRooms)
	}
}

func TestBuiltinEmojiAccountDataRollbackRestoresApplyThenErrorAttempt(t *testing.T) {
	accounts, _ := seededAccountStore(t)
	injected := errors.New("persist Space failed")
	accounts.persist = func(string, []byte, fs.FileMode) error { return injected }
	s, _ := newSpacePersistenceTestServerWithRecorder(t, accounts)
	s.rawImagePackState = newFakeRawImagePackState()
	s.builtinEmojiStore = &builtinEmojiStore{data: builtinEmojiTestData(builtinEmojiRecord{Shortcode: "wechat_smile", MXC: "mxc://example.org/smile"})}
	s.cfg.Bridge.PersonalStickerUserID = "@personal:example.org"
	s.cfg.Bridge.PersonalStickerAccessToken = "token"

	stableType := event.AccountDataImagePackRooms.Type
	unstableType := event.AccountDataUnstableImagePackRooms.Type
	visibility := &fakeBuiltinPackVisibilityClient{
		members:      map[id.RoomID]event.Membership{"!created-1:example.com": event.MembershipJoin},
		memberErrors: map[id.RoomID]error{},
		accountData: map[string]event.ImagePackRoomsEventContent{
			stableType:   {Rooms: map[id.RoomID]map[string]struct{}{"!old-stable:example.org": {"other": {}}}},
			unstableType: {Rooms: map[id.RoomID]map[string]struct{}{"!old-compat:example.org": {"other": {}}}},
		},
		getErrors:         map[string]error{},
		setErrors:         map[string]error{},
		applyThenErrorsAt: map[int]error{2: context.DeadlineExceeded},
	}
	before := map[string]event.ImagePackRoomsEventContent{
		stableType:   cloneImagePackRoomsForTest(visibility.accountData[stableType]),
		unstableType: cloneImagePackRoomsForTest(visibility.accountData[unstableType]),
	}
	s.newBuiltinPackVisibilityClient = func(id.UserID, string) (builtinPackVisibilityClient, error) { return visibility, nil }

	_, err := s.ensureAccountSpaceTransition(context.Background(), "wxid_other")
	if !errors.Is(err, injected) {
		t.Fatalf("transition error = %v, want persistence failure", err)
	}
	if !reflect.DeepEqual(visibility.accountData, before) {
		t.Fatalf("account data after ambiguous write rollback = %#v, want all attempted preimages %#v", visibility.accountData, before)
	}
	if len(visibility.setCalls) != 4 {
		t.Fatalf("account-data calls = %#v, want two attempts and two rollbacks", visibility.setCalls)
	}
}

func TestBuiltinEmojiAccountDataRollbackVerificationDetectsUnrestoredPreimage(t *testing.T) {
	accounts, _ := seededAccountStore(t)
	injected := errors.New("persist Space failed")
	accounts.persist = func(string, []byte, fs.FileMode) error { return injected }
	s, _ := newSpacePersistenceTestServerWithRecorder(t, accounts)
	s.rawImagePackState = newFakeRawImagePackState()
	s.builtinEmojiStore = &builtinEmojiStore{data: builtinEmojiTestData(builtinEmojiRecord{Shortcode: "wechat_smile", MXC: "mxc://example.org/smile"})}
	s.cfg.Bridge.PersonalStickerUserID = "@personal:example.org"
	s.cfg.Bridge.PersonalStickerAccessToken = "token"
	visibility := &fakeBuiltinPackVisibilityClient{
		members: map[id.RoomID]event.Membership{"!created-1:example.com": event.MembershipJoin}, memberErrors: map[id.RoomID]error{},
		accountData: map[string]event.ImagePackRoomsEventContent{}, getErrors: map[string]error{}, setErrors: map[string]error{},
		ignoreSetsAt: map[int]bool{3: true},
	}
	s.newBuiltinPackVisibilityClient = func(id.UserID, string) (builtinPackVisibilityClient, error) { return visibility, nil }

	result, err := s.ensureAccountSpaceTransition(context.Background(), "wxid_other")
	if !errors.Is(err, injected) || !strings.Contains(err.Error(), "restored content does not match preimage") {
		t.Fatalf("transition result=%#v error=%v, want persistence and rollback verification failures", result, err)
	}
	if !strings.Contains(strings.Join(result.Warnings, "; "), "verify rollback account data") {
		t.Fatalf("rollback verification failure missing from warnings: %#v", result.Warnings)
	}
}

func TestBuiltinEmojiAccountDataRollbackFailureIsSurfaced(t *testing.T) {
	accounts, _ := seededAccountStore(t)
	injected := errors.New("persist Space failed")
	rollbackFailure := errors.New("rollback account data failed")
	accounts.persist = func(string, []byte, fs.FileMode) error { return injected }
	s, _ := newSpacePersistenceTestServerWithRecorder(t, accounts)
	s.rawImagePackState = newFakeRawImagePackState()
	s.builtinEmojiStore = &builtinEmojiStore{data: builtinEmojiTestData(builtinEmojiRecord{Shortcode: "wechat_smile", MXC: "mxc://example.org/smile"})}
	s.cfg.Bridge.PersonalStickerUserID = "@personal:example.org"
	s.cfg.Bridge.PersonalStickerAccessToken = "token"
	visibility := &fakeBuiltinPackVisibilityClient{
		members: map[id.RoomID]event.Membership{"!created-1:example.com": event.MembershipJoin}, memberErrors: map[id.RoomID]error{},
		accountData: map[string]event.ImagePackRoomsEventContent{}, getErrors: map[string]error{}, setErrors: map[string]error{},
		setErrorsAt: map[int]error{3: rollbackFailure},
	}
	s.newBuiltinPackVisibilityClient = func(id.UserID, string) (builtinPackVisibilityClient, error) { return visibility, nil }

	result, err := s.ensureAccountSpaceTransition(context.Background(), "wxid_other")
	if !errors.Is(err, injected) || !errors.Is(err, rollbackFailure) {
		t.Fatalf("transition result=%#v error=%v, want persistence and rollback failures", result, err)
	}
	if !strings.Contains(strings.Join(result.Warnings, "; "), "rollback account data") {
		t.Fatalf("rollback failure missing from warnings: %#v", result.Warnings)
	}
}

func TestBuiltinEmojiSpaceDoesNotPromoteMatrixStateIntoEmptyStore(t *testing.T) {
	accounts, _ := seededAccountStore(t)
	s, _ := newSpacePersistenceTestServerWithRecorder(t, accounts)
	fakeState := newFakeRawImagePackState()
	fakeState.set("!old:example.com", event.StateImagePack, builtinEmojiStateKey, `{"images":{"wechat_smile":{"url":"mxc://example.org/from-matrix"}}}`)
	s.rawImagePackState = fakeState
	s.builtinEmojiStore = &builtinEmojiStore{data: builtinEmojiTestData()}

	result, err := s.rebuildAccountSpace(context.Background(), "wxid_old", "")
	if err != nil {
		t.Fatalf("rebuild with empty canonical store: %v", err)
	}
	if result.BuiltinPack.Status != builtinPackStatusSkipped {
		t.Fatalf("transition = %#v", result)
	}
	fakeState.mu.Lock()
	loads, saves := fakeState.loads, fakeState.saves
	fakeState.mu.Unlock()
	if loads != 0 || saves != 0 {
		t.Fatalf("empty canonical store read/wrote Matrix pack state: loads=%d saves=%d", loads, saves)
	}
	if got := s.builtinEmojiStore.Snapshot(); len(got.Images) != 0 {
		t.Fatalf("Matrix state was promoted into canonical store: %#v", got)
	}
}

func TestBuiltinEmojiSpaceAccountBUsesGlobalStore(t *testing.T) {
	data := builtinEmojiTestData(builtinEmojiRecord{Shortcode: "wechat_smile", MXC: "mxc://example.org/global"})
	fakeState := newFakeRawImagePackState()
	accounts := newAccountStore(filepath.Join(t.TempDir(), "accounts.json"))
	for _, acc := range []savedAccount{
		{ID: "a", SpaceRoom: "!a-space:example.org", SpaceFolders: builtinEmojiTestFolders("a")},
		{ID: "b", SpaceRoom: "!b-space:example.org", SpaceFolders: builtinEmojiTestFolders("b")},
	} {
		if err := accounts.put(acc); err != nil {
			t.Fatal(err)
		}
	}
	s, _ := newSpacePersistenceTestServerWithRecorder(t, accounts)
	s.rawImagePackState = fakeState
	s.builtinEmojiStore = &builtinEmojiStore{data: data}

	result, err := s.ensureAccountSpaceTransition(context.Background(), "b")
	if err != nil {
		t.Fatalf("ensure account B Space: %v", err)
	}
	if result.Space != "!b-space:example.org" || result.BuiltinPack.Status != builtinPackStatusSuccess {
		t.Fatalf("transition = %#v", result)
	}
	for _, room := range []id.RoomID{"!a-space:example.org", "!b-space:example.org"} {
		assertBuiltinRoomURL(t, fakeState, room, "mxc://example.org/global")
	}
}

type accountSpaceAwareVisibilityClient struct {
	*fakeBuiltinPackVisibilityClient
	accounts *accountStore
	newSpace id.RoomID
}

func (client *accountSpaceAwareVisibilityClient) Membership(_ context.Context, room id.RoomID, _ id.UserID) (event.Membership, error) {
	client.memberCalls = append(client.memberCalls, room)
	if room == client.newSpace {
		account, _ := client.accounts.get("a")
		if id.RoomID(account.SpaceRoom) == client.newSpace {
			return event.MembershipJoin, nil
		}
		return event.MembershipInvite, nil
	}
	return client.members[room], client.memberErrors[room]
}

func TestRebuildAccountSpaceRetiresOldAccountDataWhenNewSpaceIsInviteOnly(t *testing.T) {
	data := builtinEmojiTestData(builtinEmojiRecord{Shortcode: "wechat_smile", MXC: "mxc://example.org/global"})
	accounts := newAccountStore(filepath.Join(t.TempDir(), "accounts.json"))
	oldSpace := id.RoomID("!old-space:example.org")
	if err := accounts.put(savedAccount{ID: "a", SpaceRoom: oldSpace.String(), SpaceFolders: builtinEmojiTestFolders("old")}); err != nil {
		t.Fatal(err)
	}
	fakeState := newFakeRawImagePackState()
	seedBuiltinPackRoom(t, fakeState, oldSpace, data)
	s, _ := newSpacePersistenceTestServerWithRecorder(t, accounts)
	s.rawImagePackState = fakeState
	s.builtinEmojiStore = &builtinEmojiStore{data: data}
	s.cfg.Bridge.PersonalStickerUserID = "@personal:example.org"
	s.cfg.Bridge.PersonalStickerAccessToken = "token"
	stableType := event.AccountDataImagePackRooms.Type
	unstableType := event.AccountDataUnstableImagePackRooms.Type
	visibility := &fakeBuiltinPackVisibilityClient{
		members: map[id.RoomID]event.Membership{
			oldSpace:                 event.MembershipJoin,
			"!created-1:example.com": event.MembershipInvite,
		},
		memberErrors: map[id.RoomID]error{},
		accountData: map[string]event.ImagePackRoomsEventContent{
			stableType: {Rooms: map[id.RoomID]map[string]struct{}{
				oldSpace:                 {builtinEmojiStateKey: {}, "keep-old": {}},
				"!unrelated:example.org": {builtinEmojiStateKey: {}, "keep-other": {}},
			}},
			unstableType: {Rooms: map[id.RoomID]map[string]struct{}{
				oldSpace:                 {"": {}, "keep-old": {}},
				"!unrelated:example.org": {"": {}, "keep-other": {}},
			}},
		},
		getErrors: map[string]error{},
		setErrors: map[string]error{},
	}
	s.newBuiltinPackVisibilityClient = func(id.UserID, string) (builtinPackVisibilityClient, error) { return visibility, nil }

	result, err := s.rebuildAccountSpace(context.Background(), "a", "")
	if err != nil {
		t.Fatalf("rebuild account Space: %v", err)
	}
	if result.AccountData.Room != "" || result.AccountData.Status != builtinPackStatusSkipped {
		t.Fatalf("account-data status = %#v, want no valid joined source after rebuild", result.AccountData)
	}
	for _, candidate := range visibility.memberCalls {
		if candidate == oldSpace {
			t.Fatalf("retired old Space was considered as a visibility candidate: %#v", visibility.memberCalls)
		}
	}
	if keys := visibility.accountData[stableType].Rooms[oldSpace]; keys == nil || hasImagePackRoomKey(keys, builtinEmojiStateKey) || !hasImagePackRoomKey(keys, "keep-old") {
		t.Fatalf("retired stable source was not removed precisely: %#v", visibility.accountData[stableType].Rooms)
	}
	if keys := visibility.accountData[unstableType].Rooms[oldSpace]; keys == nil || hasImagePackRoomKey(keys, "") || !hasImagePackRoomKey(keys, "keep-old") {
		t.Fatalf("retired compatibility source was not removed precisely: %#v", visibility.accountData[unstableType].Rooms)
	}
	for eventType, key := range map[string]string{stableType: builtinEmojiStateKey, unstableType: ""} {
		keys := visibility.accountData[eventType].Rooms["!unrelated:example.org"]
		if !hasImagePackRoomKey(keys, key) || !hasImagePackRoomKey(keys, "keep-other") {
			t.Fatalf("cleanup changed unrelated %s sources: %#v", eventType, visibility.accountData[eventType].Rooms)
		}
	}
	if warnings := strings.Join(result.Warnings, "; "); !strings.Contains(warnings, "no joined room") || !strings.Contains(warnings, oldSpace.String()) {
		t.Fatalf("rebuild warnings = %q, want missing-source and retired-Space status", warnings)
	}
}

func TestRebuildAccountSpaceRegistersNewSpaceAfterReplacement(t *testing.T) {
	data := builtinEmojiTestData(builtinEmojiRecord{Shortcode: "wechat_smile", MXC: "mxc://example.org/global"})
	accounts := newAccountStore(filepath.Join(t.TempDir(), "accounts.json"))
	oldSpace := id.RoomID("!old-space:example.org")
	newSpace := id.RoomID("!created-1:example.com")
	if err := accounts.put(savedAccount{ID: "a", SpaceRoom: oldSpace.String(), SpaceFolders: builtinEmojiTestFolders("old")}); err != nil {
		t.Fatal(err)
	}
	fakeState := newFakeRawImagePackState()
	seedBuiltinPackRoom(t, fakeState, oldSpace, data)
	s, _ := newSpacePersistenceTestServerWithRecorder(t, accounts)
	s.rawImagePackState = fakeState
	s.builtinEmojiStore = &builtinEmojiStore{data: data}
	s.cfg.Bridge.PersonalStickerUserID = "@personal:example.org"
	s.cfg.Bridge.PersonalStickerAccessToken = "token"
	stableType := event.AccountDataImagePackRooms.Type
	unstableType := event.AccountDataUnstableImagePackRooms.Type
	base := &fakeBuiltinPackVisibilityClient{
		members:      map[id.RoomID]event.Membership{oldSpace: event.MembershipJoin},
		memberErrors: map[id.RoomID]error{},
		accountData: map[string]event.ImagePackRoomsEventContent{
			stableType:   {Rooms: map[id.RoomID]map[string]struct{}{oldSpace: {builtinEmojiStateKey: {}, "keep": {}}}},
			unstableType: {Rooms: map[id.RoomID]map[string]struct{}{oldSpace: {"": {}, "keep": {}}}},
		},
		getErrors: map[string]error{},
		setErrors: map[string]error{},
	}
	visibility := &accountSpaceAwareVisibilityClient{fakeBuiltinPackVisibilityClient: base, accounts: accounts, newSpace: newSpace}
	s.newBuiltinPackVisibilityClient = func(id.UserID, string) (builtinPackVisibilityClient, error) { return visibility, nil }

	result, err := s.rebuildAccountSpace(context.Background(), "a", "")
	if err != nil {
		t.Fatalf("rebuild account Space: %v", err)
	}
	if result.AccountData.Status != builtinPackStatusSuccess || result.AccountData.Room != newSpace {
		t.Fatalf("account-data registration = %#v, want successful current replacement Space", result.AccountData)
	}
	if hasImagePackRoomKey(visibility.accountData[stableType].Rooms[oldSpace], builtinEmojiStateKey) || hasImagePackRoomKey(visibility.accountData[unstableType].Rooms[oldSpace], "") {
		t.Fatalf("old Space retained built-in account-data sources: %#v", visibility.accountData)
	}
	if !hasImagePackRoomKey(visibility.accountData[stableType].Rooms[newSpace], builtinEmojiStateKey) || !hasImagePackRoomKey(visibility.accountData[unstableType].Rooms[newSpace], "") {
		t.Fatalf("new Space was not registered in both account-data documents: %#v", visibility.accountData)
	}
	if !hasImagePackRoomKey(visibility.accountData[stableType].Rooms[oldSpace], "keep") || !hasImagePackRoomKey(visibility.accountData[unstableType].Rooms[oldSpace], "keep") {
		t.Fatalf("new registration removed unrelated old-Space state keys: %#v", visibility.accountData)
	}
}

func hasImagePackRoomKey(keys map[string]struct{}, key string) bool {
	_, ok := keys[key]
	return ok
}

func TestRebuildAccountSpaceReconcilesEverySavedSpaceWithoutPortals(t *testing.T) {
	data := builtinEmojiTestData(builtinEmojiRecord{Shortcode: "wechat_smile", MXC: "mxc://example.org/global"})
	accounts := newAccountStore(filepath.Join(t.TempDir(), "accounts.json"))
	for _, acc := range []savedAccount{
		{ID: "a", SpaceRoom: "!a-old:example.org", SpaceFolders: builtinEmojiTestFolders("a")},
		{ID: "b", SpaceRoom: "!b-space:example.org", SpaceFolders: builtinEmojiTestFolders("b")},
	} {
		if err := accounts.put(acc); err != nil {
			t.Fatal(err)
		}
	}
	fakeState := newFakeRawImagePackState()
	fakeState.set("!b-space:example.org", event.StateImagePack, builtinEmojiStateKey, `{"images":{"wechat_smile":{"url":"mxc://example.org/stale"}}}`)
	fakeState.set("!b-space:example.org", event.StateUnstableImagePack, "", `{"images":{"wechat_smile":{"url":"mxc://example.org/stale"}}}`)
	s, _ := newSpacePersistenceTestServerWithRecorder(t, accounts)
	s.rawImagePackState = fakeState
	s.builtinEmojiStore = &builtinEmojiStore{data: data}

	result, err := s.rebuildAccountSpace(context.Background(), "a", "")
	if err != nil {
		t.Fatalf("rebuild account A: %v", err)
	}
	if result.BuiltinPack.Status != builtinPackStatusSuccess || result.BuiltinPack.AttemptedRooms != 2 || result.BuiltinPack.ReconciledRooms != 2 {
		t.Fatalf("rebuild reconciliation = %#v, want prepared A and saved B exactly once", result.BuiltinPack)
	}
	assertBuiltinRoomURL(t, fakeState, result.Space, "mxc://example.org/global")
	assertBuiltinRoomURL(t, fakeState, "!b-space:example.org", "mxc://example.org/global")
}

func TestRepairAccountSpaceCountsUniquePortalsAndReconcilesGloballyOnce(t *testing.T) {
	data := builtinEmojiTestData(builtinEmojiRecord{Shortcode: "wechat_smile", MXC: "mxc://example.org/global"})
	accounts := newAccountStore(filepath.Join(t.TempDir(), "accounts.json"))
	if err := accounts.put(savedAccount{ID: "a", SpaceRoom: "!a-space:example.org", SpaceFolders: builtinEmojiTestFolders("a")}); err != nil {
		t.Fatal(err)
	}
	fakeState := newFakeRawImagePackState()
	s, _ := newSpacePersistenceTestServerWithRecorder(t, accounts)
	s.rawImagePackState = fakeState
	s.builtinEmojiStore = &builtinEmojiStore{data: data}
	s.portals.data.Portals = map[string]portalRecord{
		"one":       {ChatID: "one", RoomID: "!one:example.org"},
		"two":       {ChatID: "two", RoomID: "!two:example.org"},
		"two-alias": {ChatID: "two-alias", RoomID: "!two:example.org"},
	}

	result, err := s.repairAccountSpace(context.Background(), "a", "")
	if err != nil {
		t.Fatalf("repair account Space: %v", err)
	}
	if result.PortalCount != 2 {
		t.Fatalf("PortalCount = %d, want exactly 2 unique portal rooms", result.PortalCount)
	}
	stableCalls := map[id.RoomID]int{}
	fakeState.mu.Lock()
	for _, call := range fakeState.saveCalls {
		if call.eventType == event.StateImagePack.Type && call.stateKey == builtinEmojiStateKey {
			stableCalls[call.room]++
		}
	}
	fakeState.mu.Unlock()
	wantCalls := map[id.RoomID]int{
		"!a-space:example.org": 1,
		"!one:example.org":     1,
		"!two:example.org":     1,
	}
	if !reflect.DeepEqual(stableCalls, wantCalls) {
		t.Fatalf("stable built-in reconciliation calls = %#v, want one bounded global pass %#v", stableCalls, wantCalls)
	}
}

func TestStartupBuiltinEmojiLifecycleRunsOneGlobalPass(t *testing.T) {
	for _, loggedIn := range []bool{false, true} {
		t.Run(fmt.Sprintf("logged-in-%t", loggedIn), func(t *testing.T) {
			data := builtinEmojiTestData(builtinEmojiRecord{Shortcode: "wechat_smile", MXC: "mxc://example.org/global"})
			accounts := newAccountStore(filepath.Join(t.TempDir(), "accounts.json"))
			if err := accounts.put(savedAccount{ID: "a", SpaceRoom: "!a-space:example.org", SpaceFolders: builtinEmojiTestFolders("a")}); err != nil {
				t.Fatal(err)
			}
			if _, err := accounts.setCurrent("a"); err != nil {
				t.Fatal(err)
			}
			fakeState := newFakeRawImagePackState()
			s, _ := newSpacePersistenceTestServerWithRecorder(t, accounts)
			s.api = &accountSessionAPI{self: "a"}
			s.rawImagePackState = fakeState
			s.builtinEmojiStore = &builtinEmojiStore{data: data}
			s.portals.data.Portals = map[string]portalRecord{
				"one": {ChatID: "one", RoomID: "!one:example.org"},
				"two": {ChatID: "two", RoomID: "!two:example.org"},
			}

			s.bootstrapStartupSpaces(context.Background(), loggedIn)

			stableCalls := map[id.RoomID]int{}
			fakeState.mu.Lock()
			for _, call := range fakeState.saveCalls {
				if call.eventType == event.StateImagePack.Type && call.stateKey == builtinEmojiStateKey {
					stableCalls[call.room]++
				}
			}
			fakeState.mu.Unlock()
			want := map[id.RoomID]int{"!a-space:example.org": 1, "!one:example.org": 1, "!two:example.org": 1}
			if !reflect.DeepEqual(stableCalls, want) {
				t.Fatalf("startup stable built-in calls = %#v, want one global pass %#v", stableCalls, want)
			}
		})
	}
}

func TestBuiltinEmojiPortalCreationWithoutExistingSpaceBootstrapsPortalAndNewSpaceOnce(t *testing.T) {
	accounts := newAccountStore(filepath.Join(t.TempDir(), "accounts.json"))
	if err := accounts.put(savedAccount{ID: "wxid_self"}); err != nil {
		t.Fatal(err)
	}
	created := 0
	homeserver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/_matrix/client/v3/createRoom" {
			created++
			_, _ = fmt.Fprintf(w, `{"room_id":"!created-%d:example.org"}`, created)
			return
		}
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/state") {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		_, _ = w.Write([]byte(`{"event_id":"$event"}`))
	}))
	t.Cleanup(homeserver.Close)
	fakeState := newFakeRawImagePackState()
	s := &Server{
		cfg: &config.Config{Homeserver: config.Homeserver{Domain: "example.org"}}, as: testAppservice(t, homeserver.URL), api: portalCreationAPI{},
		accounts: accounts, portals: newPortalStore(filepath.Join(t.TempDir(), "portals.json")), contacts: newContactStore(filepath.Join(t.TempDir(), "contacts.json")),
		rooms: map[id.RoomID]bool{}, rawImagePackState: fakeState,
		builtinEmojiStore: &builtinEmojiStore{data: builtinEmojiTestData(builtinEmojiRecord{Shortcode: "wechat_smile", MXC: "mxc://example.org/global"})},
	}

	rec, err := s.ensurePortal(context.Background(), "future@chatroom")
	if err != nil {
		t.Fatalf("create future portal: %v", err)
	}
	account, ok := accounts.get("wxid_self")
	if !ok || account.SpaceRoom == "" {
		t.Fatalf("new account Space was not persisted: %#v, ok=%v", account, ok)
	}
	space := id.RoomID(account.SpaceRoom)
	calls := map[rawPackStateKey]int{}
	fakeState.mu.Lock()
	for _, call := range fakeState.saveCalls {
		if (call.eventType == event.StateImagePack.Type && call.stateKey == builtinEmojiStateKey) ||
			(call.eventType == event.StateUnstableImagePack.Type && call.stateKey == "") {
			calls[call]++
		}
	}
	fakeState.mu.Unlock()
	for _, room := range []id.RoomID{rec.RoomID, space} {
		for _, target := range imagePackStateEventTargets(builtinEmojiStateKey) {
			key := rawPackStateKey{room: room, eventType: target.EventType.Type, stateKey: target.StateKey}
			if calls[key] != 1 {
				t.Fatalf("built-in target %#v writes = %d, want exactly one; all calls=%#v", key, calls[key], calls)
			}
		}
	}
	if len(calls) != 4 {
		t.Fatalf("built-in writes = %#v, want exactly stable+compat for the new portal and Space", calls)
	}
}

func TestBuiltinEmojiPortalStructuralSpacePackFailureBlocksSpacePersistence(t *testing.T) {
	accounts := newAccountStore(filepath.Join(t.TempDir(), "accounts.json"))
	if err := accounts.put(savedAccount{ID: "wxid_self"}); err != nil {
		t.Fatal(err)
	}
	created := 0
	homeserver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/_matrix/client/v3/createRoom" {
			created++
			_, _ = fmt.Fprintf(w, `{"room_id":"!created-%d:example.org"}`, created)
			return
		}
		_, _ = w.Write([]byte(`{"event_id":"$event"}`))
	}))
	t.Cleanup(homeserver.Close)
	fakeState := newFakeRawImagePackState()
	fakeState.saveErrors = map[int]error{3: errors.New("new Space stable pack failed")}
	s := &Server{
		cfg: &config.Config{Homeserver: config.Homeserver{Domain: "example.org"}}, as: testAppservice(t, homeserver.URL), api: portalCreationAPI{},
		accounts: accounts, portals: newPortalStore(filepath.Join(t.TempDir(), "portals.json")), contacts: newContactStore(filepath.Join(t.TempDir(), "contacts.json")),
		rooms: map[id.RoomID]bool{}, rawImagePackState: fakeState,
		builtinEmojiStore: &builtinEmojiStore{data: builtinEmojiTestData(builtinEmojiRecord{Shortcode: "wechat_smile", MXC: "mxc://example.org/global"})},
	}

	if _, err := s.ensurePortal(context.Background(), "future@chatroom"); err != nil {
		t.Fatalf("portal creation should survive reported Space attachment failure: %v", err)
	}
	account, _ := accounts.get("wxid_self")
	if account.SpaceRoom != "" {
		t.Fatalf("failed new Space pack was persisted as %s", account.SpaceRoom)
	}
}

func TestBuiltinEmojiPortalCreationBootstrapsFuturePortal(t *testing.T) {
	accounts := newAccountStore(filepath.Join(t.TempDir(), "accounts.json"))
	if err := accounts.put(savedAccount{ID: "wxid_self", SpaceRoom: "!space:example.org", SpaceFolders: builtinEmojiTestFolders("self")}); err != nil {
		t.Fatal(err)
	}
	created := 0
	homeserver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/_matrix/client/v3/createRoom" {
			created++
			_, _ = fmt.Fprintf(w, `{"room_id":"!portal-%d:example.org"}`, created)
			return
		}
		_, _ = w.Write([]byte(`{"event_id":"$event"}`))
	}))
	t.Cleanup(homeserver.Close)
	as := testAppservice(t, homeserver.URL)
	fakeState := newFakeRawImagePackState()
	s := &Server{
		cfg: &config.Config{Homeserver: config.Homeserver{Domain: "example.org"}}, as: as, api: portalCreationAPI{},
		accounts: accounts, portals: newPortalStore(filepath.Join(t.TempDir(), "portals.json")), contacts: newContactStore(filepath.Join(t.TempDir(), "contacts.json")),
		rooms: map[id.RoomID]bool{}, rawImagePackState: fakeState,
		builtinEmojiStore: &builtinEmojiStore{data: builtinEmojiTestData(builtinEmojiRecord{Shortcode: "wechat_smile", MXC: "mxc://example.org/global"})},
	}

	rec, err := s.ensurePortal(context.Background(), "future@chatroom")
	if err != nil {
		t.Fatalf("create future portal: %v", err)
	}
	assertBuiltinRoomURL(t, fakeState, rec.RoomID, "mxc://example.org/global")
	fakeState.mu.Lock()
	stableCalls := append([]rawPackStateKey(nil), fakeState.saveCalls...)
	fakeState.mu.Unlock()
	count := 0
	for _, call := range stableCalls {
		if call.eventType == event.StateImagePack.Type && call.stateKey == builtinEmojiStateKey {
			count++
			if call.room != rec.RoomID {
				t.Fatalf("portal creation globally reconciled unrelated room %s", call.room)
			}
		}
	}
	if count != 1 {
		t.Fatalf("portal built-in stable writes = %d, want bootstrap of only new portal", count)
	}
}

func builtinEmojiPackTestServer(data builtinEmojiStoreData, state *fakeRawImagePackState) *Server {
	return &Server{
		cfg: &config.Config{}, rawImagePackState: state,
		builtinEmojiStore: &builtinEmojiStore{data: data},
		accounts:          &accountStore{data: accountStoreData{Accounts: map[string]savedAccount{}}},
		portals:           &portalStore{data: portalState{Portals: map[string]portalRecord{}, Pending: map[string]pendingPortal{}}},
	}
}

func seedBuiltinPackRoom(t *testing.T, state *fakeRawImagePackState, room id.RoomID, data builtinEmojiStoreData) {
	t.Helper()
	state.set(room, event.StateImagePack, builtinEmojiStateKey, string(buildBuiltinEmojiPack(data)))
	replacements := make(map[string]json.RawMessage, len(data.Images))
	for _, image := range data.Images {
		replacements[image.Shortcode] = json.RawMessage(fmt.Sprintf(`{"url":%q}`, image.MXC))
	}
	raw, err := mergePackImages(json.RawMessage(`{}`), replacements)
	if err != nil {
		t.Fatal(err)
	}
	state.set(room, event.StateUnstableImagePack, "", string(raw))
}

func rawJSONObjectForTest(t *testing.T, raw json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	var out map[string]json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode JSON object %s: %v", raw, err)
	}
	return out
}

func assertJSONPathEqual(t *testing.T, raw json.RawMessage, want any) {
	t.Helper()
	var got any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode JSON %s: %v", raw, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("JSON = %#v, want %#v", got, want)
	}
}

func cloneImagePackRoomsForTest(content event.ImagePackRoomsEventContent) event.ImagePackRoomsEventContent {
	clone := event.ImagePackRoomsEventContent{Rooms: map[id.RoomID]map[string]struct{}{}}
	for room, keys := range content.Rooms {
		clone.Rooms[room] = map[string]struct{}{}
		for key := range keys {
			clone.Rooms[room][key] = struct{}{}
		}
	}
	return clone
}

func builtinEmojiTestFolders(prefix string) map[string]string {
	return map[string]string{
		spaceFolderContacts: "!" + prefix + "-contacts:example.org",
		spaceFolderOfficial: "!" + prefix + "-official:example.org",
		spaceFolderGroups:   "!" + prefix + "-groups:example.org",
	}
}

func assertBuiltinRoomURL(t *testing.T, state *fakeRawImagePackState, room id.RoomID, want string) {
	t.Helper()
	stable := rawPackImageObjects(state.get(room, event.StateImagePack, builtinEmojiStateKey))
	compat := rawPackImageObjects(state.get(room, event.StateUnstableImagePack, ""))
	for name, images := range map[string]map[string]json.RawMessage{"stable": stable, "compat": compat} {
		var image struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal(images["wechat_smile"], &image); err != nil || image.URL != want {
			t.Fatalf("%s room %s image=%s err=%v, want URL %s", name, room, images["wechat_smile"], err, want)
		}
	}
}

func testAppservice(t *testing.T, homeserverURL string) *appservice.AppService {
	t.Helper()
	as := appservice.Create()
	as.Registration = &appservice.Registration{SenderLocalpart: "wechatbot", AppToken: "test-token"}
	as.HomeserverDomain = "example.org"
	if err := as.SetHomeserverURL(homeserverURL); err != nil {
		t.Fatal(err)
	}
	return as
}
