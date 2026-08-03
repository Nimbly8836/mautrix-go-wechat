package matrixas

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/leat/mautrix-go-wechat/internal/wechat/textemoji"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

const (
	builtinEmojiStateKey       = "wechat.builtin"
	builtinEmojiDisplayName    = "WeChat built-in emoji"
	builtinEmojiAttribution    = "Source repository: https://github.com/airinghost/wechat-emoji\nPinned commit: 2896eb55348041f5c5a66e4eccfb4882ab9cda0c\nSource path: web.wechat.com/compressed-cwebp/lossless-z9\nNo license found in the source repository."
	builtinEmojiBridgedPackURL = "https://github.com/airinghost/wechat-emoji/tree/2896eb55348041f5c5a66e4eccfb4882ab9cda0c"
)

type builtinPackStatus string

const (
	builtinPackStatusSkipped builtinPackStatus = "skipped"
	builtinPackStatusSuccess builtinPackStatus = "success"
	builtinPackStatusPartial builtinPackStatus = "partial"
	builtinPackStatusFailed  builtinPackStatus = "failed"
)

type reconciliationResult struct {
	Status          builtinPackStatus
	AttemptedRooms  int
	ReconciledRooms int
	FailedRooms     int
	Warnings        []string
}

type accountDataVisibilityResult struct {
	Status   builtinPackStatus
	User     id.UserID
	Room     id.RoomID
	Writes   int
	Warnings []string
}

type accountDataPreimage struct {
	EventType string
	Content   event.ImagePackRoomsEventContent
}

type accountDataWriteTransaction struct {
	client          builtinPackVisibilityClient
	preimages       []accountDataPreimage
	accountDataLock bool
}

func mergeReconciliationResults(first, second reconciliationResult) reconciliationResult {
	if first.Status == builtinPackStatusSkipped {
		second.Warnings = append(append([]string(nil), first.Warnings...), second.Warnings...)
		return second
	}
	if second.Status == builtinPackStatusSkipped {
		first.Warnings = append(append([]string(nil), first.Warnings...), second.Warnings...)
		return first
	}
	merged := reconciliationResult{
		AttemptedRooms:  first.AttemptedRooms + second.AttemptedRooms,
		ReconciledRooms: first.ReconciledRooms + second.ReconciledRooms,
		FailedRooms:     first.FailedRooms + second.FailedRooms,
		Warnings:        append(append([]string(nil), first.Warnings...), second.Warnings...),
		Status:          builtinPackStatusSuccess,
	}
	if merged.FailedRooms > 0 {
		if merged.ReconciledRooms > 0 {
			merged.Status = builtinPackStatusPartial
		} else {
			merged.Status = builtinPackStatusFailed
		}
	}
	return merged
}

type builtinPackVisibilityClient interface {
	Membership(context.Context, id.RoomID, id.UserID) (event.Membership, error)
	GetAccountData(context.Context, string, *event.ImagePackRoomsEventContent) error
	SetAccountData(context.Context, string, *event.ImagePackRoomsEventContent) error
}

type matrixBuiltinPackVisibilityClient struct {
	client *mautrix.Client
}

func (client matrixBuiltinPackVisibilityClient) Membership(ctx context.Context, room id.RoomID, user id.UserID) (event.Membership, error) {
	var content event.MemberEventContent
	if err := client.client.StateEvent(ctx, room, event.StateMember, user.String(), &content); err != nil {
		return "", err
	}
	return content.Membership, nil
}

func (client matrixBuiltinPackVisibilityClient) GetAccountData(ctx context.Context, eventType string, content *event.ImagePackRoomsEventContent) error {
	return client.client.GetAccountData(ctx, eventType, content)
}

func (client matrixBuiltinPackVisibilityClient) SetAccountData(ctx context.Context, eventType string, content *event.ImagePackRoomsEventContent) error {
	return client.client.SetAccountData(ctx, eventType, content)
}

