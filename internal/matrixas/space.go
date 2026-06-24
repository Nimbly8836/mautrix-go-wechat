package matrixas

import (
	"context"
	"fmt"
	"log"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

// ensureAccountSpace creates or returns the Matrix Space for the current WeChat
// account. The space is one-per-wxid and contains that account's portal rooms.
func (s *Server) ensureAccountSpace(ctx context.Context, account remoteapi.UserID) (id.RoomID, error) {
	if account == "" {
		return "", fmt.Errorf("empty account id")
	}
	if acc, ok := s.accounts.get(account); ok && acc.SpaceRoom != "" {
		space := id.RoomID(acc.SpaceRoom)
		s.attachExistingPortalsToSpace(ctx, space)
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
	log.Printf("created account space: account=%s room=%s", account, resp.RoomID)
	s.notifyManagement(ctx, fmt.Sprintf("已创建微信账号 Space:\n- account: %s\n- space: %s", account, resp.RoomID))
	s.attachExistingPortalsToSpace(ctx, resp.RoomID)
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

func (s *Server) attachExistingPortalsToSpace(ctx context.Context, space id.RoomID) {
	if space == "" || s.portals == nil {
		return
	}
	for _, portal := range s.portals.list() {
		s.addPortalToSpace(ctx, space, portal)
	}
}

func (s *Server) addPortalToAccountSpace(ctx context.Context, account remoteapi.UserID, portal portalRecord) {
	if account == "" || portal.RoomID == "" {
		return
	}
	space, err := s.ensureAccountSpace(ctx, account)
	if err != nil {
		log.Printf("failed to ensure space for account %s: %v", account, err)
		return
	}
	s.addPortalToSpace(ctx, space, portal)
}

func (s *Server) addPortalToSpace(ctx context.Context, space id.RoomID, portal portalRecord) {
	if space == "" || portal.RoomID == "" {
		return
	}
	via := []string{s.cfg.Homeserver.Domain}
	_, err := s.as.BotIntent().SendStateEvent(ctx, space, event.StateSpaceChild, portal.RoomID.String(), &event.SpaceChildEventContent{
		Via:       via,
		Suggested: true,
	})
	if err != nil {
		log.Printf("failed to add portal %s to space %s: %v", portal.RoomID, space, err)
	}
	_, err = s.as.BotIntent().SendStateEvent(ctx, portal.RoomID, event.StateSpaceParent, space.String(), &event.SpaceParentEventContent{
		Via:       via,
		Canonical: true,
	})
	if err != nil {
		log.Printf("failed to set space parent %s on portal %s: %v", space, portal.RoomID, err)
	}
}
