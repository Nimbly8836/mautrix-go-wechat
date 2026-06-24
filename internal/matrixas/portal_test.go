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

func (a namingAPI) GetContact(ctx context.Context, id remoteapi.UserID) (remoteapi.Contact, error) {
	if c, ok := a.contacts[id]; ok {
		return c, nil
	}
	return remoteapi.Contact{ID: id}, nil
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
