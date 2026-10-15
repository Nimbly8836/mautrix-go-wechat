package matrixas

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

const (
	fileCaptionWindow          = 30 * time.Second
	fileCandidateLimit         = 512
	deferredFileTimeout        = 120 * time.Second
	deferredFileFailureTimeout = 10 * time.Second
)

type fileCaptionKey struct {
	account    remoteapi.UserID
	chat       remoteapi.ChatID
	room       id.RoomID
	sender     remoteapi.UserID
	generation uint64
}

type fileCaption struct {
	name      string
	timestamp time.Time
	eventID   id.EventID
	client    *mautrix.Client // The actual sender, including bot fallback, never re-resolved.
}

type fileCaptionSlot struct {
	order     uint64
	candidate *fileCaption
	expires   time.Time
	claimed   bool
	ambiguous bool
}

// History includes ineligible deliveries: only the immediately preceding
// same-sender arrival can match. Connector order, not ACK completion or wall
// clock arrival here, decides which entry precedes a queued file.
type fileCaptionDelivery struct {
	key     fileCaptionKey
	slot    *fileCaptionSlot
	claimed *fileCaption
}

func isFileLifecycleMessage(m *remoteapi.InboundMessage) bool {
	return m.Type == remoteapi.MsgFile && m.Record == nil && m.Solitaire == nil
}

func fileLifecycleName(m *remoteapi.InboundMessage) string {
	if m.Media != nil && m.Media.FileName != "" {
		return m.Media.FileName
	}
	return m.Text
}