func buildBuiltinEmojiPack(data builtinEmojiStoreData) json.RawMessage {
	if validateBuiltinEmojiStoreData(data) != nil || len(data.Images) == 0 {
		return nil
	}
	raw, err := buildBuiltinEmojiPackWithUnrelated(data, nil)
	if err != nil {
		return nil
	}
	return raw
}

func buildBuiltinEmojiPackWithUnrelated(data builtinEmojiStoreData, unrelated map[string]json.RawMessage) (json.RawMessage, error) {
	if err := validateBuiltinEmojiStoreData(data); err != nil {
		return nil, err
	}
	data = cloneBuiltinEmojiStoreData(data)
	sortBuiltinEmojiRecords(data.Images)
	canonical := make(map[string]builtinEmojiRecord, len(data.Images))
	for _, image := range data.Images {
		canonical[image.Shortcode] = image
	}

	var out bytes.Buffer
	out.WriteString(`{"images":{`)
	first := true
	for _, entry := range textemoji.Entries() {
		var value json.RawMessage
		if image, ok := canonical[entry.Shortcode]; ok {
			value, _ = json.Marshal(struct {
				URL string `json:"url"`
			}{URL: image.MXC})
		} else {
			value = unrelated[entry.Shortcode]
			if !json.Valid(value) {
				continue
			}
		}
		if !first {
			out.WriteByte(',')
		}
		first = false
		key, _ := json.Marshal(entry.Shortcode)
		out.Write(key)
		out.WriteByte(':')
		out.Write(value)
	}

	unrelatedKeys := make([]string, 0, len(unrelated))
	registry := builtinEmojiRegistryIndexes()
	for shortcode := range unrelated {
		if _, isCanonical := registry[shortcode]; !isCanonical {
			unrelatedKeys = append(unrelatedKeys, shortcode)
		}
	}
	sort.Strings(unrelatedKeys)
	for _, shortcode := range unrelatedKeys {
		if !json.Valid(unrelated[shortcode]) {
			continue
		}
		if !first {
			out.WriteByte(',')
		}
		first = false
		key, _ := json.Marshal(shortcode)
		out.Write(key)
		out.WriteByte(':')
		out.Write(unrelated[shortcode])
	}
	out.WriteString(`},"pack":`)
	metadata, err := json.Marshal(event.ImagePackMetadata{
		DisplayName: builtinEmojiDisplayName,
		Usage:       []event.ImagePackUsage{event.ImagePackUsageSticker, event.ImagePackUsageEmoji},
		Attribution: builtinEmojiAttribution,
		BridgedPack: &event.BridgedStickerPack{Network: "wechat", URL: builtinEmojiBridgedPackURL},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal built-in emoji metadata: %w", err)
	}
	out.Write(metadata)
	out.WriteByte('}')
	return out.Bytes(), nil
}

func rawImagePackImages(raw json.RawMessage) map[string]json.RawMessage {
	var content struct {
		Images map[string]json.RawMessage `json:"images"`
	}
	if json.Unmarshal(raw, &content) != nil || content.Images == nil {
		return map[string]json.RawMessage{}
	}
	images := make(map[string]json.RawMessage, len(content.Images))
	for shortcode, image := range content.Images {
		images[shortcode] = append(json.RawMessage(nil), image...)
	}
	return images
}

func builtinEmojiImageReplacements(data builtinEmojiStoreData) map[string]json.RawMessage {
	replacements := make(map[string]json.RawMessage, len(data.Images))
	for _, image := range data.Images {
		encoded, _ := json.Marshal(struct {
			URL string `json:"url"`
		}{URL: image.MXC})
		replacements[image.Shortcode] = encoded
	}
	return replacements
}

func (s *Server) builtinEmojiStoreSnapshot() (builtinEmojiStoreData, string) {
	if s == nil || s.builtinEmojiStore == nil {
		return builtinEmojiStoreData{}, "built-in emoji store is unavailable"
	}
	if err := s.builtinEmojiStore.LoadError(); err != nil {
		return builtinEmojiStoreData{}, fmt.Sprintf("built-in emoji store is invalid: %v", err)
	}
	data := s.builtinEmojiStore.Snapshot()
	if err := validateBuiltinEmojiStoreData(data); err != nil {
		return builtinEmojiStoreData{}, fmt.Sprintf("built-in emoji store snapshot is invalid: %v", err)
	}
	if len(data.Images) == 0 {
		return data, "built-in emoji store is empty"
	}
	return data, ""
}

func (s *Server) prepareBuiltinEmojiRoomLocked(ctx context.Context, room id.RoomID, data builtinEmojiStoreData) (json.RawMessage, json.RawMessage, error) {
	stableExisting, err := s.packState().Load(ctx, room, event.StateImagePack, builtinEmojiStateKey)
	if errors.Is(err, errImagePackStateNotFound) {
		stableExisting = json.RawMessage(`{}`)
	} else if err != nil {
		return nil, nil, fmt.Errorf("load stable built-in emoji pack: %w", err)
	}
	stable, err := buildBuiltinEmojiPackWithUnrelated(data, rawImagePackImages(stableExisting))
	if err != nil {
		return nil, nil, fmt.Errorf("build stable built-in emoji pack: %w", err)
	}

	compatExisting, err := s.packState().Load(ctx, room, event.StateUnstableImagePack, "")
	if errors.Is(err, errImagePackStateNotFound) {
		compatExisting = json.RawMessage(`{}`)
	} else if err != nil {
		return nil, nil, fmt.Errorf("load compatibility built-in emoji pack: %w", err)
	}
	compat, err := mergePackImages(compatExisting, builtinEmojiImageReplacements(data))
	if err != nil {
		return nil, nil, fmt.Errorf("merge compatibility built-in emoji pack: %w", err)
	}
	return stable, compat, nil
}

func (s *Server) reconcileBuiltinEmojiRoomLocked(ctx context.Context, room id.RoomID, data builtinEmojiStoreData) error {
	stable, compat, err := s.prepareBuiltinEmojiRoomLocked(ctx, room, data)
	if err != nil {
		return err
	}
	if err = s.packState().Save(ctx, room, event.StateImagePack, builtinEmojiStateKey, stable); err != nil {
		return fmt.Errorf("save stable built-in emoji pack: %w", err)
	}
	if err = s.packState().Save(ctx, room, event.StateUnstableImagePack, "", compat); err != nil {
		return fmt.Errorf("save compatibility built-in emoji pack: %w", err)
	}
	return nil
}

func (s *Server) reconcileBuiltinEmojiRoomWithTimeoutLocked(ctx context.Context, room id.RoomID, data builtinEmojiStoreData) error {
	operationCtx, cancel := s.matrixOperationContext(ctx)
	defer cancel()
	return s.reconcileBuiltinEmojiRoomLocked(operationCtx, room, data)
}

func (s *Server) builtinEmojiReconciliationRooms() []id.RoomID {
	unique := make(map[id.RoomID]struct{})
	if s != nil && s.accounts != nil {
		for _, account := range s.accounts.list() {
			if account.SpaceRoom != "" {
				unique[id.RoomID(account.SpaceRoom)] = struct{}{}
			}
		}
	}
	if s != nil && s.portals != nil {
		for _, portal := range s.portals.list() {
			if portal.RoomID != "" {
				unique[portal.RoomID] = struct{}{}
			}
		}
	}
	rooms := make([]id.RoomID, 0, len(unique))
	for room := range unique {
		rooms = append(rooms, room)
	}
	sort.Slice(rooms, func(i, j int) bool { return rooms[i] < rooms[j] })
	return rooms
}

func (s *Server) reconcileBuiltinEmojiRoomsLocked(ctx context.Context, data builtinEmojiStoreData, rooms []id.RoomID) reconciliationResult {
	rooms = sortedUniqueRooms(rooms)
	result := reconciliationResult{Status: builtinPackStatusSuccess, AttemptedRooms: len(rooms)}
	for _, room := range rooms {
		if err := s.reconcileBuiltinEmojiRoomWithTimeoutLocked(ctx, room, data); err != nil {
			result.FailedRooms++
			result.Warnings = append(result.Warnings, fmt.Sprintf("room %s: %v", room, err))
			continue
		}
		result.ReconciledRooms++
	}
	if result.FailedRooms > 0 {
		if result.ReconciledRooms > 0 {
			result.Status = builtinPackStatusPartial
		} else {
			result.Status = builtinPackStatusFailed
		}
	}
	return result
}

// reconcileBuiltinEmojiPacksLocked reconciles the canonical local store into
// every saved account Space and portal. The caller must hold packMu. It never
// acquires spaceMu and never writes Matrix state back into the local store.
func (s *Server) reconcileBuiltinEmojiPacksLocked(ctx context.Context) reconciliationResult {
	return s.reconcileBuiltinEmojiPacksExcludingLocked(ctx)
}

func (s *Server) reconcileBuiltinEmojiPacksExcludingLocked(ctx context.Context, excludedRooms ...id.RoomID) reconciliationResult {
	data, skipReason := s.builtinEmojiStoreSnapshot()
	if skipReason != "" {
		return reconciliationResult{Status: builtinPackStatusSkipped, Warnings: []string{skipReason}}
	}
	excluded := make(map[id.RoomID]struct{}, len(excludedRooms))
	for _, room := range excludedRooms {
		if room != "" {
			excluded[room] = struct{}{}
		}
	}
	rooms := s.builtinEmojiReconciliationRooms()
	filtered := rooms[:0]
	for _, room := range rooms {
		if _, skip := excluded[room]; !skip {
			filtered = append(filtered, room)
		}
	}
	return s.reconcileBuiltinEmojiRoomsLocked(ctx, data, filtered)
}

func (s *Server) reconcileBuiltinEmojiPacks(ctx context.Context) (reconciliationResult, accountDataVisibilityResult) {
	s.lockImagePack()
	defer s.packMu.Unlock()
	reconciliation := s.reconcileBuiltinEmojiPacksLocked(ctx)
	visibility := s.reconcileBuiltinPackAccountDataLocked(ctx)
	return reconciliation, visibility
}

func (s *Server) bootstrapBuiltinEmojiPortal(ctx context.Context, room id.RoomID) (reconciliationResult, accountDataVisibilityResult) {
	s.lockImagePack()
	defer s.packMu.Unlock()
	data, skipReason := s.builtinEmojiStoreSnapshot()
	if skipReason != "" {
		skipped := reconciliationResult{Status: builtinPackStatusSkipped, Warnings: []string{skipReason}}
		return skipped, accountDataVisibilityResult{Status: builtinPackStatusSkipped, Warnings: []string{skipReason}}
	}
	reconciliation := s.reconcileBuiltinEmojiRoomsLocked(ctx, data, []id.RoomID{room})
	return reconciliation, accountDataVisibilityResult{Status: builtinPackStatusSkipped}
}

func (s *Server) visibilityClient(user id.UserID, token string) (builtinPackVisibilityClient, error) {
	if s.newBuiltinPackVisibilityClient != nil {
		return s.newBuiltinPackVisibilityClient(user, token)
	}
	if s == nil || s.cfg == nil || strings.TrimSpace(s.cfg.Homeserver.Address) == "" {
		return nil, fmt.Errorf("homeserver address is unavailable")
	}
	client, err := mautrix.NewClient(s.cfg.Homeserver.Address, user, token)
	if err != nil {
		return nil, err
	}
	return matrixBuiltinPackVisibilityClient{client: client}, nil
}

func sortedUniqueRooms(rooms []id.RoomID) []id.RoomID {
	unique := make(map[id.RoomID]struct{}, len(rooms))
	for _, room := range rooms {
		if room != "" {
			unique[room] = struct{}{}
		}
	}
	out := make([]id.RoomID, 0, len(unique))
	for room := range unique {
		out = append(out, room)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func (s *Server) builtinEmojiVisibilityCandidates(extraSpaces []id.RoomID) ([]id.RoomID, []id.RoomID) {
	return s.builtinEmojiVisibilityCandidatesExcluding(extraSpaces, nil)
}

func (s *Server) builtinEmojiVisibilityCandidatesExcluding(extraSpaces, excludedRooms []id.RoomID) ([]id.RoomID, []id.RoomID) {
	excluded := make(map[id.RoomID]struct{}, len(excludedRooms))
	for _, room := range excludedRooms {
		if room != "" {
			excluded[room] = struct{}{}
		}
	}
	preparedSpaces := sortedUniqueRooms(extraSpaces)
	preparedSet := make(map[id.RoomID]struct{}, len(preparedSpaces))
	filteredPrepared := preparedSpaces[:0]
	for _, room := range preparedSpaces {
		if _, skip := excluded[room]; skip {
			continue
		}
		preparedSet[room] = struct{}{}
		filteredPrepared = append(filteredPrepared, room)
	}
	preparedSpaces = filteredPrepared
	savedSpaces := make([]id.RoomID, 0)
	if s != nil && s.accounts != nil {
		for _, account := range s.accounts.list() {
			room := id.RoomID(account.SpaceRoom)
			if _, skip := excluded[room]; skip {
				continue
			}
			if _, prepared := preparedSet[room]; !prepared {
				savedSpaces = append(savedSpaces, room)
			}
		}
	}
	spaces := append(preparedSpaces, sortedUniqueRooms(savedSpaces)...)
	portals := make([]id.RoomID, 0)
	if s != nil && s.portals != nil {
		for _, portal := range s.portals.list() {
			if _, skip := excluded[portal.RoomID]; !skip {
				portals = append(portals, portal.RoomID)
			}
		}
	}
	spaceSet := make(map[id.RoomID]struct{}, len(spaces))
	for _, room := range spaces {
		spaceSet[room] = struct{}{}
	}
	portals = sortedUniqueRooms(portals)
	filteredPortals := portals[:0]
	for _, room := range portals {
		if _, duplicate := spaceSet[room]; !duplicate {
			filteredPortals = append(filteredPortals, room)
		}
	}
	return spaces, filteredPortals
}

func packContainsBuiltinStoreImages(raw json.RawMessage, data builtinEmojiStoreData) bool {
	images := rawImagePackImages(raw)
	if len(images) == 0 {
		return false
	}
	for _, expected := range data.Images {
		image, ok := images[expected.Shortcode]
		if !ok {
			return false
		}
		var content struct {
			URL string `json:"url"`
		}
		if json.Unmarshal(image, &content) != nil || content.URL != expected.MXC {
			return false
		}
	}
	return true
}

func (s *Server) roomContainsBuiltinPacksLocked(ctx context.Context, room id.RoomID, data builtinEmojiStoreData) (bool, error) {
	stable, err := s.packState().Load(ctx, room, event.StateImagePack, builtinEmojiStateKey)
	if errors.Is(err, errImagePackStateNotFound) {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("load %s: %w", event.StateImagePack.Type, err)
	}
	if !packContainsBuiltinStoreImages(stable, data) {
		return false, nil
	}
	compat, err := s.packState().Load(ctx, room, event.StateUnstableImagePack, "")
	if errors.Is(err, errImagePackStateNotFound) {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("load %s: %w", event.StateUnstableImagePack.Type, err)
	}
	return packContainsBuiltinStoreImages(compat, data), nil
}

func removeImagePackRoomStateKey(content *event.ImagePackRoomsEventContent, stateKey string) {
	if content.Rooms == nil {
		content.Rooms = map[id.RoomID]map[string]struct{}{}
		return
	}
	for room, keys := range content.Rooms {
		delete(keys, stateKey)
		if len(keys) == 0 {
			delete(content.Rooms, room)
		}
	}
}

func mergeAccountDataVisibilityResults(initial, final accountDataVisibilityResult) accountDataVisibilityResult {
	final.Writes += initial.Writes
	final.Warnings = append(append([]string(nil), initial.Warnings...), final.Warnings...)
	if final.User == "" {
		final.User = initial.User
	}
	if final.Room == "" && final.Status == builtinPackStatusSuccess {
		final.Room = initial.Room
	}
	return final
}

func (s *Server) registerBuiltinPackAccountDataLocked(ctx context.Context) accountDataVisibilityResult {
	result, transaction := s.registerBuiltinPackAccountDataForSpacesTransactionalLocked(ctx, nil)
	defer s.finishBuiltinPackAccountDataTransactionLocked(&transaction)
	return result
}

func (s *Server) registerBuiltinPackAccountDataForSpacesLocked(ctx context.Context, extraSpaces []id.RoomID) accountDataVisibilityResult {
	result, transaction := s.registerBuiltinPackAccountDataForSpacesTransactionalLocked(ctx, extraSpaces)
	defer s.finishBuiltinPackAccountDataTransactionLocked(&transaction)
	return result
}

func cloneImagePackRooms(content event.ImagePackRoomsEventContent) event.ImagePackRoomsEventContent {
	if content.Rooms == nil {
		return event.ImagePackRoomsEventContent{}
	}
	clone := event.ImagePackRoomsEventContent{Rooms: make(map[id.RoomID]map[string]struct{}, len(content.Rooms))}
	for room, keys := range content.Rooms {
		clonedKeys := make(map[string]struct{}, len(keys))
		for key := range keys {
			clonedKeys[key] = struct{}{}
		}
		clone.Rooms[room] = clonedKeys
	}
	return clone
}

func (s *Server) registerBuiltinPackAccountDataForSpacesTransactionalLocked(ctx context.Context, extraSpaces []id.RoomID) (accountDataVisibilityResult, accountDataWriteTransaction) {
	return s.registerBuiltinPackAccountDataForSpacesTransactionalExcludingLocked(ctx, extraSpaces, nil)
}

func (s *Server) registerBuiltinPackAccountDataForSpacesTransactionalExcludingLocked(ctx context.Context, extraSpaces, excludedRooms []id.RoomID) (accountDataVisibilityResult, accountDataWriteTransaction) {
	s.lockAccountData()
	transaction := accountDataWriteTransaction{accountDataLock: true}
	returned := false
	defer func() {
		if !returned {
			s.finishBuiltinPackAccountDataTransactionLocked(&transaction)
		}
	}()
	result := s.registerBuiltinPackAccountDataForSpacesTransactionAccountDataLocked(ctx, extraSpaces, excludedRooms, &transaction)
	returned = true
	return result, transaction
}

// registerBuiltinPackAccountDataForSpacesTransactionAccountDataLocked performs
// the account-data RMW while packMu and accountDataMu are held by the caller.
func (s *Server) registerBuiltinPackAccountDataForSpacesTransactionAccountDataLocked(ctx context.Context, extraSpaces, excludedRooms []id.RoomID, transaction *accountDataWriteTransaction) accountDataVisibilityResult {
	result := accountDataVisibilityResult{Status: builtinPackStatusSkipped}
	data, skipReason := s.builtinEmojiStoreSnapshot()
	if skipReason != "" {
		result.Warnings = []string{skipReason}
		return result
	}
	if s == nil || s.cfg == nil {
		result.Warnings = []string{"personal sticker credentials are not configured"}
		return result
	}
	user := id.UserID(strings.TrimSpace(s.cfg.Bridge.PersonalStickerUserID))
	token := strings.TrimSpace(s.cfg.Bridge.PersonalStickerAccessToken)
	result.User = user
	if user == "" || token == "" {
		result.Warnings = []string{"personal sticker user ID and access token are both required"}
		return result
	}
	client, err := s.visibilityClient(user, token)
	if err != nil {
		result.Status = builtinPackStatusFailed
		result.Warnings = []string{fmt.Sprintf("create personal-token Matrix client: %v", err)}
		return result
	}
	transaction.client = client

	spaces, portals := s.builtinEmojiVisibilityCandidatesExcluding(extraSpaces, excludedRooms)
	candidates := append(append([]id.RoomID(nil), spaces...), portals...)
	for _, room := range candidates {
		loadCtx, cancelLoad := s.matrixOperationContext(ctx)
		contains, loadErr := s.roomContainsBuiltinPacksLocked(loadCtx, room, data)
		cancelLoad()
		if loadErr != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("room %s built-in pack load: %v", room, loadErr))
			continue
		}
		if !contains {
			continue
		}
		membershipCtx, cancelMembership := s.matrixOperationContext(ctx)
		membership, memberErr := client.Membership(membershipCtx, room, user)
		cancelMembership()
		if memberErr != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("verify membership in %s: %v", room, memberErr))
			continue
		}
		if membership != event.MembershipJoin {
			continue
		}
		result.Room = room
		break
	}
	if result.Room == "" {
		result.Warnings = append(result.Warnings, "no joined room contains both built-in emoji pack states")
		return result
	}

	targets := imagePackAccountDataTargets(builtinEmojiStateKey)
	failed := false
	for _, target := range targets {
		var content event.ImagePackRoomsEventContent
		getCtx, cancelGet := s.matrixOperationContext(ctx)
		getErr := client.GetAccountData(getCtx, target.EventType, &content)
		cancelGet()
		if getErr != nil && !errors.Is(getErr, mautrix.MNotFound) {
			failed = true
			result.Warnings = append(result.Warnings, fmt.Sprintf("read account data %s: %v", target.EventType, getErr))
			continue
		}
		preimage := cloneImagePackRooms(content)
		removeImagePackRoomStateKey(&content, builtinEmojiStateKey)
		addImagePackRoomSource(&content, result.Room, target.StateKey)
		// Record the preimage before SetAccountData because a timeout/error may
		// still mean the homeserver applied the write.
		transaction.preimages = append(transaction.preimages, accountDataPreimage{EventType: target.EventType, Content: preimage})
		setCtx, cancelSet := s.matrixOperationContext(ctx)
		setErr := client.SetAccountData(setCtx, target.EventType, &content)
		cancelSet()
		if setErr != nil {
			failed = true
			result.Warnings = append(result.Warnings, fmt.Sprintf("write account data %s: %v", target.EventType, setErr))
			continue
		}
		result.Writes++
	}
	if failed || result.Writes != len(targets) {
		result.Status = builtinPackStatusFailed
	} else {
		result.Status = builtinPackStatusSuccess
	}
	return result
}

