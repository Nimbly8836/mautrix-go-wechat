package wechat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/leat/mautrix-go-wechat/internal/remoteapi"
)

func TestGetChatUsesChatRoomMemberDetailDisplayNames(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Friend/GetContractDetail":
			_ = json.NewEncoder(w).Encode(map[string]any{"Code": 0, "Success": true, "Data": map[string]any{
				"UserName": "52192122268@chatroom",
				"NickName": "业主群",
				"NewChatroomData": map[string]any{"ChatRoomMember": []any{
					map[string]any{"UserName": "wxid_a"},
					map[string]any{"UserName": "wxid_b"},
				}},
			}})
		case "/Group/GetChatRoomInfo":
			_ = json.NewEncoder(w).Encode(map[string]any{"Code": 0, "Success": true, "Data": map[string]any{}})
		case "/Group/GetChatRoomMemberDetail":
			var req map[string]any
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			if req["Wxid"] != "wxid_self" || req["QID"] != "52192122268@chatroom" {
				t.Fatalf("unexpected member detail request: %#v", req)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"Code": 0, "Success": true, "Data": map[string]any{
				"ChatroomUserName": "52192122268@chatroom",
				"NewChatroomData": map[string]any{"ChatRoomMember": []any{
					map[string]any{"UserName": "wxid_a", "NickName": "文", "DisplayName": "朱 9-2-1001", "BigHeadImgUrl": "https://example.com/a/0"},
					map[string]any{"UserName": "wxid_b", "NickName": "undefined", "DisplayName": "9-2-502", "SmallHeadImgUrl": "https://example.com/b/132"},
				}},
			}})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	c := &Client{t: newTransport(server.URL), wxid: "wxid_self"}
	chat, err := c.GetChat(context.Background(), "52192122268@chatroom")
	if err != nil {
		t.Fatalf("GetChat failed: %v", err)
	}
	if len(chat.Members) != 2 || chat.Members[0] != "wxid_a" || chat.Members[1] != "wxid_b" {
		t.Fatalf("unexpected members: %#v", chat.Members)
	}
	contacts, ok := chat.Extra["member_contacts"].([]remoteapi.Contact)
	if !ok || len(contacts) != 2 {
		t.Fatalf("expected member contacts, got %#v", chat.Extra["member_contacts"])
	}
	if contacts[0].Name != "文" || contacts[0].Extra["room_display_name"] != "朱 9-2-1001" {
		t.Fatalf("unexpected first member contact: %#v", contacts[0])
	}
	if contacts[1].Name != "" || contacts[1].Extra["room_display_name"] != "9-2-502" {
		t.Fatalf("unexpected undefined nickname handling: %#v", contacts[1])
	}
}
