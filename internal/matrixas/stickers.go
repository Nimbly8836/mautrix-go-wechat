package matrixas

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
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

func (s *Server) handleStickersCommand(ctx context.Context, room id.RoomID, sender id.UserID, rest string) {
	fields := strings.Fields(rest)
	if len(fields) == 0 || fields[0] == "help" || fields[0] == "帮助" {
		s.notice(ctx, room, fmt.Sprintf("用法:\n%s stickers add <shortcode> <mxc-or-image-url> [label]\n\n会把图片加入当前微信账号 Space 的 m.room.image_pack，Matrix 客户端可把它当 sticker/emoji 使用。", s.commandPrefix()))
		return
	}
	if fields[0] != "add" && fields[0] != "添加" {
		s.notice(ctx, room, fmt.Sprintf("未知 stickers 子命令。发送 `%s stickers help` 查看用法。", s.commandPrefix()))
		return
	}
	account := s.currentAccountID(ctx)
	if account == "" {
		s.notice(ctx, room, fmt.Sprintf("当前没有选择/登录微信账号。先使用 `%s account <wxid>` 或 `%s login <flow>`。", s.commandPrefix(), s.commandPrefix()))
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
	s.notice(ctx, room, fmt.Sprintf("已添加 sticker `:%s:` 到微信账号 Space %s。当前 pack 共 %d 个。", sticker.Shortcode, space, count))
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
	if _, err := s.as.BotIntent().SendStateEvent(ctx, space, event.StateImagePack, stateKey, content); err != nil {
		return 0, err
	}
	return len(content.Images), nil
}
