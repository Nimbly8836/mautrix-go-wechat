package matrixas

import (
	"context"
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
		s.notice(ctx, room, fmt.Sprintf("用法:\n%s stickers add <shortcode> <mxc-or-image-url> [label]\n%s stickers status\n%s stickers repair\n\n会把图片加入当前微信账号 Space，并同步 Element 兼容 pack 到 portal rooms。", s.commandPrefix(), s.commandPrefix(), s.commandPrefix()))
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
	stateKey := wechatStickerPackStateKey(account)
	space := id.RoomID("")
	if acc, ok := s.accounts.get(account); ok {
		space = id.RoomID(acc.SpaceRoom)
	}
	spaceCount := s.imagePackCount(ctx, space, event.StateImagePack, stateKey)
	legacySpaceCount := s.imagePackCount(ctx, space, event.StateUnstableImagePack, "")
	portalRooms := 0
	portalLegacyWithPack := 0
	portalLegacyTotal := 0
	if s.portals != nil {
		for _, portal := range s.portals.list() {
			if portal.RoomID == "" {
				continue
			}
			portalRooms++
			count := s.imagePackCount(ctx, portal.RoomID, event.StateUnstableImagePack, "")
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
	if room == "" || s == nil || s.as == nil || s.as.BotIntent() == nil {
		return 0
	}
	var content event.ImagePackEventContent
	if err := s.as.BotIntent().StateEvent(ctx, room, eventType, stateKey, &content); err != nil {
		return 0
	}
	return len(content.Images)
}

func (s *Server) syncStickerToElementPortals(ctx context.Context, account remoteapi.UserID, sticker wechatStickerImage) int {
	if s == nil || s.portals == nil {
		return 0
	}
	count := 0
	for _, portal := range s.portals.list() {
		if portal.RoomID == "" {
			continue
		}
		s.addStickerToElementPortal(ctx, account, portal.RoomID, sticker)
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
	for _, target := range imagePackAccountDataTargets(stateKey) {
		var content event.ImagePackRoomsEventContent
		if err := client.GetAccountData(ctx, target.EventType, &content); err != nil {
			log.Printf("creating image pack rooms account data type=%s for %s: %v", target.EventType, user, err)
		}
		setSingleImagePackRoomSourceAliases(&content, room, target.StateKey, target.Aliases...)
		if err := client.SetAccountData(ctx, target.EventType, &content); err != nil {
			log.Printf("failed to register image pack room type=%s for %s room=%s state_key=%s: %v", target.EventType, user, room, target.StateKey, err)
		} else {
			log.Printf("registered image pack room type=%s for %s room=%s state_key=%s", target.EventType, user, room, target.StateKey)
		}
	}
}

func (s *Server) repairStickerPacksForAccount(ctx context.Context, account remoteapi.UserID, space id.RoomID, owner id.UserID) int {
	if account == "" || space == "" || s == nil || s.as == nil || s.as.BotIntent() == nil || s.as.BotIntent().Client == nil {
		return 0
	}
	stateKey := wechatStickerPackStateKey(account)
	merged := buildWechatStickerPackContent(account, nil)
	for _, target := range imagePackStateEventTargets(stateKey) {
		var existing event.ImagePackEventContent
		if err := s.as.BotIntent().StateEvent(ctx, space, target.EventType, target.StateKey, &existing); err == nil {
			mergeImagePackContent(merged, &existing)
		}
	}
	joined, err := s.as.BotIntent().Client.JoinedRooms(ctx)
	if err != nil {
		log.Printf("failed to list joined rooms for sticker pack repair account=%s: %v", account, err)
	} else {
		for _, room := range joined.JoinedRooms {
			for _, target := range imagePackStateEventTargets(stateKey) {
				for _, alias := range target.Aliases {
					var existing event.ImagePackEventContent
					if err := s.as.BotIntent().StateEvent(ctx, room, target.EventType, alias, &existing); err == nil {
						mergeImagePackContent(merged, &existing)
					}
				}
			}
		}
	}
	if len(merged.Images) == 0 {
		return 0
	}
	merged.Metadata = buildWechatStickerPackContent(account, nil).Metadata
	if err := s.writeStickerPackToRoom(ctx, account, space, merged); err != nil {
		log.Printf("failed to write repaired account sticker pack account=%s space=%s: %v", account, space, err)
		return 0
	}
	if owner != "" {
		s.ensureUserSeesImagePackRoom(ctx, owner, space, stateKey)
	}
	if s.portals != nil {
		for _, portal := range s.portals.list() {
			s.mirrorStickerPackToElementPortal(ctx, account, space, portal.RoomID)
		}
	}
	return len(merged.Images)
}

func (s *Server) repairStickerPackCompatibility(ctx context.Context, account remoteapi.UserID, room id.RoomID, owner id.UserID) {
	if account == "" || room == "" || s == nil || s.as == nil || s.as.BotIntent() == nil {
		return
	}
	stateKey := wechatStickerPackStateKey(account)
	var content event.ImagePackEventContent
	if err := s.as.BotIntent().StateEvent(ctx, room, event.StateImagePack, stateKey, &content); err != nil || len(content.Images) == 0 {
		return
	}
	if err := s.writeStickerPackToRoom(ctx, account, room, &content); err != nil {
		log.Printf("failed to repair sticker pack compatibility account=%s room=%s: %v", account, room, err)
	}
	if owner != "" {
		s.ensureUserSeesImagePackRoom(ctx, owner, room, stateKey)
	}
}

func (s *Server) mirrorStickerPackToElementPortal(ctx context.Context, account remoteapi.UserID, source, portal id.RoomID) {
	if account == "" || source == "" || portal == "" || source == portal || s == nil || s.as == nil || s.as.BotIntent() == nil {
		return
	}
	stateKey := wechatStickerPackStateKey(account)
	var content event.ImagePackEventContent
	if err := s.as.BotIntent().StateEvent(ctx, source, event.StateImagePack, stateKey, &content); err != nil || len(content.Images) == 0 {
		return
	}
	for _, target := range imagePackPortalMirrorTargets() {
		if _, err := s.as.BotIntent().SendStateEvent(ctx, portal, target.EventType, target.StateKey, &content); err != nil {
			log.Printf("failed to mirror sticker pack to Element portal type=%s account=%s portal=%s state_key=%s: %v", target.EventType.Type, account, portal, target.StateKey, err)
		}
	}
}

func (s *Server) addStickerToElementPortal(ctx context.Context, account remoteapi.UserID, portal id.RoomID, sticker wechatStickerImage) {
	if account == "" || portal == "" || s == nil || s.as == nil || s.as.BotIntent() == nil {
		return
	}
	content := buildWechatStickerPackContent(account, nil)
	var existing event.ImagePackEventContent
	if err := s.as.BotIntent().StateEvent(ctx, portal, event.StateUnstableImagePack, "", &existing); err == nil && existing.Images != nil {
		content = &existing
	}
	if content.Images == nil {
		content.Images = map[string]*event.ImagePackImage{}
	}
	content.Metadata = buildWechatStickerPackContent(account, nil).Metadata
	content.Images[sticker.Shortcode] = &event.ImagePackImage{URL: sticker.URL, Body: sticker.Body}
	for _, target := range imagePackPortalMirrorTargets() {
		if _, err := s.as.BotIntent().SendStateEvent(ctx, portal, target.EventType, target.StateKey, content); err != nil {
			log.Printf("failed to add sticker to Element portal pack type=%s account=%s portal=%s state_key=%s: %v", target.EventType.Type, account, portal, target.StateKey, err)
		} else {
			log.Printf("added sticker to Element portal pack type=%s account=%s portal=%s state_key=%s count=%d shortcode=%s", target.EventType.Type, account, portal, target.StateKey, len(content.Images), sticker.Shortcode)
		}
	}
}

func (s *Server) removeStickerPackFromRoom(ctx context.Context, account remoteapi.UserID, room id.RoomID) {
	if account == "" || room == "" || s == nil || s.as == nil || s.as.BotIntent() == nil {
		return
	}
	stateKey := wechatStickerPackStateKey(account)
	for _, target := range imagePackStateEventTargets(stateKey) {
		for _, alias := range target.Aliases {
			if target.EventType == event.StateUnstableImagePack && alias == "" {
				continue
			}
			if _, err := s.as.BotIntent().SendStateEvent(ctx, room, target.EventType, alias, map[string]any{}); err != nil {
				log.Printf("failed to remove sticker image pack type=%s account=%s room=%s state_key=%s: %v", target.EventType.Type, account, room, alias, err)
			} else {
				log.Printf("removed sticker image pack type=%s account=%s room=%s state_key=%s", target.EventType.Type, account, room, alias)
			}
		}
	}
}

func (s *Server) writeStickerPackToRoom(ctx context.Context, account remoteapi.UserID, room id.RoomID, content *event.ImagePackEventContent) error {
	stateKey := wechatStickerPackStateKey(account)
	for _, target := range imagePackStateEventTargets(stateKey) {
		for _, alias := range target.Aliases {
			if alias != target.StateKey {
				_, _ = s.as.BotIntent().SendStateEvent(ctx, room, target.EventType, alias, map[string]any{})
			}
		}
		if _, err := s.as.BotIntent().SendStateEvent(ctx, room, target.EventType, target.StateKey, content); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) addStickerToAccountSpace(ctx context.Context, account remoteapi.UserID, space id.RoomID, sticker wechatStickerImage) (int, error) {
	stateKey := wechatStickerPackStateKey(account)
	content := buildWechatStickerPackContent(account, nil)
	if s.as != nil && s.as.BotIntent() != nil {
		var existing event.ImagePackEventContent
		if err := s.as.BotIntent().StateEvent(ctx, space, event.StateImagePack, stateKey, &existing); err == nil && existing.Images != nil {
			content = &existing
		} else if err != nil {
			log.Printf("creating new sticker image pack in space %s state_key=%s: %v", space, stateKey, err)
		}
	}
	if content.Images == nil {
		content.Images = map[string]*event.ImagePackImage{}
	}
	metadata := buildWechatStickerPackContent(account, nil).Metadata
	content.Metadata = metadata
	content.Images[sticker.Shortcode] = &event.ImagePackImage{URL: sticker.URL, Body: sticker.Body}
	if err := s.writeStickerPackToRoom(ctx, account, space, content); err != nil {
		return 0, err
	}
	return len(content.Images), nil
}
