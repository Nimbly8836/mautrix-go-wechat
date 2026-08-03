package matrixas

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

type wechatStickerImage struct {
	Shortcode string
	Body      string
	URL       id.ContentURIString
}

func parseStickerAddArgs(rest string) (wechatStickerImage, error) {
	fields := strings.Fields(strings.TrimSpace(rest))
	if len(fields) < 2 {
		return wechatStickerImage{}, fmt.Errorf("用法: stickers add <shortcode> <mxc-or-image-url> [label]")
	}
	shortcode := normalizeStickerShortcode(fields[0])
	if shortcode == "" {
		return wechatStickerImage{}, fmt.Errorf("shortcode 不能为空")
	}
	body := shortcode
	if len(fields) > 2 {
		body = strings.Join(fields[2:], " ")
	}
	return wechatStickerImage{Shortcode: shortcode, Body: body, URL: id.ContentURIString(fields[1])}, nil
}

func normalizeStickerShortcode(shortcode string) string {
	shortcode = strings.TrimSpace(shortcode)
	shortcode = strings.Trim(shortcode, ":")
	shortcode = strings.ToLower(shortcode)
	shortcode = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			return r
		}
		return '_'
	}, shortcode)
	return strings.Trim(shortcode, "_")
}

func wechatStickerPackStateKey(account remoteapi.UserID) string {
	return "wechat." + string(account)
}

func buildWechatStickerPackContent(account remoteapi.UserID, stickers []wechatStickerImage) *event.ImagePackEventContent {
	images := make(map[string]*event.ImagePackImage, len(stickers))
	sorted := append([]wechatStickerImage(nil), stickers...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Shortcode < sorted[j].Shortcode })
	for _, sticker := range sorted {
		shortcode := normalizeStickerShortcode(sticker.Shortcode)
		if shortcode == "" || sticker.URL == "" {
			continue
		}
		body := strings.TrimSpace(sticker.Body)
		if body == "" {
			body = shortcode
		}
		images[shortcode] = &event.ImagePackImage{URL: sticker.URL, Body: body}
	}
	return &event.ImagePackEventContent{
		Images: images,
		Metadata: event.ImagePackMetadata{
			DisplayName: fmt.Sprintf("WeChat %s stickers", account),
			Usage:       []event.ImagePackUsage{event.ImagePackUsageSticker, event.ImagePackUsageEmoji},
			Attribution: "Bridged from WeChat",
			BridgedPack: &event.BridgedStickerPack{Network: "wechat", URL: string(account)},
		},
	}
}

func mergeImagePackContent(dst, src *event.ImagePackEventContent) int {
	if dst == nil || src == nil || len(src.Images) == 0 {
		return 0
	}
	if dst.Images == nil {
		dst.Images = map[string]*event.ImagePackImage{}
	}
	added := 0
	for shortcode, image := range src.Images {
		shortcode = normalizeStickerShortcode(shortcode)
		if shortcode == "" || image == nil || image.URL == "" {
			continue
		}
		if _, exists := dst.Images[shortcode]; exists {
			continue
		}
		copyImage := *image
		dst.Images[shortcode] = &copyImage
		added++
	}
	return added
}

