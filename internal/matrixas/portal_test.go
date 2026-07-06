package matrixas

import (
	"context"
	"testing"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

type namingAPI struct {
	remoteapi.PuppetAPI
	contacts map[remoteapi.UserID]remoteapi.Contact
}

func (a namingAPI) Self(ctx context.Context) (remoteapi.UserID, error) { return "wxid_self", nil }
func (a namingAPI) ListContacts(ctx context.Context) ([]remoteapi.Contact, error) {
	out := make([]remoteapi.Contact, 0, len(a.contacts))
	for _, c := range a.contacts {
		out = append(out, c)
	}
	return out, nil
}
func (a namingAPI) GetContact(ctx context.Context, id remoteapi.UserID) (remoteapi.Contact, error) {
	if c, ok := a.contacts[id]; ok {
		return c, nil
	}
	return remoteapi.Contact{ID: id}, nil
}

func TestRoomMemberDisplayNameUsesRoomDisplayNameWithNickname(t *testing.T) {
	name := roomMemberDisplayName(remoteapi.Contact{
		Name:   "昵称",
		Remark: "备注",
		Extra:  map[string]any{"room_display_name": "群备注"},
	})
	if name != "群备注 [昵称]" {
		t.Fatalf("expected room display name with nickname, got %q", name)
	}
}

func TestRoomMemberDisplayNameUsesBareRoomDisplayNameWithoutNickname(t *testing.T) {
	name := roomMemberDisplayName(remoteapi.Contact{Extra: map[string]any{"room_display_name": "群备注"}})
	if name != "群备注" {
		t.Fatalf("expected bare room display name, got %q", name)
	}
}

func TestRoomMemberDisplayNameUsesBracketedNicknameWithoutDisplayName(t *testing.T) {
	name := roomMemberDisplayName(remoteapi.Contact{Name: "昵称", Remark: "备注"})
	if name != "[昵称]" {
		t.Fatalf("expected bracketed nickname fallback, got %q", name)
	}
}

func TestRoomMemberDisplayNameFallsBackToRemarkOnlyWhenNicknameMissing(t *testing.T) {
	if name := roomMemberDisplayName(remoteapi.Contact{Remark: "备注"}); name != "备注" {
		t.Fatalf("expected remark fallback, got %q", name)
	}
}

func TestPortalRoomNameUsesContactRemarkBeforeNickname(t *testing.T) {
	s := &Server{api: namingAPI{contacts: map[remoteapi.UserID]remoteapi.Contact{
		"wxid_friend": {ID: "wxid_friend", Remark: "朋友备注", Name: "朋友昵称"},
	}}}
	name := s.portalRoomName(context.Background(), remoteapi.Chat{ID: "wxid_friend", Name: "wxid_friend"})
	if name != "朋友备注" {
		t.Fatalf("expected remark as room name, got %q", name)
	}
}

func TestPortalRoomNameUsesContactNicknameBeforeWxid(t *testing.T) {
	s := &Server{api: namingAPI{contacts: map[remoteapi.UserID]remoteapi.Contact{
		"wxid_friend": {ID: "wxid_friend", Name: "朋友昵称"},
	}}}
	name := s.portalRoomName(context.Background(), remoteapi.Chat{ID: "wxid_friend"})
	if name != "朋友昵称" {
		t.Fatalf("expected nickname as room name, got %q", name)
	}
}

func TestPortalRoomNameUsesGroupName(t *testing.T) {
	s := &Server{api: namingAPI{}}
	name := s.portalRoomName(context.Background(), remoteapi.Chat{ID: "123@chatroom", Name: "群名称", IsGroup: true})
	if name != "群名称" {
		t.Fatalf("expected group name, got %q", name)
	}
}
