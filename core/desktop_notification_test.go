package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type desktopNoticeTestPlatform struct {
	*desktopJourneyPlatform
	enabled bool
	message string
}

func (p *desktopNoticeTestPlatform) EnableDesktopNotificationReplies(v bool) { p.enabled = v }
func (p *desktopNoticeTestPlatform) SendDesktopNotification(_ context.Context, _ any, n DesktopNotification) (string, error) {
	p.message = n.Text
	return "om_notice", nil
}

func TestDesktopNotificationHookConfiguresQuoteRouting(t *testing.T) {
	p := &desktopNoticeTestPlatform{desktopJourneyPlatform: &desktopJourneyPlatform{stubPlatformEngine: &stubPlatformEngine{n: "notice-test"}}}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	e.SetHooks(NewHookManager("test", []HookConfig{{Event: "desktop.answer", Type: "command", Command: "handler"}}, "", "", ""))
	if !p.enabled {
		t.Fatal("exact desktop hook must enable quote routing")
	}
	e.SetHooks(nil)
	if p.enabled {
		t.Fatal("removing desktop hook must restore ordinary quote behavior")
	}
}

func TestDesktopNotificationAPIKeepsExactMessageBinding(t *testing.T) {
	p := &desktopNoticeTestPlatform{desktopJourneyPlatform: &desktopJourneyPlatform{stubPlatformEngine: &stubPlatformEngine{n: "notice-test"}}}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	e.interactiveStates["notice-test:user"] = &interactiveState{platform: p, replyCtx: "ctx"}
	s := &APIServer{engines: map[string]*Engine{"test": e}}
	req := httptest.NewRequest(http.MethodPost, "/desktop-notification", strings.NewReader(`{"project":"test","session_key":"notice-test:user","notice":{"code":"N-0123456789ABCDEF","text":"original desktop final"}}`))
	rec := httptest.NewRecorder()
	s.handleDesktopNotification(rec, req)
	var response map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &response)
	if rec.Code != http.StatusOK || response["message_id"] != "om_notice" || p.message != "original desktop final" {
		t.Fatal("notification did not return the original message binding")
	}
}