func (s *Server) handleStickersCommand(ctx context.Context, room id.RoomID, sender id.UserID, rest string) {
	fields := strings.Fields(rest)
	if len(fields) == 0 || fields[0] == "help" || fields[0] == "帮助" {
		s.notice(ctx, room, fmt.Sprintf("用法:\n%s stickers add <shortcode> <mxc-or-image-url> [label]\n%s stickers status\n%s stickers repair\n%s stickers import wechat --ack-no-license\n\n会把图片加入当前微信账号 Space，并同步 Element 兼容 pack 到 portal rooms。WeChat 内置表情源没有明确 license；导入会 copy/上传图片，确认风险不会授予任何权利。", s.commandPrefix(), s.commandPrefix(), s.commandPrefix(), s.commandPrefix()))
		return
	}
	if fields[0] == "import" || fields[0] == "导入" {
		s.handleBuiltinEmojiImportCommand(ctx, room, sender, rest)
		return
	}
	account := s.currentAccountID(ctx)
	if account == "" {
		s.notice(ctx, room, fmt.Sprintf("当前没有选择/登录微信账号。先使用 `%s account <wxid>` 或 `%s login <flow>`。", s.commandPrefix(), s.commandPrefix()))
		return
	}
	if fields[0] == "status" || fields[0] == "状态" {
		s.notice(ctx, room, s.stickerStatusText(ctx, account))
		return
	}
	if fields[0] == "repair" || fields[0] == "修复" {
		space, _, err := s.ensureAccountSpaceVisibleTo(ctx, account, sender)
		if err != nil {
			s.notice(ctx, room, fmt.Sprintf("创建/获取 Space 失败: %v", err))
			return
		}
		count := s.repairStickerPacksForAccount(ctx, account, space, sender)
		s.notice(ctx, room, fmt.Sprintf("已修复 sticker pack。当前合并后 pack: %d 个。\n%s", count, s.stickerStatusText(ctx, account)))
		return
	}
	if fields[0] != "add" && fields[0] != "添加" {
		s.notice(ctx, room, fmt.Sprintf("未知 stickers 子命令。发送 `%s stickers help` 查看用法。", s.commandPrefix()))
		return
	}
	sticker, err := parseStickerAddArgs(strings.TrimSpace(strings.TrimPrefix(rest, fields[0])))
	if err != nil {
		s.notice(ctx, room, err.Error())
		return
	}
	if mxc, err := s.resolveStickerMXC(ctx, string(sticker.URL)); err != nil {
		s.notice(ctx, room, fmt.Sprintf("上传/解析 sticker 图片失败: %v", err))
		return
	} else {
		sticker.URL = mxc
	}
	s.portals.setOwner(sender)
	space, _, err := s.ensureAccountSpaceVisibleTo(ctx, account, sender)
	if err != nil {
		s.notice(ctx, room, fmt.Sprintf("创建/获取 Space 失败: %v", err))
		return
	}
	count, err := s.addStickerToAccountSpace(ctx, account, space, sticker)
	if err != nil {
		s.notice(ctx, room, fmt.Sprintf("写入 Space sticker pack 失败: %v", err))
		return
	}
	mirrored := s.syncStickerToElementPortals(ctx, account, sticker)
	s.notice(ctx, room, fmt.Sprintf("已添加 sticker `:%s:` 到微信账号 Space %s。当前 pack 共 %d 个；已同步 Element portal pack: %d 个房间。", sticker.Shortcode, space, count, mirrored))
}

func (s *Server) stickerStatusText(ctx context.Context, account remoteapi.UserID) string {
	if s == nil {
		return "Sticker 状态: unavailable"
	}
	s.lockImagePack()
	defer s.packMu.Unlock()
	return s.stickerStatusTextLocked(ctx, account)
}

func (s *Server) stickerStatusTextLocked(ctx context.Context, account remoteapi.UserID) string {
	stateKey := wechatStickerPackStateKey(account)
	space := id.RoomID("")
	if s.accounts != nil {
		if acc, ok := s.accounts.get(account); ok {
			space = id.RoomID(acc.SpaceRoom)
		}
	}
	spaceCount := s.imagePackCountLocked(ctx, space, event.StateImagePack, stateKey)
	legacySpaceCount := s.imagePackCountLocked(ctx, space, event.StateUnstableImagePack, "")
	portalRooms := 0
	portalLegacyWithPack := 0
	portalLegacyTotal := 0
	if s.portals != nil {
		for _, portal := range s.portals.list() {
			if portal.RoomID == "" {
				continue
			}
			portalRooms++
			count := s.imagePackCountLocked(ctx, portal.RoomID, event.StateUnstableImagePack, "")
			if count > 0 {
				portalLegacyWithPack++
				portalLegacyTotal += count
			}
		}
	}
	owner := id.UserID("")
	if s.portals != nil {
		owner = s.portals.owner()
	}
	return fmt.Sprintf("Sticker 状态:\n- account: %s\n- owner: %s\n- Space: %s\n- Space m.room.image_pack[%s]: %d\n- Space im.ponies.room_emotes['']: %d\n- portal rooms: %d\n- portal Element legacy pack: %d/%d rooms, total images=%d", account, owner, space, stateKey, spaceCount, legacySpaceCount, portalRooms, portalLegacyWithPack, portalRooms, portalLegacyTotal)
}

