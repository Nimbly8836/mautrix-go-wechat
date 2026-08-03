package matrixas

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
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

type spaceTransitionResult struct {
	Space       id.RoomID
	Folders     map[string]id.RoomID
	PortalCount int
	BuiltinPack reconciliationResult
	AccountData accountDataVisibilityResult
	Warnings    []string
}

func (result *spaceTransitionResult) collectBuiltinWarnings() {
	result.Warnings = append(result.Warnings, result.BuiltinPack.Warnings...)
	result.Warnings = append(result.Warnings, result.AccountData.Warnings...)
}

func formatSpaceTransitionStatus(result spaceTransitionResult) string {
	status := fmt.Sprintf("Built-in emoji pack: %s (%d/%d rooms); personal account data: %s", result.BuiltinPack.Status, result.BuiltinPack.ReconciledRooms, result.BuiltinPack.AttemptedRooms, result.AccountData.Status)
	if result.AccountData.Room != "" {
		status += fmt.Sprintf(" via %s", result.AccountData.Room)
	}
	if len(result.Warnings) > 0 {
		status += "\nWarnings: " + strings.Join(result.Warnings, "; ")
	}
	return status
}

var accountSpaceFolders = []accountSpaceFolder{
	{Key: spaceFolderContacts, Name: "联系人", Topic: "WeChat one-to-one contact portals"},
	{Key: spaceFolderOfficial, Name: "公众号", Topic: "WeChat official account portals"},
	{Key: spaceFolderGroups, Name: "群聊", Topic: "WeChat group chat portals"},
}

func accountSpaceFolderNames() map[string]struct{} {
	names := make(map[string]struct{}, len(accountSpaceFolders))
	for _, folder := range accountSpaceFolders {
		names[folder.Name] = struct{}{}
	}
	return names
}

func uniquePortalRecords(portals []portalRecord) []portalRecord {
	byRoom := make(map[id.RoomID]portalRecord, len(portals))
	for _, portal := range portals {
		if portal.RoomID == "" {
			continue
		}
		if existing, ok := byRoom[portal.RoomID]; !ok || portal.ChatID < existing.ChatID {
			byRoom[portal.RoomID] = portal
		}
	}
	unique := make([]portalRecord, 0, len(byRoom))
	for _, portal := range byRoom {
		unique = append(unique, portal)
	}
	sort.Slice(unique, func(i, j int) bool { return unique[i].RoomID < unique[j].RoomID })
	return unique
}

func (s *Server) savedPortalRecords() []portalRecord {
	if s == nil || s.portals == nil {
		return nil
	}
	portals := uniquePortalRecords(s.portals.list())
	if s.afterPortalSnapshot != nil {
		s.afterPortalSnapshot(portals)
	}
	return portals
}

func duplicateAccountFolderChildren(folders map[string]id.RoomID, childNames map[id.RoomID]string, childSpaces map[id.RoomID]bool) []id.RoomID {
	current := map[id.RoomID]struct{}{}
	for _, room := range folders {
		if room != "" {
			current[room] = struct{}{}
		}
	}
	folderNames := accountSpaceFolderNames()
	duplicates := make([]id.RoomID, 0)
	for room, name := range childNames {
		if room == "" || !childSpaces[room] {
			continue
		}
		if _, isCurrent := current[room]; isCurrent {
			continue
		}
		if _, isFolderName := folderNames[strings.TrimSpace(name)]; isFolderName {
			duplicates = append(duplicates, room)
		}
	}
	sort.Slice(duplicates, func(i, j int) bool { return duplicates[i] < duplicates[j] })
	return duplicates
}

// ensureAccountSpace creates or returns the Matrix Space for the current WeChat
// account. The account space contains child spaces (folders) for contacts,
// official accounts and group chats.
func (s *Server) ensureAccountSpace(ctx context.Context, account remoteapi.UserID) (id.RoomID, error) {
	result, err := s.ensureAccountSpaceTransition(ctx, account)
	return result.Space, err
}

