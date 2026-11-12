package feishu

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chenhg5/cc-connect/core"
	lark "github.com/larksuite/oapi-sdk-go/v3"
)

func TestDesktopNotificationQuoteRoutingIsOptIn(t *testing.T) {
	p := &Platform{platformName: "feishu"}
	if p.handleDesktopNotificationReply(context.Background(), "text", `{"text":"ordinary reply"}`, nil, "om_reply", "key", "user", replyContext{}, "om_parent") {
		t.Fatal("without a desktop hook, quoted replies must retain normal behavior")
	}
}

func TestDesktopNotificationQuoteNeverStartsOrdinaryAgent(t *testing.T) {
	for _, scenario := range []struct {
		name, senderType, senderID, text string
		missing, handled                 bool
	}{
		{"desktop completion", "app", "cli_test", "[Codex · 回复完成]\nID：desktop-session\nQuestion", false, true},
		{"old card", "app", "cli_test", "Codex · 等待你的回答", false, true},
		{"spoofed user", "user", "ou_owner", "[Codex · 回复完成]\nID：desktop-session", false, false},
		{"other bot", "app", "cli_other", "[Codex · 回复完成]", false, false},
		{"ordinary quote", "app", "cli_test", "ordinary answer", false, false},
		{"unverifiable quote", "app", "cli_test", "", true, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var incoming []*core.Message
			var replies int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.Contains(r.URL.Path, "tenant_access_token") {
					_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "expire": 7200, "tenant_access_token": "fake"})
					return
				}
				if r.Method == http.MethodPost {
					replies++
					_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"message_id": "om_error"}})
					return
				}
				if scenario.missing {
					_ = json.NewEncoder(w).Encode(map[string]any{"code": 1})
					return
				}
				content, _ := json.Marshal(map[string]string{"text": scenario.text})
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"items": []any{map[string]any{"message_id": "om_parent", "msg_type": "text", "sender": map[string]string{"id": scenario.senderID, "sender_type": scenario.senderType}, "body": map[string]string{"content": string(content)}}}}})
			}))
			defer srv.Close()
			p := &Platform{platformName: "feishu", appID: "cli_test", appSecret: "fake", domain: srv.URL, botOpenID: "ou_bot", client: lark.NewClient("cli_test", "fake", lark.WithEnableTokenCache(false), lark.WithOpenBaseUrl(srv.URL), lark.WithHttpClient(srv.Client())), handler: func(_ core.Platform, m *core.Message) { incoming = append(incoming, m) }}
			p.self = p
			p.EnableDesktopNotificationReplies(true)
			handled := p.handleDesktopNotificationReply(context.Background(), "text", `{"text":"use the ledger"}`, nil, "om_reply", "feishu:oc_chat:ou_owner", "ou_owner", replyContext{chatID: "oc_chat"}, "om_parent")
			if handled != scenario.handled {
				t.Fatalf("handled=%v want=%v", handled, scenario.handled)
			}
			if scenario.handled && !scenario.missing {
				if len(incoming) != 1 || incoming[0].DesktopReply == nil || incoming[0].Content != "" || incoming[0].DesktopReply.ParentMessageID != "om_parent" {
					t.Fatal("desktop quote fell through to ordinary agent prompt")
				}
			}
			if scenario.missing && (len(incoming) != 0 || replies != 1) {
				t.Fatal("unverifiable quote must fail visibly without dispatch")
			}
			if !scenario.handled && len(incoming) != 0 {
				t.Fatal("unrelated quotes must use normal dispatch")
			}
		})
	}
}