func (s *Server) imagePackCount(ctx context.Context, room id.RoomID, eventType event.Type, stateKey string) int {
	if s == nil {
		return 0
	}
	s.lockImagePack()
	defer s.packMu.Unlock()
	return s.imagePackCountLocked(ctx, room, eventType, stateKey)
}

func (s *Server) imagePackCountLocked(ctx context.Context, room id.RoomID, eventType event.Type, stateKey string) int {
	if room == "" {
		return 0
	}
	raw, err := s.packState().Load(ctx, room, eventType, stateKey)
	if err != nil {
		return 0
	}
	return len(parsePackImages(raw))
}

func (s *Server) syncStickerToElementPortals(ctx context.Context, account remoteapi.UserID, sticker wechatStickerImage) int {
	if s == nil {
		return 0
	}
	s.lockImagePack()
	defer s.packMu.Unlock()
	return s.syncStickerToElementPortalsLocked(ctx, account, sticker)
}

func (s *Server) syncStickerToElementPortalsLocked(ctx context.Context, account remoteapi.UserID, sticker wechatStickerImage) int {
	if s.portals == nil {
		return 0
	}
	count := 0
	for _, portal := range s.portals.list() {
		if portal.RoomID == "" {
			continue
		}
		s.addStickerToElementPortalLocked(ctx, account, portal.RoomID, sticker)
		count++
	}
	return count
}

func (s *Server) resolveStickerMXC(ctx context.Context, source string) (id.ContentURIString, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return "", fmt.Errorf("图片 URL 不能为空")
	}
	if strings.HasPrefix(source, "mxc://") {
		return id.ContentURIString(source), nil
	}
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		return s.roomAvatarFromURL(ctx, source)
	}
	return "", fmt.Errorf("只支持 mxc://、http:// 或 https:// 图片 URL")
}

type imagePackAccountDataTarget struct {
	EventType string
	StateKey  string
	Aliases   []string
}

type imagePackStateEventTarget struct {
	EventType event.Type
	StateKey  string
	Aliases   []string
}

func imagePackAccountDataTypes() []string {
	return []string{event.AccountDataImagePackRooms.Type, event.AccountDataUnstableImagePackRooms.Type}
}

func imagePackAccountDataTargets(stateKey string) []imagePackAccountDataTarget {
	return []imagePackAccountDataTarget{
		{EventType: event.AccountDataImagePackRooms.Type, StateKey: stateKey, Aliases: []string{stateKey}},
		{EventType: event.AccountDataUnstableImagePackRooms.Type, StateKey: "", Aliases: []string{stateKey, ""}},
	}
}

func imagePackStateEventTypes() []event.Type {
	return []event.Type{event.StateImagePack, event.StateUnstableImagePack}
}

func imagePackStateEventTargets(stateKey string) []imagePackStateEventTarget {
	return []imagePackStateEventTarget{
		{EventType: event.StateImagePack, StateKey: stateKey, Aliases: []string{stateKey}},
		{EventType: event.StateUnstableImagePack, StateKey: "", Aliases: []string{stateKey, ""}},
	}
}

func imagePackPortalMirrorTargets() []imagePackStateEventTarget {
	return []imagePackStateEventTarget{{EventType: event.StateUnstableImagePack, StateKey: "", Aliases: []string{""}}}
}

func addImagePackRoomSource(content *event.ImagePackRoomsEventContent, room id.RoomID, stateKey string) {
	if content.Rooms == nil {
		content.Rooms = map[id.RoomID]map[string]struct{}{}
	}
	if content.Rooms[room] == nil {
		content.Rooms[room] = map[string]struct{}{}
	}
	content.Rooms[room][stateKey] = struct{}{}
}

func setSingleImagePackRoomSource(content *event.ImagePackRoomsEventContent, room id.RoomID, stateKey string) {
	setSingleImagePackRoomSourceAliases(content, room, stateKey, stateKey)
}

func setSingleImagePackRoomSourceAliases(content *event.ImagePackRoomsEventContent, room id.RoomID, stateKey string, aliases ...string) {
	if content.Rooms == nil {
		content.Rooms = map[id.RoomID]map[string]struct{}{}
	}
	if len(aliases) == 0 {
		aliases = []string{stateKey}
	}
	for existingRoom, keys := range content.Rooms {
		for _, alias := range aliases {
			delete(keys, alias)
		}
		if len(keys) == 0 {
			delete(content.Rooms, existingRoom)
		}
	}
	addImagePackRoomSource(content, room, stateKey)
}

