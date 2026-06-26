package matrixas

import (
	"context"
	"fmt"
	"log"
	"strings"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

const (
	spaceFolderContacts = "contacts"
	spaceFolderOfficial = "official"
	spaceFolderGroups   = "groups"
)

type accountSpaceFolder struct {
	Key   string
	Name  string
	Topic string
}

var accountSpaceFolders = []accountSpaceFolder{
	{Key: spaceFolderContacts, Name: "联系人", Topic: "WeChat one-to-one contact portals"},
	{Key: spaceFolderOfficial, Name: "公众号", Topic: "WeChat official account portals"},
	{Key: spaceFolderGroups, Name: "群聊", Topic: "WeChat group chat portals"},
}

// ensureAccountSpace creates or returns the Matrix Space for the current WeChat
// account. The account space contains child spaces (folders) for contacts,
// official accounts and group chats.
func (s *Server) ensureAccountSpace(ctx context.Context, account remoteapi.UserID) (id.RoomID, error) {
	s.spaceMu.Lock()
	defer s.spaceMu.Unlock()
	return s.ensureAccountSpaceLocked(ctx, account)
}

func (s *Server) ensureAccountSpaceLocked(ctx context.Context, account remoteapi.UserID) (id.RoomID, error) {
	if account == "" {
		return "", fmt.Errorf("empty account id")
	}
	if acc, ok := s.accounts.get(account); ok && acc.SpaceRoom != "" {
		space := id.RoomID(acc.SpaceRoom)
		if _, err := s.ensureAccountSpaceFoldersLocked(ctx, account, space); err != nil {
			return "", err
		}
		if owner := s.portals.owner(); owner != "" {
			s.ensureRoomAdminPower(ctx, owner, space)
		}
		return space, nil
	}
	invite := []id.UserID{}
	if owner := s.portals.owner(); owner != "" {
		invite = append(invite, owner)
	}
	name := fmt.Sprintf("WeChat %s", account)
	resp, err := s.as.BotIntent().CreateRoom(ctx, &mautrix.ReqCreateRoom{
		Name:   name,
		Topic:  fmt.Sprintf("WeChat bridge space for account %s", account),
		Invite: invite,
		Preset: "private_chat",
		CreationContent: map[string]any{
			"type": event.RoomTypeSpace,
		},
	})
	if err != nil {
		return "", fmt.Errorf("create account space for %s: %w", account, err)
	}
	s.accounts.setSpace(account, resp.RoomID.String())
	if owner := s.portals.owner(); owner != "" {
		s.ensureRoomAdminPower(ctx, owner, resp.RoomID)
	}
	log.Printf("created account space: account=%s room=%s", account, resp.RoomID)
	s.notifyManagement(ctx, fmt.Sprintf("已创建微信账号 Space:\n- account: %s\n- space: %s", account, resp.RoomID))
	if _, err := s.ensureAccountSpaceFoldersLocked(ctx, account, resp.RoomID); err != nil {
		return resp.RoomID, err
	}
	return resp.RoomID, nil
}

func (s *Server) currentAccountID(ctx context.Context) remoteapi.UserID {
	if self, err := s.api.Self(ctx); err == nil && self != "" {
		return self
	}
	if s.accounts != nil {
		if acc, ok := s.accounts.current(); ok {
			return acc.ID
		}
	}
	return ""
}

func (s *Server) inviteUserToRoom(ctx context.Context, room id.RoomID, user id.UserID) {
	if room == "" || user == "" {
		return
	}
	_, err := s.as.BotIntent().InviteUser(ctx, room, &mautrix.ReqInviteUser{
		UserID: user,
		Reason: "WeChat bridge space access",
	})
	if err != nil && !strings.Contains(strings.ToLower(err.Error()), "already") {
		log.Printf("failed to invite %s to room/space %s: %v", user, room, err)
	}
}

func (s *Server) ensureRoomAdminPower(ctx context.Context, user id.UserID, room id.RoomID) {
	if user == "" || room == "" || s == nil || s.as == nil || s.as.BotIntent() == nil {
		return
	}
	var pl event.PowerLevelsEventContent
	if err := s.as.BotIntent().StateEvent(ctx, room, event.StatePowerLevels, "", &pl); err != nil {
		pl = event.PowerLevelsEventContent{}
	}
	if pl.Users == nil {
		pl.Users = map[id.UserID]int{}
	}
	changed := false
	if pl.Users[user] < 100 {
		pl.Users[user] = 100
		changed = true
	}
	if bot := s.as.BotMXID(); bot != "" && pl.Users[bot] < 100 {
		pl.Users[bot] = 100
		changed = true
	}
	if !changed {
		return
	}
	if _, err := s.as.BotIntent().SendStateEvent(ctx, room, event.StatePowerLevels, "", &pl); err != nil {
		log.Printf("failed to set admin power for %s in %s: %v", user, room, err)
	}
}

func (s *Server) ensureAccountSpaceVisibleTo(ctx context.Context, account remoteapi.UserID, user id.UserID) (id.RoomID, map[string]id.RoomID, error) {
	s.spaceMu.Lock()
	defer s.spaceMu.Unlock()
	space, err := s.ensureAccountSpaceLocked(ctx, account)
	if err != nil {
		return "", nil, err
	}
	folders, err := s.ensureAccountSpaceFoldersLocked(ctx, account, space)
	if err != nil {
		return space, folders, err
	}
	s.inviteUserToRoom(ctx, space, user)
	s.ensureRoomAdminPower(ctx, user, space)
	for _, folder := range folders {
		s.inviteUserToRoom(ctx, folder, user)
		s.ensureRoomAdminPower(ctx, user, folder)
	}
	return space, folders, nil
}

func (s *Server) ensureAccountSpaceFolders(ctx context.Context, account remoteapi.UserID, parent id.RoomID) (map[string]id.RoomID, error) {
	s.spaceMu.Lock()
	defer s.spaceMu.Unlock()
	return s.ensureAccountSpaceFoldersLocked(ctx, account, parent)
}

func (s *Server) ensureAccountSpaceFoldersLocked(ctx context.Context, account remoteapi.UserID, parent id.RoomID) (map[string]id.RoomID, error) {
	out := map[string]id.RoomID{}
	if parent == "" {
		return out, fmt.Errorf("empty account space")
	}
	acc, _ := s.accounts.get(account)
	for _, folder := range accountSpaceFolders {
		roomID := id.RoomID("")
		if acc.SpaceFolders != nil {
			roomID = id.RoomID(acc.SpaceFolders[folder.Key])
		}
		if roomID == "" {
			created, err := s.createAccountFolderSpace(ctx, account, folder)
			if err != nil {
				return out, err
			}
			roomID = created
			s.accounts.setSpaceFolder(account, folder.Key, roomID.String())
			log.Printf("created account folder space: account=%s folder=%s room=%s", account, folder.Key, roomID)
		}
		out[folder.Key] = roomID
		if owner := s.portals.owner(); owner != "" {
			s.ensureRoomAdminPower(ctx, owner, roomID)
		}
		if _, err := s.as.BotIntent().SetRoomName(ctx, roomID, folder.Name); err != nil {
			log.Printf("failed to update account folder space name: account=%s folder=%s room=%s: %v", account, folder.Key, roomID, err)
		}
		if _, err := s.as.BotIntent().SetRoomTopic(ctx, roomID, folder.Topic); err != nil {
			log.Printf("failed to update account folder space topic: account=%s folder=%s room=%s: %v", account, folder.Key, roomID, err)
		}
		s.addSpaceChild(ctx, parent, roomID, true)
		s.setSpaceParent(ctx, roomID, parent, false)
	}
	return out, nil
}

func (s *Server) createAccountRootSpace(ctx context.Context, account remoteapi.UserID, owner id.UserID) (id.RoomID, error) {
	invite := []id.UserID{}
	if owner != "" {
		invite = append(invite, owner)
	}
	name := fmt.Sprintf("WeChat %s", account)
	resp, err := s.as.BotIntent().CreateRoom(ctx, &mautrix.ReqCreateRoom{
		Name:   name,
		Topic:  fmt.Sprintf("WeChat bridge space for account %s", account),
		Invite: invite,
		Preset: "private_chat",
		CreationContent: map[string]any{
			"type": event.RoomTypeSpace,
		},
	})
	if err != nil {
		return "", fmt.Errorf("create account space for %s: %w", account, err)
	}
	return resp.RoomID, nil
}

func (s *Server) createAccountFolderSpace(ctx context.Context, account remoteapi.UserID, folder accountSpaceFolder) (id.RoomID, error) {
	invite := []id.UserID{}
	if owner := s.portals.owner(); owner != "" {
		invite = append(invite, owner)
	}
	resp, err := s.as.BotIntent().CreateRoom(ctx, &mautrix.ReqCreateRoom{
		Name:   folder.Name,
		Topic:  folder.Topic,
		Invite: invite,
		Preset: "private_chat",
		CreationContent: map[string]any{
			"type": event.RoomTypeSpace,
		},
	})
	if err != nil {
		return "", fmt.Errorf("create account folder space %s for %s: %w", folder.Key, account, err)
	}
	return resp.RoomID, nil
}

func (s *Server) attachExistingPortalsToAccountSpace(ctx context.Context, account remoteapi.UserID, space id.RoomID) {
	if space == "" || s.portals == nil {
		return
	}
	for _, portal := range s.portals.list() {
		s.addPortalToAccountSpace(ctx, account, portal)
	}
}

func (s *Server) rebuildAccountSpace(ctx context.Context, account remoteapi.UserID, owner id.UserID) (id.RoomID, map[string]id.RoomID, int, error) {
	s.spaceMu.Lock()
	oldAcc, _ := s.accounts.get(account)
	oldSpace := id.RoomID(oldAcc.SpaceRoom)
	oldFolders := map[string]id.RoomID{}
	for key, room := range oldAcc.SpaceFolders {
		oldFolders[key] = id.RoomID(room)
	}
	space, err := s.createAccountRootSpace(ctx, account, owner)
	if err != nil {
		s.spaceMu.Unlock()
		return "", nil, 0, err
	}
	folders := map[string]id.RoomID{}
	folderStrings := map[string]string{}
	for _, folder := range accountSpaceFolders {
		room, err := s.createAccountFolderSpace(ctx, account, folder)
		if err != nil {
			s.spaceMu.Unlock()
			return space, folders, 0, err
		}
		folders[folder.Key] = room
		folderStrings[folder.Key] = room.String()
		s.addSpaceChild(ctx, space, room, true)
		s.setSpaceParent(ctx, room, space, false)
		s.ensureRoomAdminPower(ctx, owner, room)
	}
	s.accounts.replaceSpace(account, space.String(), folderStrings)
	s.ensureRoomAdminPower(ctx, owner, space)
	s.spaceMu.Unlock()

	for _, folder := range oldFolders {
		s.removeSpaceChild(ctx, oldSpace, folder)
		s.removeSpaceParent(ctx, folder, oldSpace)
	}
	count := 0
	for _, portal := range s.portals.list() {
		s.removeSpaceChild(ctx, oldSpace, portal.RoomID)
		s.removeSpaceParent(ctx, portal.RoomID, oldSpace)
		for _, oldFolder := range oldFolders {
			s.removeSpaceChild(ctx, oldFolder, portal.RoomID)
			s.removeSpaceParent(ctx, portal.RoomID, oldFolder)
		}
		s.addPortalToAccountSpace(ctx, account, portal)
		s.ensureRoomAdminPower(ctx, owner, portal.RoomID)
		count++
	}
	return space, folders, count, nil
}

func (s *Server) repairAccountSpace(ctx context.Context, account remoteapi.UserID, owner id.UserID) int {
	space, folders, err := s.ensureAccountSpaceVisibleTo(ctx, account, owner)
	if err != nil {
		log.Printf("failed to repair account space account=%s owner=%s: %v", account, owner, err)
		return 0
	}
	s.ensureRoomAdminPower(ctx, owner, space)
	for _, folder := range folders {
		s.ensureRoomAdminPower(ctx, owner, folder)
	}
	count := 0
	for _, portal := range s.portals.list() {
		s.addPortalToAccountSpace(ctx, account, portal)
		s.ensureRoomAdminPower(ctx, owner, portal.RoomID)
		count++
	}
	return count
}

func (s *Server) addPortalToAccountSpace(ctx context.Context, account remoteapi.UserID, portal portalRecord) {
	if account == "" || portal.RoomID == "" {
		return
	}
	if owner := s.portals.owner(); owner != "" {
		s.ensureRoomAdminPower(ctx, owner, portal.RoomID)
	}
	space, err := s.ensureAccountSpace(ctx, account)
	if err != nil {
		log.Printf("failed to ensure space for account %s: %v", account, err)
		return
	}
	folders, err := s.ensureAccountSpaceFolders(ctx, account, space)
	if err != nil {
		log.Printf("failed to ensure folder spaces for account %s: %v", account, err)
		return
	}
	folder := s.portalSpaceFolder(ctx, portal)
	folderSpace := folders[folder]
	if folderSpace == "" {
		folderSpace = space
	}
	// Keep every portal directly under the account root space, and also add it
	// to the category subspace. The root parent is non-canonical so clients can
	// still prefer the category as the primary parent while the room remains
	// visible under the account space.
	s.addSpaceChild(ctx, space, portal.RoomID, true)
	s.setSpaceParent(ctx, portal.RoomID, space, false)
	s.addPortalToSpace(ctx, folderSpace, portal)
}

func (s *Server) portalSpaceFolder(ctx context.Context, portal portalRecord) string {
	chatID := portal.ChatID
	if strings.HasSuffix(string(chatID), "@chatroom") {
		return spaceFolderGroups
	}
	if s.api != nil && chatID != "" {
		if chat, err := s.api.GetChat(ctx, chatID); err == nil {
			chat = normalizePortalChat(portal, chat)
			if chat.IsGroup {
				return spaceFolderGroups
			}
			if isOfficialChat(chat) {
				return spaceFolderOfficial
			}
		} else {
			log.Printf("failed to classify portal space folder chat=%s: %v", chatID, err)
		}
	}
	if portal.IsGroup {
		return spaceFolderGroups
	}
	return spaceFolderContacts
}

func (s *Server) addPortalToSpace(ctx context.Context, space id.RoomID, portal portalRecord) {
	if space == "" || portal.RoomID == "" {
		return
	}
	s.addSpaceChild(ctx, space, portal.RoomID, true)
	s.setSpaceParent(ctx, portal.RoomID, space, true)
}

func (s *Server) addSpaceChild(ctx context.Context, space, child id.RoomID, suggested bool) {
	if space == "" || child == "" {
		return
	}
	via := []string{s.cfg.Homeserver.Domain}
	_, err := s.as.BotIntent().SendStateEvent(ctx, space, event.StateSpaceChild, child.String(), &event.SpaceChildEventContent{
		Via:       via,
		Suggested: suggested,
	})
	if err != nil {
		log.Printf("failed to add child %s to space %s: %v", child, space, err)
	}
}

func (s *Server) setSpaceParent(ctx context.Context, room, parent id.RoomID, canonical bool) {
	if room == "" || parent == "" {
		return
	}
	via := []string{s.cfg.Homeserver.Domain}
	_, err := s.as.BotIntent().SendStateEvent(ctx, room, event.StateSpaceParent, parent.String(), &event.SpaceParentEventContent{
		Via:       via,
		Canonical: canonical,
	})
	if err != nil {
		log.Printf("failed to set space parent %s on room %s: %v", parent, room, err)
	}
}

func (s *Server) removeSpaceChild(ctx context.Context, space, child id.RoomID) {
	if space == "" || child == "" {
		return
	}
	_, err := s.as.BotIntent().SendStateEvent(ctx, space, event.StateSpaceChild, child.String(), map[string]any{})
	if err != nil {
		log.Printf("failed to remove child %s from space %s: %v", child, space, err)
	}
}

func (s *Server) removeSpaceParent(ctx context.Context, room, parent id.RoomID) {
	if room == "" || parent == "" {
		return
	}
	_, err := s.as.BotIntent().SendStateEvent(ctx, room, event.StateSpaceParent, parent.String(), map[string]any{})
	if err != nil {
		log.Printf("failed to remove space parent %s from room %s: %v", parent, room, err)
	}
}