func (s *Server) ensureAccountSpaceTransition(ctx context.Context, account remoteapi.UserID) (spaceTransitionResult, error) {
	s.spaceMu.Lock()
	defer s.spaceMu.Unlock()
	return s.ensureAccountSpaceLocked(ctx, account)
}

func (s *Server) bootstrapNewAccountSpaceLocked(ctx context.Context, room id.RoomID, reconcileOthers bool, persist func() error) (reconciliationResult, accountDataVisibilityResult, error) {
	s.lockImagePack()
	defer s.packMu.Unlock()
	data, skipReason := s.builtinEmojiStoreSnapshot()
	packResult := reconciliationResult{Status: builtinPackStatusSkipped}
	accountData := accountDataVisibilityResult{Status: builtinPackStatusSkipped}
	transaction := accountDataWriteTransaction{}
	if skipReason != "" {
		packResult.Warnings = []string{skipReason}
	} else {
		if err := s.reconcileBuiltinEmojiRoomWithTimeoutLocked(ctx, room, data); err != nil {
			packResult = reconciliationResult{Status: builtinPackStatusFailed, AttemptedRooms: 1, FailedRooms: 1, Warnings: []string{fmt.Sprintf("room %s: %v", room, err)}}
			return packResult, accountData, fmt.Errorf("write built-in emoji packs to new Space %s: %w", room, err)
		}
		packResult = reconciliationResult{Status: builtinPackStatusSuccess, AttemptedRooms: 1, ReconciledRooms: 1}
		accountData, transaction = s.registerBuiltinPackAccountDataForSpacesTransactionalLocked(ctx, []id.RoomID{room})
	}
	if err := persist(); err != nil {
		rollbackFailures := s.rollbackBuiltinPackAccountDataLocked(ctx, &transaction)
		for _, rollbackFailure := range rollbackFailures {
			accountData.Warnings = append(accountData.Warnings, rollbackFailure.Error())
		}
		return packResult, accountData, errors.Join(append([]error{err}, rollbackFailures...)...)
	}
	s.finishBuiltinPackAccountDataTransactionLocked(&transaction)
	if skipReason == "" && reconcileOthers {
		packResult = mergeReconciliationResults(packResult, s.reconcileBuiltinEmojiPacksExcludingLocked(ctx, room))
	}
	return packResult, accountData, nil
}

func (s *Server) existingAccountSpaceStructuralLocked(ctx context.Context, account remoteapi.UserID) (spaceTransitionResult, bool, error) {
	result := spaceTransitionResult{Folders: map[string]id.RoomID{}}
	acc, ok := s.accounts.get(account)
	if !ok || acc.SpaceRoom == "" {
		return result, false, nil
	}
	result.Space = id.RoomID(acc.SpaceRoom)
	folders, err := s.ensureAccountSpaceFoldersLocked(ctx, account, result.Space)
	result.Folders = folders
	if err != nil {
		return result, true, err
	}
	if owner := s.portals.owner(); owner != "" {
		s.ensureRoomAdminPower(ctx, owner, result.Space)
	}
	result.PortalCount = len(s.savedPortalRecords())
	return result, true, nil
}

