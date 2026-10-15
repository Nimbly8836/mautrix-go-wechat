package matrixas

import (
	"fmt"
	"sort"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/id"
)

// Append related edits without replacing the caption's original backend ID.
// The first progress edit independently anchors the file's backend ID. This
// keeps the existing one-strong-ID-per-Matrix-event journal invariant intact.
func (s *Server) appendMessageReferenceParts(account remoteapi.UserID, rec portalRecord, root id.EventID, client *mautrix.Client, parts ...id.EventID) error {
	if root == "" || client == nil {
		return fmt.Errorf("missing file reference root or sender")
	}
	store, err := s.referenceStore()
	if err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	ref := store.reverse[matrixReferenceKey{account, rec.ChatID, rec.RoomID, root}]
	if ref.EventID == "" || !ref.BridgeSender || ref.MatrixSender != client.UserID {
		return fmt.Errorf("file reference root is not owned by this bridge sender")
	}
	keys := make([]string, 0, 4)
	for key, candidate := range store.forward {
		if key.Account == account && key.Chat == rec.ChatID && key.Room == rec.RoomID && candidate.EventID == root {
			keys = append(keys, key.ID)
		}
	}
	sort.Strings(keys)
	ref.RelatedEventIDs = mergeRelatedEventIDs(ref.RelatedEventIDs, parts)
	ref.Client = client
	return store.appendLocked(referenceRecord{Version: 1, Account: account, Chat: rec.ChatID, Room: rec.RoomID, Keys: keys, Reference: ref})
}

func trustedFilePart(root, part messageReference) bool {
	rootKind := root.Type == remoteapi.MsgText || ((root.Type == remoteapi.MsgFile || root.Type == remoteapi.MsgApp) && fileNamesMatch(root.Text, part.Text))
	return rootKind && part.Type == remoteapi.MsgFile &&
		root.BridgeSender && part.BridgeSender && root.EventID != part.EventID &&
		root.MatrixSender != "" && root.MatrixSender == part.MatrixSender &&
		root.Sender != "" && root.Sender == part.Sender
}

// Caller holds the store mutex. Only a unique, explicitly linked file part
// may replace the caption's identity for quoting; ordinary record attachments
// have no independent file reference and are deliberately not promoted.
func (s *messageReferenceStore) filePartLocked(scope matrixReferenceKey, root messageReference) (messageReference, bool) {
	var found messageReference
	for _, partID := range root.RelatedEventIDs {
		key := scope
		key.Event = partID
		if s.parents[key] != root.EventID {
			continue
		}
		part := s.reverse[key]
		if !trustedFilePart(root, part) {
			continue
		}
		if found.EventID != "" && found.EventID != part.EventID {
			return messageReference{}, false
		}
		found = part
	}
	return found, found.EventID != ""
}

func visibleFileReference(root, file messageReference) messageReference {
	file.EventID = root.EventID
	file.MatrixSender, file.Client = root.MatrixSender, root.Client
	file.RelatedEventIDs = append([]id.EventID(nil), root.RelatedEventIDs...)
	return file
}

func (s *messageReferenceStore) visibleReferenceLocked(scope matrixReferenceKey, ref messageReference, preferFile bool) messageReference {
	if ref.EventID == "" {
		return ref
	}
	// Alias records can predate later edits; always take the root's latest
	// related-event list rather than an older forward-index copy.
	key := scope
	key.Event = ref.EventID
	if current := s.reverse[key]; current.EventID != "" {
		ref.RelatedEventIDs = append([]id.EventID(nil), current.RelatedEventIDs...)
	}
	if ref.Type == remoteapi.MsgFile {
		if owner := s.parents[key]; owner != "" {
			parentKey := scope
			parentKey.Event = owner
			root := s.reverse[parentKey]
			if file, ok := s.filePartLocked(scope, root); ok && file.EventID == ref.EventID {
				return visibleFileReference(root, file)
			}
		}
	}
	if preferFile {
		if file, ok := s.filePartLocked(scope, ref); ok {
			return visibleFileReference(ref, file)
		}
	}
	return ref
}
