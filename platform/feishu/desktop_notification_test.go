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

func TestDesktopNotificationCardRendersMarkdown(t *testing.T) {
	notice := core.DesktopNotification{
		Code: "N-0123456789ABCDEF",
		Text: "[Codex · 回复完成]\n\n**粗体**\n> 引用内容\n![本地截图](/C:/temp/screenshot.png)\n[查看文件](/E:/project/report.md)",
	}
	card := desktopNotificationCardMap(notice)
	if card["schema"] != "2.0" {
		t.Fatalf("schema = %v, want 2.0", card["schema"])
	}
	body, ok := card["body"].(map[string]any)
	if !ok {
		t.Fatalf("body = %#v, want card body", card["body"])
	}
	elements, ok := body["elements"].([]map[string]any)
	if !ok || len(elements) != 1 {
		t.Fatalf("elements = %#v, want one markdown element", body["elements"])
	}
	element := elements[0]
	if element["tag"] != "markdown" {
		t.Fatalf("tag = %v, want markdown", element["tag"])
	}
	content, _ := element["content"].(string)
	for _, want := range []string{"**粗体**", "> 引用内容", "查看文件", "/E:/project/report.md"} {
		if !strings.Contains(content, want) {
			t.Fatalf("markdown content %q missing %q", content, want)
		}
	}
	if strings.Contains(content, "![本地截图]") {
		t.Fatalf("local image markdown must not produce a broken Feishu image: %q", content)
	}
}

func TestDesktopNotificationMarkdownCardKeepsQuoteRouting(t *testing.T) {
	card := desktopNotificationCardMap(core.DesktopNotification{
		Code: "N-0123456789ABCDEF",
		Text: "[Codex · 回复完成]\n\n请选择：**A** 或 **B**",
	})
	raw, err := json.Marshal(card)
	if err != nil {
		t.Fatal(err)
	}
	text := extractInteractiveCardText(string(raw))
	if !isDesktopNotificationText(text) {
		t.Fatalf("markdown desktop notice is no longer recognizable as a quote target: %q", text)
	}
}

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