func (s *Server) ensureAccountSpaceStructuralLocked(ctx context.Context, account remoteapi.UserID) (spaceTransitionResult, error) {
	result := spaceTransitionResult{Folders: map[string]id.RoomID{}}
	if account == "" {
		return result, fmt.Errorf("empty account id")
	}
	if existing, ok, err := s.existingAccountSpaceStructuralLocked(ctx, account); ok || err != nil {
		return existing, err
	}
	owner := s.portals.owner()
	space, err := s.createAccountRootSpace(ctx, account, owner)
	if err != nil {
		return result, err
	}
	result.Space = space
	result.BuiltinPack, result.AccountData, err = s.bootstrapNewAccountSpaceLocked(ctx, space, false, func() error {
		return s.accounts.setSpace(account, space.String())
	})
	result.PortalCount = len(s.savedPortalRecords())
	result.collectBuiltinWarnings()
	if err != nil {
		rooms, cleanupFailures := s.cleanupCreatedAccountSpaces(ctx, space, nil, true)
		return result, createdSpacePersistenceError(fmt.Sprintf("persist account Space %s for %s", space, account), err, rooms, cleanupFailures)
	}
	if owner != "" {
		s.ensureRoomAdminPower(ctx, owner, space)
	}
	log.Printf("created account space: account=%s room=%s", account, space)
	notifyCtx, cancelNotify := s.matrixOperationContext(ctx)
	s.notifyManagement(notifyCtx, fmt.Sprintf("已创建微信账号 Space:\n- account: %s\n- space: %s", account, space))
	cancelNotify()
	result.Folders, err = s.ensureAccountSpaceFoldersLocked(ctx, account, space)
	return result, err
}