func (s *Server) personalStickerAccessTokenForUser(user id.UserID) string {
	if s == nil || s.cfg == nil || s.cfg.Bridge.PersonalStickerAccessToken == "" {
		return ""
	}
	configuredUser := id.UserID(strings.TrimSpace(s.cfg.Bridge.PersonalStickerUserID))
	if configuredUser == "" || configuredUser == user {
		return strings.TrimSpace(s.cfg.Bridge.PersonalStickerAccessToken)
	}
	return ""
}

func (s *Server) imagePackAccountDataClient(user id.UserID) (*mautrix.Client, error) {
	if token := s.personalStickerAccessTokenForUser(user); token != "" {
		return mautrix.NewClient(s.cfg.Homeserver.Address, user, token)
	}
	if s == nil || s.as == nil {
		return nil, fmt.Errorf("appservice unavailable")
	}
	intent := s.as.Intent(user)
	if intent == nil || intent.Client == nil {
		return nil, fmt.Errorf("no Matrix intent")
	}
	return intent.Client, nil
}

func (s *Server) ensureUserSeesImagePackRoom(ctx context.Context, user id.UserID, room id.RoomID, stateKey string) {
	if user == "" || room == "" || stateKey == "" || s == nil {
		return
	}
	client, err := s.imagePackAccountDataClient(user)
	if err != nil {
		log.Printf("cannot register image pack room for %s: %v", user, err)
		return
	}
	s.lockAccountData()
	defer s.accountDataMu.Unlock()
	for _, target := range imagePackAccountDataTargets(stateKey) {
		var content event.ImagePackRoomsEventContent
		getCtx, cancelGet := s.matrixOperationContext(ctx)
		getErr := client.GetAccountData(getCtx, target.EventType, &content)
		cancelGet()
		if getErr != nil {
			log.Printf("creating image pack rooms account data type=%s for %s: %v", target.EventType, user, getErr)
		}
		setSingleImagePackRoomSourceAliases(&content, room, target.StateKey, target.Aliases...)
		setCtx, cancelSet := s.matrixOperationContext(ctx)
		setErr := client.SetAccountData(setCtx, target.EventType, &content)
		cancelSet()
		if setErr != nil {
			log.Printf("failed to register image pack room type=%s for %s room=%s state_key=%s: %v", target.EventType, user, room, target.StateKey, setErr)
		} else {
			log.Printf("registered image pack room type=%s for %s room=%s state_key=%s", target.EventType, user, room, target.StateKey)
		}
	}
}

func (s *Server) lockImagePack() {
	if s.beforePackLock != nil {
		s.beforePackLock()
	}
	s.packMu.Lock()
}

func (s *Server) lockAccountData() {
	if s.beforeAccountDataLock != nil {
		s.beforeAccountDataLock()
	}
	s.accountDataMu.Lock()
}

func (s *Server) packState() rawImagePackState {
	if s.rawImagePackState != nil {
		return s.rawImagePackState
	}
	return matrixRawImagePackState{server: s}
}

func (s *Server) baseStickerPackRaw(account remoteapi.UserID) json.RawMessage {
	raw, err := json.Marshal(buildWechatStickerPackContent(account, nil))
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}

func rawJSONObject(raw json.RawMessage) bool {
	var object map[string]json.RawMessage
	return json.Unmarshal(raw, &object) == nil && object != nil
}

func (s *Server) loadStickerPackOrBaseLocked(ctx context.Context, account remoteapi.UserID, room id.RoomID, eventType event.Type, stateKey string) (json.RawMessage, error) {
	raw, err := s.packState().Load(ctx, room, eventType, stateKey)
	if errors.Is(err, errImagePackStateNotFound) {
		return s.initialStickerPackRaw(account, eventType, stateKey), nil
	} else if err != nil {
		return nil, fmt.Errorf("load image pack type=%s room=%s state_key=%s: %w", eventType.Type, room, stateKey, err)
	} else if !rawJSONObject(raw) {
		return s.initialStickerPackRaw(account, eventType, stateKey), nil
	}
	return raw, nil
}

func (s *Server) initialStickerPackRaw(account remoteapi.UserID, eventType event.Type, stateKey string) json.RawMessage {
	// The shared Element compatibility event must contain only images. Account-
	// specific metadata belongs exclusively to the namespaced stable pack.
	if eventType == event.StateUnstableImagePack && stateKey == "" {
		return json.RawMessage(`{}`)
	}
	return s.baseStickerPackRaw(account)
}

