package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"
)

type DesktopQuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}
type DesktopQuestionCard struct {
	Language     Language                `json:"language,omitempty"`
	Code         string                  `json:"code"`
	Token        string                  `json:"token"`
	Title        string                  `json:"title"`
	Conversation string                  `json:"conversation,omitempty"`
	Directory    string                  `json:"directory,omitempty"`
	Options      []DesktopQuestionOption `json:"options,omitempty"`
}
type DesktopCardSubmission struct {
	Code      string `json:"code"`
	Token     string `json:"token"`
	Option    string `json:"option,omitempty"`
	Text      string `json:"text,omitempty"`
	MessageID string `json:"message_id"`
}
type DesktopAnswerReceipt struct {
	Language      Language `json:"language,omitempty"`
	OK            bool     `json:"ok"`
	Message       string   `json:"message"`
	Status        string   `json:"status,omitempty"`
	Code          string   `json:"code,omitempty"`
	Question      string   `json:"question,omitempty"`
	Answer        string   `json:"answer,omitempty"`
	CardMessageID string   `json:"cardMessageId,omitempty"`
}
type DesktopQuestionPlatform interface {
	SendDesktopQuestion(context.Context, any, DesktopQuestionCard) (string, error)
	UpdateDesktopQuestion(context.Context, string, DesktopAnswerReceipt) error
}

var desktopQuestionCode = regexp.MustCompile(`^Q-[A-F0-9]{12}$`)

func (q DesktopQuestionCard) Validate() error {
	if !desktopQuestionCode.MatchString(q.Code) || len(q.Token) < 24 || len(q.Token) > 80 || strings.TrimSpace(q.Title) == "" || len(q.Title) > 16000 || len(q.Options) > 20 {
		return fmt.Errorf("invalid desktop question")
	}
	for _, option := range q.Options {
		if strings.TrimSpace(option.Label) == "" || len(option.Label) > 1000 || len(option.Description) > 4000 {
			return fmt.Errorf("invalid question option")
		}
	}
	return nil
}
func (s *APIServer) handleDesktopQuestion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Project    string              `json:"project"`
		SessionKey string              `json:"session_key"`
		Question   DesktopQuestionCard `json:"question"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 128<<10)).Decode(&req); err != nil {
		http.Error(w, "invalid question JSON", http.StatusBadRequest)
		return
	}
	if err := req.Question.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.RLock()
	e := s.engines[req.Project]
	s.mu.RUnlock()
	if e == nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}
	target, err := e.resolveSendTarget(req.SessionKey, false)
	if err != nil {
		http.Error(w, "session unavailable", http.StatusBadRequest)
		return
	}
	platform, ok := target.platform.(DesktopQuestionPlatform)
	if !ok {
		http.Error(w, "question cards unavailable", http.StatusBadRequest)
		return
	}
	if err = e.waitOutgoing(target.platform); err != nil {
		http.Error(w, "outgoing rate limited", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	req.Question.Language = e.i18n.CurrentLang()
	id, err := platform.SendDesktopQuestion(ctx, target.replyCtx, req.Question)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	apiJSON(w, http.StatusOK, map[string]string{"message_id": id})
}
func (e *Engine) dispatchDesktopCardAnswer(p Platform, msg *Message) {
	submission := msg.DesktopAnswer
	if submission == nil {
		return
	}
	e.userRolesMu.RLock()
	disabled := e.disabledCmds["answer"]
	roles := e.userRoles
	e.userRolesMu.RUnlock()
	if role := roles.ResolveRole(msg.UserID); role != nil {
		disabled = role.DisabledCmds["answer"]
	}
	if disabled {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgCommandDisabled, "/answer"))
		return
	}
	slog.Info("desktop card answer received", "project", e.name, "code", submission.Code)
	go func() {
		body, err := e.hooks.callDesktopAnswer(HookEvent{Event: "desktop.answer", Project: e.name, Timestamp: time.Now(), SessionKey: msg.SessionKey, Platform: msg.Platform, UserID: msg.UserID, Extra: map[string]any{"source": "card", "code": submission.Code, "token": submission.Token, "option": submission.Option, "text": submission.Text, "message_id": submission.MessageID}})
		e.finishDesktopAnswer(p, msg, body, err)
	}()
}
func (e *Engine) finishDesktopAnswer(p Platform, msg *Message, body string, err error) {
	if err != nil {
		slog.Warn("desktop answer bridge failed", "project", e.name)
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgDesktopBridgeUnavailable))
		return
	}
	var receipt DesktopAnswerReceipt
	if json.Unmarshal([]byte(body), &receipt) != nil || receipt.Message == "" {
		e.reply(p, msg.ReplyCtx, e.i18n.T(MsgDesktopInvalidReceipt))
		return
	}
	receipt.Language = e.i18n.CurrentLang()
	if receipt.CardMessageID != "" && (receipt.Status == "submitted" || receipt.Status == "resolved" || receipt.Status == "expired" || receipt.Status == "uncertain") {
		if platform, ok := p.(DesktopQuestionPlatform); ok {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := platform.UpdateDesktopQuestion(ctx, receipt.CardMessageID, receipt); err == nil {
				return
			}
			slog.Warn("desktop question card update failed", "project", e.name, "code", receipt.Code)
		}
	}
	e.reply(p, msg.ReplyCtx, receipt.Message)
}