func (s *Server) ensureAccountSpaceLocked(ctx context.Context, account remoteapi.UserID) (spaceTransitionResult, error) {
	result := spaceTransitionResult{Folders: map[string]id.RoomID{}}
	if account == "" {
		return result, fmt.Errorf("empty account id")
	}
	if existing, ok, err := s.existingAccountSpaceStructuralLocked(ctx, account); ok || err != nil {
		result = existing
		if err != nil {
			return result, err
		}
		s.lockImagePack()
		result.BuiltinPack = s.reconcileBuiltinEmojiPacksLocked(ctx)
		result.AccountData = s.registerBuiltinPackAccountDataLocked(ctx)
		s.packMu.Unlock()
		result.collectBuiltinWarnings()
		return result, nil
	}
	owner := s.portals.owner()
	space, err := s.createAccountRootSpace(ctx, account, owner)
	if err != nil {
		return result, err
	}
	result.Space = space
	result.BuiltinPack, result.AccountData, err = s.bootstrapNewAccountSpaceLocked(ctx, space, true, func() error {
		return s.accounts.setSpace(account, space.String())
	})
	result.PortalCount = len(s.savedPortalRecords())
	result.collectBuiltinWarnings()
	if err != nil {
		rooms, cleanupFailures := s.cleanupCreatedAccountSpaces(ctx, space, nil, true)
		return result, createdSpacePersistenceError(fmt.Sprintf("persist account Space %s for %s", space, account), err, rooms, cleanupFailures)
	}
	if owner != "" {
		s.ensureRoomAdminPower(ctx, owner, space)
	}
	log.Printf("created account space: account=%s room=%s", account, space)
	notifyCtx, cancelNotify := s.matrixOperationContext(ctx)
	s.notifyManagement(notifyCtx, fmt.Sprintf("已创建微信账号 Space:\n- account: %s\n- space: %s", account, space))
	cancelNotify()
	result.Folders, err = s.ensureAccountSpaceFoldersLocked(ctx, account, space)
	return result, err
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
	operationCtx, cancel := s.matrixOperationContext(ctx)
	defer cancel()
	_, err := s.as.BotIntent().InviteUser(operationCtx, room, &mautrix.ReqInviteUser{
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
	loadCtx, cancelLoad := s.matrixOperationContext(ctx)
	loadErr := s.as.BotIntent().StateEvent(loadCtx, room, event.StatePowerLevels, "", &pl)
	cancelLoad()
	if loadErr != nil {
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
	setCtx, cancelSet := s.matrixOperationContext(ctx)
	_, err := s.as.BotIntent().SendStateEvent(setCtx, room, event.StatePowerLevels, "", &pl)
	cancelSet()
	if err != nil {
		log.Printf("failed to set admin power for %s in %s: %v", user, room, err)
	}
}

func (s *Server) ensureAccountSpaceVisibleTransition(ctx context.Context, account remoteapi.UserID, user id.UserID) (spaceTransitionResult, error) {
	s.spaceMu.Lock()
	defer s.spaceMu.Unlock()
	result, err := s.ensureAccountSpaceLocked(ctx, account)
	if err != nil {
		return result, err
	}
	s.inviteUserToRoom(ctx, result.Space, user)
	s.ensureRoomAdminPower(ctx, user, result.Space)
	s.repairStickerPackCompatibility(ctx, account, result.Space, user)
	for _, folder := range result.Folders {
		s.inviteUserToRoom(ctx, folder, user)
		s.ensureRoomAdminPower(ctx, user, folder)
	}
	return result, nil
}

func (s *Server) ensureAccountSpaceVisibleTo(ctx context.Context, account remoteapi.UserID, user id.UserID) (id.RoomID, map[string]id.RoomID, error) {
	result, err := s.ensureAccountSpaceVisibleTransition(ctx, account, user)
	return result.Space, result.Folders, err
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
			if err := s.accounts.setSpaceFolder(account, folder.Key, roomID.String()); err != nil {
				rooms, cleanupFailures := s.cleanupCreatedAccountSpaces(ctx, parent, map[string]id.RoomID{folder.Key: roomID}, false)
				return out, createdSpacePersistenceError(fmt.Sprintf("persist account folder Space %s (%s) for %s", folder.Key, roomID, account), err, rooms, cleanupFailures)
			}
			log.Printf("created account folder space: account=%s folder=%s room=%s", account, folder.Key, roomID)
		}
		out[folder.Key] = roomID
		if owner := s.portals.owner(); owner != "" {
			s.ensureRoomAdminPower(ctx, owner, roomID)
		}
		nameCtx, cancelName := s.matrixOperationContext(ctx)
		_, nameErr := s.as.BotIntent().SetRoomName(nameCtx, roomID, folder.Name)
		cancelName()
		if nameErr != nil {
			log.Printf("failed to update account folder space name: account=%s folder=%s room=%s: %v", account, folder.Key, roomID, nameErr)
		}
		topicCtx, cancelTopic := s.matrixOperationContext(ctx)
		_, topicErr := s.as.BotIntent().SetRoomTopic(topicCtx, roomID, folder.Topic)
		cancelTopic()
		if topicErr != nil {
			log.Printf("failed to update account folder space topic: account=%s folder=%s room=%s: %v", account, folder.Key, roomID, topicErr)
		}
		s.addSpaceChild(ctx, parent, roomID, true)
		s.setSpaceParent(ctx, roomID, parent, false)
	}
	s.cleanupDuplicateAccountFolderChildren(ctx, parent, out)
	return out, nil
}

func (s *Server) cleanupDuplicateAccountFolderChildren(ctx context.Context, parent id.RoomID, folders map[string]id.RoomID) {
	if parent == "" || s == nil || s.as == nil || s.as.BotIntent() == nil || s.as.BotIntent().Client == nil {
		return
	}
	stateCtx, cancelState := s.matrixOperationContext(ctx)
	state, err := s.as.BotIntent().Client.StateAsArray(stateCtx, parent)
	cancelState()
	if err != nil {
		log.Printf("failed to fetch account space state for duplicate folder cleanup space=%s: %v", parent, err)
		return
	}
	childNames := map[id.RoomID]string{}
	childSpaces := map[id.RoomID]bool{}
	for _, evt := range state {
		if evt == nil || evt.Type != event.StateSpaceChild || evt.StateKey == nil || *evt.StateKey == "" {
			continue
		}
		child := id.RoomID(*evt.StateKey)
		var createContent event.CreateEventContent
		createCtx, cancelCreate := s.matrixOperationContext(ctx)
		createErr := s.as.BotIntent().StateEvent(createCtx, child, event.StateCreate, "", &createContent)
		cancelCreate()
		if createErr != nil || createContent.Type != event.RoomTypeSpace {
			continue
		}
		var nameContent event.RoomNameEventContent
		nameCtx, cancelName := s.matrixOperationContext(ctx)
		nameErr := s.as.BotIntent().StateEvent(nameCtx, child, event.StateRoomName, "", &nameContent)
		cancelName()
		if nameErr != nil {
			continue
		}
		childNames[child] = nameContent.Name
		childSpaces[child] = true
	}
	for _, duplicate := range duplicateAccountFolderChildren(folders, childNames, childSpaces) {
		s.removeSpaceChild(ctx, parent, duplicate)
		s.removeSpaceParent(ctx, duplicate, parent)
		log.Printf("removed duplicate account folder space from account space: parent=%s duplicate=%s name=%q", parent, duplicate, childNames[duplicate])
	}
}

func (s *Server) createAccountRootSpace(ctx context.Context, account remoteapi.UserID, owner id.UserID) (id.RoomID, error) {
	invite := []id.UserID{}
	if owner != "" {
		invite = append(invite, owner)
	}
	name := fmt.Sprintf("WeChat %s", account)
	operationCtx, cancel := s.matrixOperationContext(ctx)
	defer cancel()
	resp, err := s.as.BotIntent().CreateRoom(operationCtx, &mautrix.ReqCreateRoom{
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
	operationCtx, cancel := s.matrixOperationContext(ctx)
	defer cancel()
	resp, err := s.as.BotIntent().CreateRoom(operationCtx, &mautrix.ReqCreateRoom{
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

func (s *Server) attachPortalToKnownAccountSpaceLocked(ctx context.Context, space id.RoomID, folders map[string]id.RoomID, portal portalRecord) {
	folderSpace := folders[s.portalSpaceFolder(ctx, portal)]
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

func (s *Server) finishPortalAccountSpaceAttachment(ctx context.Context, account remoteapi.UserID, space id.RoomID, portal portalRecord) {
	for _, room := range stickerPackCleanupRooms(space, portal.RoomID) {
		s.removeStickerPackFromRoom(ctx, account, room)
	}
	s.mirrorStickerPackToElementPortal(ctx, account, space, portal.RoomID)
}

func (s *Server) attachPortalsToKnownAccountSpace(ctx context.Context, account remoteapi.UserID, space id.RoomID, folders map[string]id.RoomID, portals []portalRecord) error {
	if space == "" || len(portals) == 0 {
		return nil
	}
	if owner := s.portals.owner(); owner != "" {
		for _, portal := range portals {
			s.ensureRoomAdminPower(ctx, owner, portal.RoomID)
		}
	}
	s.spaceMu.Lock()
	current, ok := s.accounts.get(account)
	if !ok || id.RoomID(current.SpaceRoom) != space {
		s.spaceMu.Unlock()
		return fmt.Errorf("stale account Space transition for %s: captured=%s current=%s", account, space, current.SpaceRoom)
	}
	for _, portal := range portals {
		s.attachPortalToKnownAccountSpaceLocked(ctx, space, folders, portal)
	}
	s.spaceMu.Unlock()
	for _, portal := range portals {
		s.finishPortalAccountSpaceAttachment(ctx, account, space, portal)
	}
	return nil
}

func (s *Server) attachExistingPortalsToAccountSpace(ctx context.Context, account remoteapi.UserID, space id.RoomID, folders map[string]id.RoomID, portals []portalRecord) error {
	return s.attachPortalsToKnownAccountSpace(ctx, account, space, folders, portals)
}

func (s *Server) rebuildAccountSpace(ctx context.Context, account remoteapi.UserID, owner id.UserID) (spaceTransitionResult, error) {
	result := spaceTransitionResult{Folders: map[string]id.RoomID{}}
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
		return result, err
	}
	result.Space = space
	folders := map[string]id.RoomID{}
	folderStrings := map[string]string{}
	for _, folder := range accountSpaceFolders {
		room, createErr := s.createAccountFolderSpace(ctx, account, folder)
		if createErr != nil {
			rooms, cleanupFailures := s.cleanupCreatedAccountSpaces(ctx, space, folders, true)
			s.spaceMu.Unlock()
			return result, createdSpacePersistenceError(fmt.Sprintf("create rebuilt account folder Space %s for %s", folder.Key, account), createErr, rooms, cleanupFailures)
		}
		folders[folder.Key] = room
		folderStrings[folder.Key] = room.String()
		s.addSpaceChild(ctx, space, room, true)
		s.setSpaceParent(ctx, room, space, false)
		s.ensureRoomAdminPower(ctx, owner, room)
	}
	result.Folders = folders

	s.lockImagePack()
	data, skipReason := s.builtinEmojiStoreSnapshot()
	result.AccountData = accountDataVisibilityResult{Status: builtinPackStatusSkipped}
	if skipReason != "" {
		result.BuiltinPack = reconciliationResult{Status: builtinPackStatusSkipped, Warnings: []string{skipReason}}
		result.AccountData.Warnings = []string{skipReason}
	} else {
		if packErr := s.reconcileBuiltinEmojiRoomWithTimeoutLocked(ctx, space, data); packErr != nil {
			result.BuiltinPack = reconciliationResult{Status: builtinPackStatusFailed, AttemptedRooms: 1, FailedRooms: 1, Warnings: []string{fmt.Sprintf("room %s: %v", space, packErr)}}
			s.packMu.Unlock()
			result.collectBuiltinWarnings()
			rooms, cleanupFailures := s.cleanupCreatedAccountSpaces(ctx, space, folders, true)
			s.spaceMu.Unlock()
			cause := fmt.Errorf("write built-in emoji packs to rebuilt Space %s: %w", space, packErr)
			return result, createdSpacePersistenceError(fmt.Sprintf("prepare rebuilt account Space for %s", account), cause, rooms, cleanupFailures)
		}
		result.BuiltinPack = reconciliationResult{Status: builtinPackStatusSuccess, AttemptedRooms: 1, ReconciledRooms: 1}
	}
	transaction := accountDataWriteTransaction{}
	if skipReason == "" {
		result.AccountData, transaction = s.registerBuiltinPackAccountDataForSpacesTransactionalExcludingLocked(ctx, []id.RoomID{space}, []id.RoomID{oldSpace})
		if oldSpace != space {
			s.retireBuiltinPackAccountDataSourceLocked(ctx, oldSpace, &result.AccountData, &transaction)
		}
	}
	if err = s.accounts.replaceSpace(account, space.String(), folderStrings); err != nil {
		rollbackFailures := s.rollbackBuiltinPackAccountDataLocked(ctx, &transaction)
		for _, rollbackFailure := range rollbackFailures {
			result.AccountData.Warnings = append(result.AccountData.Warnings, rollbackFailure.Error())
		}
		s.packMu.Unlock()
		result.collectBuiltinWarnings()
		rooms, cleanupFailures := s.cleanupCreatedAccountSpaces(ctx, space, folders, true)
		s.spaceMu.Unlock()
		cause := errors.Join(append([]error{err}, rollbackFailures...)...)
		return result, createdSpacePersistenceError(fmt.Sprintf("persist rebuilt account Space for %s", account), cause, rooms, cleanupFailures)
	}
	s.finishBuiltinPackAccountDataTransactionLocked(&transaction)
	if skipReason == "" {
		result.BuiltinPack = mergeReconciliationResults(result.BuiltinPack, s.reconcileBuiltinEmojiPacksExcludingLocked(ctx, space))
		finalAccountData := s.reconcileBuiltinPackAccountDataLocked(ctx)
		result.AccountData = mergeAccountDataVisibilityResults(result.AccountData, finalAccountData)
	}
	s.packMu.Unlock()
	result.collectBuiltinWarnings()
	s.ensureRoomAdminPower(ctx, owner, space)
	s.spaceMu.Unlock()

	portals := s.savedPortalRecords()
	result.PortalCount = len(portals)
	for _, folder := range oldFolders {
		s.removeSpaceChild(ctx, oldSpace, folder)
		s.removeSpaceParent(ctx, folder, oldSpace)
	}
	for _, portal := range portals {
		s.removeSpaceChild(ctx, oldSpace, portal.RoomID)
		s.removeSpaceParent(ctx, portal.RoomID, oldSpace)
		for _, oldFolder := range oldFolders {
			s.removeSpaceChild(ctx, oldFolder, portal.RoomID)
			s.removeSpaceParent(ctx, portal.RoomID, oldFolder)
		}
	}
	if err = s.attachPortalsToKnownAccountSpace(ctx, account, space, folders, portals); err != nil {
		return result, err
	}
	for _, portal := range portals {
		s.ensureRoomAdminPower(ctx, owner, portal.RoomID)
	}
	return result, nil
}

func (s *Server) repairAccountSpace(ctx context.Context, account remoteapi.UserID, owner id.UserID) (spaceTransitionResult, error) {
	result, err := s.ensureAccountSpaceVisibleTransition(ctx, account, owner)
	if err != nil {
		return result, err
	}
	s.ensureRoomAdminPower(ctx, owner, result.Space)
	for _, folder := range result.Folders {
		s.ensureRoomAdminPower(ctx, owner, folder)
	}
	portals := s.savedPortalRecords()
	result.PortalCount = len(portals)
	if err = s.attachPortalsToKnownAccountSpace(ctx, account, result.Space, result.Folders, portals); err != nil {
		return result, err
	}
	for _, portal := range portals {
		s.ensureRoomAdminPower(ctx, owner, portal.RoomID)
	}
	s.repairStickerPacksForAccount(ctx, account, result.Space, owner)
	return result, nil
}

func (s *Server) addPortalToAccountSpace(ctx context.Context, account remoteapi.UserID, portal portalRecord) error {
	if account == "" || portal.RoomID == "" {
		return nil
	}
	if owner := s.portals.owner(); owner != "" {
		s.ensureRoomAdminPower(ctx, owner, portal.RoomID)
	}
	s.spaceMu.Lock()
	result, err := s.ensureAccountSpaceStructuralLocked(ctx, account)
	if err != nil {
		s.spaceMu.Unlock()
		return fmt.Errorf("ensure Space for account %s: %w", account, err)
	}
	space := result.Space
	s.attachPortalToKnownAccountSpaceLocked(ctx, space, result.Folders, portal)
	s.spaceMu.Unlock()

	s.finishPortalAccountSpaceAttachment(ctx, account, space, portal)
	return nil
}

func (s *Server) portalSpaceFolder(ctx context.Context, portal portalRecord) string {
	chatID := portal.ChatID
	if strings.HasSuffix(string(chatID), "@chatroom") {
		return spaceFolderGroups
	}
	if s.api != nil && chatID != "" {
		operationCtx, cancel := s.matrixOperationContext(ctx)
		chat, err := s.api.GetChat(operationCtx, chatID)
		cancel()
		if err == nil {
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
	operationCtx, cancel := s.matrixOperationContext(ctx)
	defer cancel()
	_, err := s.as.BotIntent().SendStateEvent(operationCtx, space, event.StateSpaceChild, child.String(), &event.SpaceChildEventContent{
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
	operationCtx, cancel := s.matrixOperationContext(ctx)
	defer cancel()
	_, err := s.as.BotIntent().SendStateEvent(operationCtx, room, event.StateSpaceParent, parent.String(), &event.SpaceParentEventContent{
		Via:       via,
		Canonical: canonical,
	})
	if err != nil {
		log.Printf("failed to set space parent %s on room %s: %v", parent, room, err)
	}
}

func (s *Server) removeSpaceChild(ctx context.Context, space, child id.RoomID) {
	if err := s.removeSpaceChildWithError(ctx, space, child); err != nil {
		log.Printf("failed to remove child %s from space %s: %v", child, space, err)
	}
}

func (s *Server) removeSpaceChildWithError(ctx context.Context, space, child id.RoomID) error {
	if space == "" || child == "" {
		return nil
	}
	operationCtx, cancel := s.matrixOperationContext(ctx)
	defer cancel()
	_, err := s.as.BotIntent().SendStateEvent(operationCtx, space, event.StateSpaceChild, child.String(), map[string]any{})
	return err
}

func (s *Server) removeSpaceParent(ctx context.Context, room, parent id.RoomID) {
	if err := s.removeSpaceParentWithError(ctx, room, parent); err != nil {
		log.Printf("failed to remove space parent %s from room %s: %v", parent, room, err)
	}
}

func (s *Server) removeSpaceParentWithError(ctx context.Context, room, parent id.RoomID) error {
	if room == "" || parent == "" {
		return nil
	}
	operationCtx, cancel := s.matrixOperationContext(ctx)
	defer cancel()
	_, err := s.as.BotIntent().SendStateEvent(operationCtx, room, event.StateSpaceParent, parent.String(), map[string]any{})
	return err
}

// Matrix rooms cannot be deleted. When persistence fails after room creation,
// cleanup is therefore best-effort: remove links and make the bot leave so an
// untracked room is not reused by a later retry.
func (s *Server) cleanupCreatedAccountSpaces(ctx context.Context, root id.RoomID, folders map[string]id.RoomID, leaveRoot bool) ([]id.RoomID, []error) {
	rooms := make([]id.RoomID, 0, len(folders)+1)
	seen := map[id.RoomID]bool{}
	for _, folder := range accountSpaceFolders {
		room := folders[folder.Key]
		if room == "" || seen[room] {
			continue
		}
		seen[room] = true
		rooms = append(rooms, room)
	}
	extraKeys := make([]string, 0, len(folders))
	for key, room := range folders {
		if room != "" && !seen[room] {
			extraKeys = append(extraKeys, key)
		}
	}
	sort.Strings(extraKeys)
	for _, key := range extraKeys {
		room := folders[key]
		seen[room] = true
		rooms = append(rooms, room)
	}

	failures := make([]error, 0)
	for _, room := range rooms {
		if root != "" {
			if err := s.removeSpaceChildWithError(ctx, root, room); err != nil {
				failures = append(failures, fmt.Errorf("remove child link %s -> %s: %w", root, room, err))
			}
			if err := s.removeSpaceParentWithError(ctx, room, root); err != nil {
				failures = append(failures, fmt.Errorf("remove parent link %s -> %s: %w", room, root, err))
			}
		}
	}
	if leaveRoot && root != "" && !seen[root] {
		rooms = append(rooms, root)
	}
	for _, room := range rooms {
		leaveCtx, cancelLeave := s.matrixOperationContext(context.WithoutCancel(ctx))
		_, err := s.as.BotIntent().LeaveRoom(leaveCtx, room)
		cancelLeave()
		if err != nil {
			failures = append(failures, fmt.Errorf("bot leave %s: %w", room, err))
		}
	}
	log.Printf("best-effort Matrix cleanup after account Space failure (rooms cannot be deleted): rooms=%v failures=%v", rooms, failures)
	return rooms, failures
}

func createdSpacePersistenceError(action string, cause error, rooms []id.RoomID, cleanupFailures []error) error {
	roomNames := make([]string, len(rooms))
	for i, room := range rooms {
		roomNames[i] = room.String()
	}
	cleanup := "best-effort links removed and bot left rooms; Matrix rooms cannot be deleted"
	if len(cleanupFailures) > 0 {
		failureNames := make([]string, len(cleanupFailures))
		for i, failure := range cleanupFailures {
			failureNames[i] = failure.Error()
		}
		cleanup += "; cleanup failures: " + strings.Join(failureNames, "; ")
	}
	return fmt.Errorf("%s: %w; created Matrix room IDs: [%s]; cleanup: %s", action, cause, strings.Join(roomNames, ", "), cleanup)
}