func rawPackImageObjects(raw json.RawMessage) map[string]json.RawMessage {
	var content struct {
		Images map[string]json.RawMessage `json:"images"`
	}
	if json.Unmarshal(raw, &content) != nil {
		return map[string]json.RawMessage{}
	}
	images := make(map[string]json.RawMessage, len(content.Images))
	for shortcode, rawImage := range content.Images {
		var image struct {
			URL string `json:"url"`
		}
		if json.Unmarshal(rawImage, &image) != nil {
			continue
		}
		if _, ok := validMXC(image.URL); !ok {
			continue
		}
		shortcode = normalizeStickerShortcode(shortcode)
		if shortcode == "" {
			continue
		}
		images[shortcode] = append(json.RawMessage(nil), rawImage...)
	}
	return images
}

func stickerImageRaw(sticker wechatStickerImage) (string, json.RawMessage, error) {
	shortcode := normalizeStickerShortcode(sticker.Shortcode)
	if shortcode == "" {
		return "", nil, fmt.Errorf("empty sticker shortcode")
	}
	if _, ok := validMXC(string(sticker.URL)); !ok {
		return "", nil, fmt.Errorf("invalid sticker MXC URL %q", sticker.URL)
	}
	body := strings.TrimSpace(sticker.Body)
	if body == "" {
		body = shortcode
	}
	raw, err := json.Marshal(&event.ImagePackImage{URL: sticker.URL, Body: body})
	return shortcode, raw, err
}

func mergeRawPackImageSets(dst map[string]json.RawMessage, raw json.RawMessage) {
	for shortcode, image := range rawPackImageObjects(raw) {
		if _, exists := dst[shortcode]; !exists {
			dst[shortcode] = image
		}
	}
}

func (s *Server) savePackImageReplacementsLocked(ctx context.Context, account remoteapi.UserID, room id.RoomID, target imagePackStateEventTarget, replacements map[string]json.RawMessage) (json.RawMessage, error) {
	raw, err := s.loadStickerPackOrBaseLocked(ctx, account, room, target.EventType, target.StateKey)
	if err != nil {
		return nil, err
	}
	merged, err := mergePackImages(raw, replacements)
	if err != nil {
		return nil, err
	}
	if err = s.packState().Save(ctx, room, target.EventType, target.StateKey, merged); err != nil {
		return nil, err
	}
	return merged, nil
}

func (s *Server) repairStickerPacksForAccount(ctx context.Context, account remoteapi.UserID, space id.RoomID, owner id.UserID) int {
	if s == nil {
		return 0
	}
	operationCtx, cancel := s.matrixOperationContext(ctx)
	defer cancel()
	s.lockImagePack()
	count := s.repairStickerPacksForAccountLocked(operationCtx, account, space)
	s.packMu.Unlock()
	if count > 0 && owner != "" {
		s.ensureUserSeesImagePackRoom(ctx, owner, space, wechatStickerPackStateKey(account))
	}
	return count
}

func (s *Server) repairStickerPacksForAccountLocked(ctx context.Context, account remoteapi.UserID, space id.RoomID) int {
	if account == "" || space == "" {
		return 0
	}
	stateKey := wechatStickerPackStateKey(account)
	replacements := make(map[string]json.RawMessage)
	for _, target := range imagePackStateEventTargets(stateKey) {
		if raw, err := s.packState().Load(ctx, space, target.EventType, target.StateKey); err == nil {
			mergeRawPackImageSets(replacements, raw)
		}
	}
	if s.as != nil && s.as.BotIntent() != nil && s.as.BotIntent().Client != nil {
		joined, err := s.as.BotIntent().Client.JoinedRooms(ctx)
		if err != nil {
			log.Printf("failed to list joined rooms for sticker pack repair account=%s: %v", account, err)
		} else {
			for _, room := range joined.JoinedRooms {
				for _, target := range imagePackStateEventTargets(stateKey) {
					for _, alias := range target.Aliases {
						if raw, loadErr := s.packState().Load(ctx, room, target.EventType, alias); loadErr == nil {
							mergeRawPackImageSets(replacements, raw)
						}
					}
				}
			}
		}
	}
	if len(replacements) == 0 {
		return 0
	}
	if _, err := s.patchStickerPackImagesToRoomLocked(ctx, account, space, replacements); err != nil {
		log.Printf("failed to write repaired account sticker pack account=%s space=%s: %v", account, space, err)
		return 0
	}
	if s.portals != nil {
		for _, portal := range s.portals.list() {
			s.mirrorStickerPackToElementPortalLocked(ctx, account, space, portal.RoomID)
		}
	}
	return len(replacements)
}

