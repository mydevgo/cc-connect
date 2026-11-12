package core

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// DesktopNotification binds an outbound desktop notice to a durable local
// record. Its returned platform message ID is the only routing key for replies.
type DesktopNotification struct {
	Code string `json:"code"`
	Text string `json:"text"`
}

type DesktopNotificationPlatform interface {
	SendDesktopNotification(context.Context, any, DesktopNotification) (string, error)
}

type DesktopNotificationReplyConfigurer interface {
	EnableDesktopNotificationReplies(bool)
}

type DesktopNotificationReply struct {
	ParentMessageID string
	Text            string
}

var desktopNoticeCode = regexp.MustCompile(`^N-[A-F0-9]{16}$`)

func (s *APIServer) handleDesktopNotification(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Project    string              `json:"project"`
		SessionKey string              `json:"session_key"`
		Notice     DesktopNotification `json:"notice"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 128<<10)).Decode(&req); err != nil || !desktopNoticeCode.MatchString(req.Notice.Code) || strings.TrimSpace(req.Notice.Text) == "" || len(req.Notice.Text) > 24000 {
		http.Error(w, "invalid desktop notification", http.StatusBadRequest)
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
	p, ok := target.platform.(DesktopNotificationPlatform)
	if !ok {
		http.Error(w, "desktop notifications unavailable", http.StatusBadRequest)
		return
	}
	if err := e.waitOutgoing(target.platform); err != nil {
		http.Error(w, "outgoing rate limited", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	id, err := p.SendDesktopNotification(ctx, target.replyCtx, req.Notice)
	if err != nil {
		http.Error(w, "desktop notification failed", http.StatusBadGateway)
		return
	}
	apiJSON(w, http.StatusOK, map[string]string{"message_id": id})
}

func (e *Engine) dispatchDesktopNotificationReply(p Platform, msg *Message) {
	if e.desktopAnswerDisabled(msg.UserID) {
		e.reply(p, msg.ReplyCtx, e.i18n.Tf(MsgCommandDisabled, "/answer"))
		return
	}
	go func() {
		body, err := e.hooks.callDesktopAnswer(HookEvent{Event: "desktop.answer", Project: e.name, Timestamp: time.Now(), SessionKey: msg.SessionKey, Platform: msg.Platform, UserID: msg.UserID, Content: msg.DesktopReply.Text, Extra: map[string]any{"source": "notification_reply", "parent_message_id": msg.DesktopReply.ParentMessageID, "message_id": msg.MessageID}})
		e.finishDesktopAnswer(p, msg, body, err)
	}()
}

func (e *Engine) desktopAnswerDisabled(userID string) bool {
	e.userRolesMu.RLock()
	disabled, roles := e.disabledCmds["answer"], e.userRoles
	e.userRolesMu.RUnlock()
	if role := roles.ResolveRole(userID); role != nil {
		return role.DisabledCmds["answer"]
	}
	return disabled
}