// Deliberately conservative: only a complete, single-line filename with an
// extension can become a caption. No substring, history, or fuzzy matching.
func isFileCaptionName(name string) bool {
	if len(name) == 0 || len(name) > 512 || strings.TrimSpace(name) != name || strings.ContainsAny(name, "/\\<>\"|:*?") {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return false
		}
	}
	dot := strings.LastIndexByte(name, '.')
	if dot <= 0 || dot == len(name)-1 || len(name)-dot > 17 || strings.TrimSpace(name[:dot]) == "" {
		return false
	}
	for _, r := range name[dot+1:] {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

func fileCaptionMatches(c *fileCaption, m *remoteapi.InboundMessage) bool {
	name := fileLifecycleName(m)
	if c == nil || m.Reply != nil || !isFileCaptionName(name) || m.Timestamp.IsZero() || c.timestamp.IsZero() || m.Timestamp.Unix() <= 0 || c.timestamp.Unix() <= 0 {
		return false
	}
	delta := m.Timestamp.Sub(c.timestamp)
	if delta < 0 || delta > fileCaptionWindow {
		return false
	}
	// These are the observed backend filename substitutions, not a general
	// punctuation normalizer. The direction matters: caption -> file name.
	return c.name == name || strings.NewReplacer("(", "_", ")", "_", "（", "_", "）", "_", "-", "_").Replace(c.name) == name
}

func (s *Server) beginFileCaptionDelivery(account remoteapi.UserID, rec portalRecord, m *remoteapi.InboundMessage) fileCaptionDelivery {
	s.mu.Lock()
	generation := s.remoteGeneration
	s.mu.Unlock()
	key := fileCaptionKey{account, rec.ChatID, rec.RoomID, m.Sender, generation}
	now := time.Now()
	s.fileDeliveryMu.Lock()
	defer s.fileDeliveryMu.Unlock()
	if s.fileCaptions == nil {
		s.fileCaptions = make(map[fileCaptionKey][]*fileCaptionSlot)
	}
	for k, history := range s.fileCaptions {
		live := false
		for _, slot := range history {
			live = live || now.Before(slot.expires)
		}
		// Keep ineligible tombstones while any entry is live. Removing an
		// expired intervening arrival must not expose an older caption.
		if !live {
			delete(s.fileCaptions, k)
		}
	}
	history := s.fileCaptions[key]
	if len(history) == 0 && len(s.fileCaptions) >= fileCandidateLimit {
		var oldest fileCaptionKey
		var expiry time.Time
		for k, entries := range s.fileCaptions {
			latest := entries[len(entries)-1].expires
			if expiry.IsZero() || latest.Before(expiry) {
				oldest, expiry = k, latest
			}
		}
		delete(s.fileCaptions, oldest)
	}
	order := m.DeliveryOrder
	if order == 0 {
		s.fileDeliverySequence++
		order = s.fileDeliverySequence
	} else if order > s.fileDeliverySequence {
		s.fileDeliverySequence = order
	}
	slot := &fileCaptionSlot{order: order, expires: now.Add(fileCaptionWindow)}
	delivery := fileCaptionDelivery{key: key, slot: slot}
	var previous *fileCaptionSlot
	for _, entry := range history {
		if entry.order == order {
			entry.ambiguous, slot.ambiguous = true, true
		}
		if entry.order < order {
			previous = entry
		}
	}
	if isFileLifecycleMessage(m) && !slot.ambiguous && previous != nil && now.Before(previous.expires) && !previous.ambiguous && !previous.claimed && fileCaptionMatches(previous.candidate, m) {
		previous.claimed = true
		delivery.claimed = previous.candidate
	}
	history = append(history, slot)
	sort.Slice(history, func(i, j int) bool { return history[i].order < history[j].order })
	if len(history) > 8 {
		history = history[len(history)-8:]
	}
	s.fileCaptions[key] = history
	return delivery
}

func (s *Server) rememberFileCaption(delivery fileCaptionDelivery, m *remoteapi.InboundMessage, eventID id.EventID, client *mautrix.Client) {
	if m.Type != remoteapi.MsgText || m.Reply != nil || m.Record != nil || m.Solitaire != nil || m.Media != nil || m.FetchMedia != nil || m.Sender == "" || m.ID == "" || eventID == "" || client == nil || !isFileCaptionName(m.Text) || m.Timestamp.IsZero() || m.Timestamp.Unix() <= 0 || delivery.key.account == "" || m.Chat != delivery.key.chat {
		return
	}
	s.fileDeliveryMu.Lock()
	defer s.fileDeliveryMu.Unlock()
	slot := delivery.slot
	if slot.ambiguous || !time.Now().Before(slot.expires) {
		return
	}
	for _, entry := range s.fileCaptions[delivery.key] {
		if entry == slot {
			slot.candidate = &fileCaption{name: m.Text, timestamp: m.Timestamp, eventID: eventID, client: client}
			return
		}
	}
}

func fileLifecycleTransactionID(account remoteapi.UserID, rec portalRecord, m *remoteapi.InboundMessage, root id.EventID, phase string) string {
	data, _ := json.Marshal([]string{string(account), string(rec.ChatID), string(rec.RoomID), string(m.Sender), string(m.ID), string(root), phase})
	sum := sha256.Sum256(data)
	return fmt.Sprintf("wechat-file-%x", sum)
}

func (s *Server) fileDeliveryCurrent(ctx context.Context, delivery fileCaptionDelivery) bool {
	if ctx.Err() != nil || s.currentAccountID(ctx) != delivery.key.account || ctx.Err() != nil || s.portals == nil {
		return false
	}
	rec, ok := s.portals.get(delivery.key.chat)
	if !ok || rec.RoomID != delivery.key.room {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.remoteGeneration == delivery.key.generation && !s.remoteTransition
}

func (s *Server) fileTransferSlots() chan struct{} {
	s.fileDeliveryMu.Lock()
	defer s.fileDeliveryMu.Unlock()
	if s.fileTransfers == nil {
		s.fileTransfers = make(chan struct{}, 4)
	}
	return s.fileTransfers
}

// Runs entirely in the connector dispatch goroutine. No detached downloads or
// edits can outlive the account's connector context.
func (s *Server) bridgeDeferredFile(ctx context.Context, account remoteapi.UserID, rec portalRecord, m *remoteapi.InboundMessage, delivery fileCaptionDelivery) {
	workCtx, cancel := context.WithTimeout(ctx, deferredFileTimeout)
	defer cancel()
	if !s.fileDeliveryCurrent(workCtx, delivery) {
		return
	}
	name := recordShortText(fileLifecycleName(m), 512)
	if name == "" {
		name = "wechat-file"
	}
	root := id.EventID("")
	client := s.as.BotIntent().Client
	var reply *event.RelatesTo
	matched := delivery.claimed != nil
	resuming := false
	for _, key := range m.DeduplicationKeys {
		if !strings.HasPrefix(key, "new:") {
			continue
		}
		ref := s.getMessageReference(account, rec, remoteapi.MessageID(strings.TrimPrefix(key, "new:")), key)
		if ref.EventID == "" {
			continue
		}
		// A completed/failure delivery or an older incompatible reference must
		// not create another root on callback replay. Pending file deliveries
		// reuse their persisted visible root, including after restart.
		if !ref.BridgeSender || ref.Sender != m.Sender || ref.Type != remoteapi.MsgFile || ref.Client == nil || len(ref.RelatedEventIDs) >= 2 {
			return
		}
		root, client, matched, resuming = ref.EventID, ref.Client, false, true
		break
	}
	if resuming {
		// Client and visible root were restored from the scoped journal.
	} else if matched {
		root, client = delivery.claimed.eventID, delivery.claimed.client
	} else {
		sender := m.Sender
		if sender == "" {
			sender = remoteapi.UserID(m.Chat)
		}
		if shouldJoinPuppet(account, sender) {
			client = s.ensurePuppetJoined(workCtx, sender, rec.RoomID)
		}
	}
	editTimestamp := max(time.Now().UnixMilli(), m.Timestamp.UnixMilli())
	send := func(sendCtx context.Context, content *event.MessageEventContent, phase string, edit bool) (id.EventID, bool) {
		if !s.fileDeliveryCurrent(sendCtx, delivery) {
			return "", false
		}
		timestamp := m.Timestamp.UnixMilli()
		if edit {
			content.RelatesTo = reply
			content.SetEdit(root)
			// Matrix chooses the newest edit by timestamp, not request order.
			// Equal timestamps can leave the progress edit visible forever.
			editTimestamp = max(editTimestamp+1, time.Now().UnixMilli())
			timestamp = editTimestamp
		}
		opCtx, done := s.matrixOperationContext(sendCtx)
		defer done()
		resp, err := client.SendMessageEvent(opCtx, rec.RoomID, event.EventMessage, content, mautrix.ReqSendEvent{
			TransactionID: fileLifecycleTransactionID(account, rec, m, root, phase),
			Timestamp:     timestamp,
		})
		if err != nil || resp == nil || resp.EventID == "" {
			return "", false
		}
		return resp.EventID, true
	}
	// Failure uses a separate short budget only while the original connector
	// context remains live. Never expose backend URLs, errors, or credentials.
	fail := func() {
		if root == "" || ctx.Err() != nil {
			return
		}
		failureCtx, done := context.WithTimeout(ctx, deferredFileFailureTimeout)
		defer done()
		if editID, ok := send(failureCtx, &event.MessageEventContent{MsgType: event.MsgText, Body: name + "（文件传输失败）"}, "failure", true); ok {
			_ = s.appendMessageReferenceParts(account, rec, root, client, editID)
		}
	}
	if !matched && !resuming {
		content := &event.MessageEventContent{MsgType: event.MsgText, Body: name}
		s.attachInboundReply(account, rec, m, content)
		reply = content.RelatesTo
		var ok bool
		root, ok = send(workCtx, content, "root", false)
		if !ok {
			return // An uncertain root must never create a second visible event.
		}
		if err := s.rememberMessageReference(account, rec, m, root, client); err != nil {
			return
		}
		for _, alias := range messageIDAliases(m) {
			s.rememberMessageEvent(m.Chat, alias, root)
		}
	}
	progress, ok := send(workCtx, &event.MessageEventContent{MsgType: event.MsgText, Body: name + "（文件传输中…）"}, "progress", true)
	if !ok {
		fail()
		return
	}
	// Link the edit to its visible root before publishing the new file identity.
	// In particular, never overwrite the caption's source-ID aliases.
	if err := s.appendMessageReferenceParts(account, rec, root, client, progress); err != nil {
		return
	}
	if matched {
		if err := s.rememberMessageReference(account, rec, m, progress, client); err != nil {
			return
		}
		for _, alias := range messageIDAliases(m) {
			s.rememberMessageEvent(m.Chat, alias, root)
		}
	}
	slots := s.fileTransferSlots()
	select {
	case slots <- struct{}{}:
		defer func() { <-slots }()
	case <-workCtx.Done():
		fail()
		return
	}
	if !s.fileDeliveryCurrent(workCtx, delivery) {
		fail()
		return
	}
	media := m.Media
	if m.FetchMedia != nil {
		var err error
		media, err = m.FetchMedia(workCtx)
		if err != nil {
			fail()
			return
		}
	}
	if media == nil || len(media.Data) == 0 {
		fail()
		return
	}
	if !s.fileDeliveryCurrent(workCtx, delivery) {
		fail()
		return
	}
	mime := media.MimeType
	if mime == "" {
		mime = http.DetectContentType(media.Data)
	}
	if media.FileName != "" {
		name = recordShortText(media.FileName, 512)
	}
	upload, err := s.as.BotIntent().UploadBytesWithName(workCtx, media.Data, mime, name)
	if err != nil || upload == nil {
		fail()
		return
	}
	uri, valid := validMXC(upload.ContentURI.String())
	if !valid || len(uri) > 2048 {
		fail()
		return
	}
	content := &event.MessageEventContent{MsgType: event.MsgFile, Body: name, FileName: name, URL: uri, Info: &event.FileInfo{MimeType: mime, Size: len(media.Data)}}
	terminal, ok := send(workCtx, content, "complete", true)
	if !ok {
		fail()
		return
	}
	_ = s.appendMessageReferenceParts(account, rec, root, client, terminal)
}