func (s *Server) repairStickerPackCompatibility(ctx context.Context, account remoteapi.UserID, room id.RoomID, owner id.UserID) {
	if s == nil {
		return
	}
	operationCtx, cancel := s.matrixOperationContext(ctx)
	defer cancel()
	s.lockImagePack()
	repaired := s.repairStickerPackCompatibilityLocked(operationCtx, account, room)
	s.packMu.Unlock()
	if repaired && owner != "" {
		s.ensureUserSeesImagePackRoom(ctx, owner, room, wechatStickerPackStateKey(account))
	}
}

func (s *Server) repairStickerPackCompatibilityLocked(ctx context.Context, account remoteapi.UserID, room id.RoomID) bool {
	if account == "" || room == "" {
		return false
	}
	raw, err := s.packState().Load(ctx, room, event.StateImagePack, wechatStickerPackStateKey(account))
	if err != nil {
		return false
	}
	replacements := rawPackImageObjects(raw)
	if len(replacements) == 0 {
		return false
	}
	if _, err = s.patchStickerPackImagesToRoomLocked(ctx, account, room, replacements); err != nil {
		log.Printf("failed to repair sticker pack compatibility account=%s room=%s: %v", account, room, err)
		return false
	}
	return true
}

func (s *Server) mirrorStickerPackToElementPortal(ctx context.Context, account remoteapi.UserID, source, portal id.RoomID) {
	if s == nil {
		return
	}
	s.lockImagePack()
	defer s.packMu.Unlock()
	s.mirrorStickerPackToElementPortalLocked(ctx, account, source, portal)
}

func (s *Server) mirrorStickerPackToElementPortalLocked(ctx context.Context, account remoteapi.UserID, source, portal id.RoomID) {
	if account == "" || source == "" || portal == "" || source == portal {
		return
	}
	raw, err := s.packState().Load(ctx, source, event.StateImagePack, wechatStickerPackStateKey(account))
	if err != nil {
		return
	}
	replacements := rawPackImageObjects(raw)
	if len(replacements) == 0 {
		return
	}
	for _, target := range imagePackPortalMirrorTargets() {
		if _, err = s.savePackImageReplacementsLocked(ctx, account, portal, target, replacements); err != nil {
			log.Printf("failed to mirror sticker pack to Element portal type=%s account=%s portal=%s state_key=%s: %v", target.EventType.Type, account, portal, target.StateKey, err)
		}
	}
}

func (s *Server) addStickerToElementPortal(ctx context.Context, account remoteapi.UserID, portal id.RoomID, sticker wechatStickerImage) {
	if s == nil {
		return
	}
	s.lockImagePack()
	defer s.packMu.Unlock()
	s.addStickerToElementPortalLocked(ctx, account, portal, sticker)
}

func (s *Server) addStickerToElementPortalLocked(ctx context.Context, account remoteapi.UserID, portal id.RoomID, sticker wechatStickerImage) {
	if account == "" || portal == "" {
		return
	}
	shortcode, rawImage, err := stickerImageRaw(sticker)
	if err != nil {
		log.Printf("failed to encode sticker for Element portal account=%s portal=%s: %v", account, portal, err)
		return
	}
	for _, target := range imagePackPortalMirrorTargets() {
		merged, saveErr := s.savePackImageReplacementsLocked(ctx, account, portal, target, map[string]json.RawMessage{shortcode: rawImage})
		if saveErr != nil {
			log.Printf("failed to add sticker to Element portal pack type=%s account=%s portal=%s state_key=%s: %v", target.EventType.Type, account, portal, target.StateKey, saveErr)
		} else {
			log.Printf("added sticker to Element portal pack type=%s account=%s portal=%s state_key=%s count=%d shortcode=%s", target.EventType.Type, account, portal, target.StateKey, len(parsePackImages(merged)), shortcode)
		}
	}
}