func removeImagePackRoomSource(content *event.ImagePackRoomsEventContent, room id.RoomID, stateKey string) bool {
	if content.Rooms == nil || content.Rooms[room] == nil {
		return false
	}
	if _, ok := content.Rooms[room][stateKey]; !ok {
		return false
	}
	delete(content.Rooms[room], stateKey)
	if len(content.Rooms[room]) == 0 {
		delete(content.Rooms, room)
	}
	return true
}

// retireBuiltinPackAccountDataSourceLocked requires packMu and accountDataMu.
// It returns true only when both stable and compatibility documents were read
// and any required removals were accepted by Matrix.
func (s *Server) retireBuiltinPackAccountDataSourceLocked(ctx context.Context, retired id.RoomID, result *accountDataVisibilityResult, transaction *accountDataWriteTransaction) bool {
	if retired == "" {
		return true
	}
	if result == nil || transaction == nil || transaction.client == nil {
		if result != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("cannot retire Space %s without a personal-token account-data client", retired))
		}
		return false
	}
	removed := 0
	failed := false
	for _, target := range imagePackAccountDataTargets(builtinEmojiStateKey) {
		var content event.ImagePackRoomsEventContent
		getCtx, cancelGet := s.matrixOperationContext(ctx)
		getErr := transaction.client.GetAccountData(getCtx, target.EventType, &content)
		cancelGet()
		if getErr != nil && !errors.Is(getErr, mautrix.MNotFound) {
			failed = true
			result.Warnings = append(result.Warnings, fmt.Sprintf("read retired Space %s account data %s: %v", retired, target.EventType, getErr))
			continue
		}
		if !removeImagePackRoomSource(&content, retired, target.StateKey) {
			continue
		}
		preimage := cloneImagePackRooms(content)
		addImagePackRoomSource(&preimage, retired, target.StateKey)
		// Record retirement attempts before SetAccountData for ambiguous errors.
		transaction.preimages = append(transaction.preimages, accountDataPreimage{EventType: target.EventType, Content: preimage})
		setCtx, cancelSet := s.matrixOperationContext(ctx)
		setErr := transaction.client.SetAccountData(setCtx, target.EventType, &content)
		cancelSet()
		if setErr != nil {
			failed = true
			result.Warnings = append(result.Warnings, fmt.Sprintf("remove retired Space %s from account data %s: %v", retired, target.EventType, setErr))
			continue
		}
		removed++
		result.Writes++
	}
	if removed > 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("removed retired Space %s from %d built-in emoji account-data documents", retired, removed))
	}
	if failed {
		result.Status = builtinPackStatusFailed
	}
	return !failed
}

