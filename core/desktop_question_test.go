package core

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type desktopQuestionTestPlatform struct {
	*desktopJourneyPlatform
	question DesktopQuestionCard
}

func (p *desktopQuestionTestPlatform) SendDesktopQuestion(_ context.Context, _ any, q DesktopQuestionCard) (string, error) {
	p.question = q
	return "om_question", nil
}

func TestDesktopQuestionAPIUsesCapabilityAndLanguage(t *testing.T) {
	p := &desktopQuestionTestPlatform{desktopJourneyPlatform: &desktopJourneyPlatform{stubPlatformEngine: &stubPlatformEngine{n: "cards-test"}}}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangJapanese)
	t.Cleanup(func() { _ = e.Stop() })
	e.interactiveStates["cards-test:user"] = &interactiveState{platform: p, replyCtx: "ctx"}
	s := &APIServer{engines: map[string]*Engine{"test": e}}
	question := DesktopQuestionCard{Code: "Q-ABCDEF123456", Token: "12345678-1234-1234-1234-123456789012", Title: "Question"}
	body, _ := json.Marshal(map[string]any{"project": "test", "session_key": "cards-test:user", "question": question})
	rec := httptest.NewRecorder()
	s.handleDesktopQuestion(rec, httptest.NewRequest(http.MethodPost, "/desktop-question", bytes.NewReader(body)))
	if rec.Code != http.StatusOK || p.question.Language != LangJapanese || p.question.Code != question.Code {
		t.Fatalf("card request failed: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestDesktopQuestionAPIRejectsUnsupportedPlatform(t *testing.T) {
	p := &stubPlatformEngine{n: "text-only"}
	e := NewEngine("test", &stubAgent{}, []Platform{p}, "", LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	e.interactiveStates["text-only:user"] = &interactiveState{platform: p, replyCtx: "ctx"}
	s := &APIServer{engines: map[string]*Engine{"test": e}}
	body := []byte(`{"project":"test","session_key":"text-only:user","question":{"code":"Q-ABCDEF123456","token":"12345678-1234-1234-1234-123456789012","title":"Question"}}`)
	rec := httptest.NewRecorder()
	s.handleDesktopQuestion(rec, httptest.NewRequest(http.MethodPost, "/desktop-question", bytes.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unsupported platform accepted: status=%d", rec.Code)
	}
}

func TestDesktopQuestionTranslationsCoverSupportedLanguages(t *testing.T) {
	for key, translations := range messages {
		if len(key) < len("desktop_") || key[:len("desktop_")] != "desktop_" {
			continue
		}
		for _, language := range []Language{LangEnglish, LangChinese, LangTraditionalChinese, LangJapanese, LangSpanish} {
			if translations[language] == "" {
				t.Errorf("missing %s translation for %s", language, key)
			}
		}
	}
}