func (s *Server) removeStickerPackFromRoom(ctx context.Context, account remoteapi.UserID, room id.RoomID) {
	if s == nil {
		return
	}
	s.lockImagePack()
	defer s.packMu.Unlock()
	s.removeStickerPackFromRoomLocked(ctx, account, room)
}

func (s *Server) removeStickerPackFromRoomLocked(ctx context.Context, account remoteapi.UserID, room id.RoomID) {
	if account == "" || room == "" {
		return
	}
	stateKey := wechatStickerPackStateKey(account)
	for _, target := range imagePackStateEventTargets(stateKey) {
		for _, alias := range target.Aliases {
			if target.EventType == event.StateUnstableImagePack && alias == "" {
				continue
			}
			if err := s.packState().Save(ctx, room, target.EventType, alias, json.RawMessage(`{}`)); err != nil {
				log.Printf("failed to remove sticker image pack type=%s account=%s room=%s state_key=%s: %v", target.EventType.Type, account, room, alias, err)
			} else {
				log.Printf("removed sticker image pack type=%s account=%s room=%s state_key=%s", target.EventType.Type, account, room, alias)
			}
		}
	}
}

// patchStickerPackImagesToRoomLocked applies image-object patches to both
// canonical pack state events, then clears non-canonical aliases. Callers must
// hold packMu. Nil replacement values delete images; omitted images are kept.
func (s *Server) patchStickerPackImagesToRoomLocked(ctx context.Context, account remoteapi.UserID, room id.RoomID, replacements map[string]json.RawMessage) (json.RawMessage, error) {
	stateKey := wechatStickerPackStateKey(account)
	targets := imagePackStateEventTargets(stateKey)
	prepared := make([]json.RawMessage, len(targets))
	for index, target := range targets {
		raw, err := s.loadStickerPackOrBaseLocked(ctx, account, room, target.EventType, target.StateKey)
		if err != nil {
			return nil, fmt.Errorf("prepare canonical image pack target %d/%d (canonical targets committed: 0/%d; partial state: no): %w", index+1, len(targets), len(targets), err)
		}
		prepared[index], err = mergePackImages(raw, replacements)
		if err != nil {
			return nil, fmt.Errorf("prepare canonical image pack target %d/%d (canonical targets committed: 0/%d; partial state: no): %w", index+1, len(targets), len(targets), err)
		}
	}
	for index, target := range targets {
		if err := s.packState().Save(ctx, room, target.EventType, target.StateKey, prepared[index]); err != nil {
			partial := "no"
			if index > 0 {
				partial = "yes"
			}
			return nil, fmt.Errorf("save canonical image pack target %d/%d (canonical targets committed: %d/%d; partial state: %s): %w", index+1, len(targets), index, len(targets), partial, err)
		}
	}
	for _, target := range targets {
		for _, alias := range target.Aliases {
			if alias == target.StateKey {
				continue
			}
			if err := s.packState().Save(ctx, room, target.EventType, alias, json.RawMessage(`{}`)); err != nil {
				return nil, fmt.Errorf("clear image pack alias type=%s state_key=%s (canonical writes committed: %d/%d; partial state: alias cleanup incomplete): %w", target.EventType.Type, alias, len(targets), len(targets), err)
			}
		}
	}
	return prepared[0], nil
}

func (s *Server) addStickerToAccountSpace(ctx context.Context, account remoteapi.UserID, space id.RoomID, sticker wechatStickerImage) (int, error) {
	if s == nil {
		return 0, fmt.Errorf("server unavailable")
	}
	s.lockImagePack()
	defer s.packMu.Unlock()
	return s.addStickerToAccountSpaceLocked(ctx, account, space, sticker)
}

func (s *Server) addStickerToAccountSpaceLocked(ctx context.Context, account remoteapi.UserID, space id.RoomID, sticker wechatStickerImage) (int, error) {
	if account == "" || space == "" {
		return 0, fmt.Errorf("account and room are required")
	}
	shortcode, rawImage, err := stickerImageRaw(sticker)
	if err != nil {
		return 0, err
	}
	merged, err := s.patchStickerPackImagesToRoomLocked(ctx, account, space, map[string]json.RawMessage{shortcode: rawImage})
	if err != nil {
		return 0, err
	}
	return len(parsePackImages(merged)), nil
}