func (s *Server) pendingRetiredSpaceRooms() []id.RoomID {
	if s == nil || s.accounts == nil {
		return nil
	}
	rooms := make([]id.RoomID, 0)
	for _, account := range s.accounts.list() {
		for _, room := range account.RetiredSpaceRooms {
			rooms = append(rooms, id.RoomID(room))
		}
	}
	return sortedUniqueRooms(rooms)
}

func (s *Server) reconcileBuiltinPackAccountDataLocked(ctx context.Context) accountDataVisibilityResult {
	result, transaction := s.registerBuiltinPackAccountDataForSpacesTransactionalLocked(ctx, nil)
	defer s.finishBuiltinPackAccountDataTransactionLocked(&transaction)
	retired := s.pendingRetiredSpaceRooms()
	cleared := make([]string, 0, len(retired))
	for _, room := range retired {
		if s.retireBuiltinPackAccountDataSourceLocked(ctx, room, &result, &transaction) {
			cleared = append(cleared, room.String())
		}
	}
	if len(cleared) > 0 {
		if err := s.accounts.clearRetiredSpaceRooms(cleared); err != nil {
			result.Status = builtinPackStatusFailed
			result.Warnings = append(result.Warnings, fmt.Sprintf("persist cleared retired Space account-data sources %v: %v", cleared, err))
		}
	}
	return result
}

