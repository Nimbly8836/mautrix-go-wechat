package wechat

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

type emojiMeta struct {
	MD5    string
	CDNURL string
	Len    int
	Width  int
	Height int
}

func parseEmojiMeta(raw string) emojiMeta {
	dec := xml.NewDecoder(strings.NewReader(raw))
	var meta emojiMeta
	var inEmoji bool
	var current string
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			name := strings.ToLower(t.Name.Local)
			if name == "emoji" {
				inEmoji = true
				for _, a := range t.Attr {
					switch strings.ToLower(a.Name.Local) {
					case "md5", "androidmd5":
						if meta.MD5 == "" {
							meta.MD5 = strings.TrimSpace(a.Value)
						}
					case "cdnurl":
						meta.CDNURL = strings.TrimSpace(html.UnescapeString(a.Value))
					case "len", "androidlen":
						if meta.Len == 0 {
							meta.Len, _ = strconv.Atoi(strings.TrimSpace(a.Value))
						}
					case "width":
						meta.Width, _ = strconv.Atoi(strings.TrimSpace(a.Value))
					case "height":
						meta.Height, _ = strconv.Atoi(strings.TrimSpace(a.Value))
					}
				}
			} else if inEmoji {
				current = name
			}
		case xml.CharData:
			if !inEmoji || current == "" {
				continue
			}
			value := strings.TrimSpace(html.UnescapeString(string(t)))
			switch current {
			case "md5":
				if meta.MD5 == "" {
					meta.MD5 = value
				}
			case "cdnurl":
				if meta.CDNURL == "" {
					meta.CDNURL = value
				}
			}
		case xml.EndElement:
			name := strings.ToLower(t.Name.Local)
			if name == "emoji" {
				inEmoji = false
			}
			current = ""
		}
	}
	return meta
}

func (c *Client) attachEmojiMedia(ctx context.Context, base *remoteapi.InboundMessage, content string) {
	meta := parseEmojiMeta(content)
	emoji, err := c.ensureEmojiCached(ctx, meta)
	if err != nil {
		return
	}
	base.Media = &remoteapi.Media{
		MimeType: emoji.MimeType,
		FileName: emoji.FileMD5 + filepath.Ext(emoji.Path),
		Data:     emoji.Data,
		Size:     int64(emoji.Size),
	}
	base.Text = "[emoji]"
	if base.Extra == nil {
		base.Extra = map[string]any{}
	}
	base.Extra["emoji_md5"] = emoji.Meta.MD5
	base.Extra["file_md5"] = emoji.FileMD5
	if emoji.Meta.Width > 0 {
		base.Extra["width"] = emoji.Meta.Width
	}
	if emoji.Meta.Height > 0 {
		base.Extra["height"] = emoji.Meta.Height
	}
}

func (c *Client) writeOutgoingEmojiCache(md5sum string, data []byte) error {
	if c.emojiDir == "" {
		return nil
	}
	if err := os.MkdirAll(c.emojiDir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(c.emojiDir, md5sum+".gif")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return os.WriteFile(path, data, 0o644)
	}
	return nil
}

func (c *Client) ensureEmojiCached(ctx context.Context, meta emojiMeta) (*remoteEmoji, error) {
	if meta.MD5 == "" && meta.CDNURL == "" {
		return nil, fmt.Errorf("emoji metadata missing md5 and cdnurl")
	}
	if path := c.locateCachedEmoji(meta.MD5); path != "" {
		return c.readEmojiFile(path, meta)
	}
	if meta.CDNURL == "" {
		return nil, fmt.Errorf("emoji %s has no cdnurl and is not cached", meta.MD5)
	}
	data, contentType, resolved, err := c.fetchEmoji(ctx, meta.CDNURL)
	if err != nil {
		return nil, err
	}
	fileMD5 := contentMD5(data)
	if meta.MD5 == "" {
		meta.MD5 = fileMD5
	}
	ext := emojiExt(resolved, contentType, data)
	path := filepath.Join(c.emojiDir, fileMD5+ext)
	if err := os.MkdirAll(c.emojiDir, 0o755); err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return nil, err
		}
	}
	return &remoteEmoji{Meta: meta, FileMD5: fileMD5, Path: path, Data: data, MimeType: emojiMime(contentType, data), Size: len(data)}, nil
}

type remoteEmoji struct {
	Meta     emojiMeta
	FileMD5  string
	Path     string
	Data     []byte
	MimeType string
	Size     int
}

func (c *Client) locateCachedEmoji(md5 string) string {
	if c.emojiDir == "" || md5 == "" {
		return ""
	}
	for _, ext := range []string{".gif", ".png", ".webp", ".jpg", ".jpeg"} {
		path := filepath.Join(c.emojiDir, md5+ext)
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}

func (c *Client) readEmojiFile(path string, meta emojiMeta) (*remoteEmoji, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return &remoteEmoji{Meta: meta, FileMD5: strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)), Path: path, Data: data, MimeType: emojiMime("", data), Size: len(data)}, nil
}

func (c *Client) fetchEmoji(ctx context.Context, rawURL string) ([]byte, string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", "", err
	}
	client := c.t.hc
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", "", fmt.Errorf("fetch emoji %s: status %d", rawURL, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, "", "", err
	}
	return data, resp.Header.Get("Content-Type"), resp.Request.URL.String(), nil
}

func contentMD5(data []byte) string {
	sum := md5.Sum(data)
	return hex.EncodeToString(sum[:])
}

func emojiExt(resolved, contentType string, data []byte) string {
	ct := strings.ToLower(strings.Split(contentType, ";")[0])
	switch ct {
	case "image/gif":
		return ".gif"
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	case "image/jpeg":
		return ".jpg"
	}
	if u, err := url.Parse(resolved); err == nil {
		ext := strings.ToLower(filepath.Ext(u.Path))
		if ext == ".gif" || ext == ".png" || ext == ".webp" || ext == ".jpg" || ext == ".jpeg" {
			return ext
		}
	}
	if isGIF(data) {
		return ".gif"
	}
	return ".bin"
}

func emojiMime(contentType string, data []byte) string {
	ct := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	if ct != "" {
		return ct
	}
	if isGIF(data) {
		return "image/gif"
	}
	return http.DetectContentType(data)
}

func isGIF(data []byte) bool {
	return len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a")
}