func (s *Server) finishBuiltinPackAccountDataTransactionLocked(transaction *accountDataWriteTransaction) {
	if transaction == nil || !transaction.accountDataLock {
		return
	}
	transaction.accountDataLock = false
	s.accountDataMu.Unlock()
}

func (s *Server) rollbackBuiltinPackAccountDataLocked(ctx context.Context, transaction *accountDataWriteTransaction) []error {
	if transaction == nil {
		return nil
	}
	defer s.finishBuiltinPackAccountDataTransactionLocked(transaction)
	if transaction.client == nil || len(transaction.preimages) == 0 {
		return nil
	}
	failures := make([]error, 0)
	for index := len(transaction.preimages) - 1; index >= 0; index-- {
		preimage := transaction.preimages[index]
		rollbackCtx, cancelRollback := s.matrixOperationContext(context.WithoutCancel(ctx))
		err := transaction.client.SetAccountData(rollbackCtx, preimage.EventType, &preimage.Content)
		cancelRollback()
		if err != nil {
			failures = append(failures, fmt.Errorf("rollback account data %s: %w", preimage.EventType, err))
		}
		var restored event.ImagePackRoomsEventContent
		verifyCtx, cancelVerify := s.matrixOperationContext(context.WithoutCancel(ctx))
		verifyErr := transaction.client.GetAccountData(verifyCtx, preimage.EventType, &restored)
		cancelVerify()
		if verifyErr != nil {
			failures = append(failures, fmt.Errorf("verify rollback account data %s: %w", preimage.EventType, verifyErr))
		} else if !reflect.DeepEqual(cloneImagePackRooms(restored), cloneImagePackRooms(preimage.Content)) {
			failures = append(failures, fmt.Errorf("verify rollback account data %s: restored content does not match preimage", preimage.EventType))
		}
	}
	return failures
}
