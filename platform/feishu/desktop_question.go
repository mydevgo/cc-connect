package feishu

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/chenhg5/cc-connect/core"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

var _ core.DesktopQuestionPlatform = (*Platform)(nil)

func desktopQuestionCardMap(question core.DesktopQuestionCard) map[string]any {
	i18n := core.NewI18n(question.Language)
	i18n.DetectAndSet(question.Title)
	elements := []map[string]any{}
	if question.Conversation != "" {
		elements = append(elements, map[string]any{"tag": "div", "text": plainText(i18n.Tf(core.MsgDesktopConversation, question.Conversation))})
	}
	if question.Directory != "" {
		elements = append(elements, map[string]any{"tag": "note", "elements": []map[string]any{plainText(i18n.Tf(core.MsgDesktopDirectory, question.Directory))}})
	}
	elements = append(elements, map[string]any{"tag": "div", "text": plainText(question.Title)})
	form := []map[string]any{}
	if len(question.Options) > 0 {
		options := []map[string]any{}
		for index, option := range question.Options {
			options = append(options, map[string]any{"text": plainText(option.Label), "value": strconv.Itoa(index)})
			if option.Description != "" {
				elements = append(elements, map[string]any{"tag": "div", "text": plainText(option.Label + "：" + option.Description)})
			}
		}
		form = append(form, map[string]any{"tag": "select_static", "name": "desktop_option", "placeholder": plainText(i18n.T(core.MsgDesktopChoose)), "options": options, "required": false})
	}
	placeholder := i18n.T(core.MsgDesktopInput)
	if len(question.Options) > 0 {
		placeholder = i18n.T(core.MsgDesktopOtherInput)
	}
	form = append(form, map[string]any{"tag": "input", "name": "desktop_text", "input_type": "multiline_text", "placeholder": plainText(placeholder), "max_length": 1000, "required": false})
	// Form buttons use callback behaviors. Feishu accepts the older top-level
	// value field but omits it from the actual form-submit callback.
	form = append(form, map[string]any{"tag": "column_set", "columns": []map[string]any{{"tag": "column", "width": "auto", "elements": []map[string]any{{"tag": "button", "text": plainText(i18n.T(core.MsgDesktopSubmit)), "type": "primary", "name": "desktop_question_submit", "form_action_type": "submit", "behaviors": []map[string]any{{"type": "callback", "value": map[string]string{"action": "desktop-answer:submit", "code": question.Code, "token": question.Token, "language": string(i18n.CurrentLang())}}}}}}}})
	elements = append(elements, map[string]any{"tag": "form", "name": "desktop_question_form", "elements": form})
	elements = append(elements, map[string]any{"tag": "note", "elements": []map[string]any{plainText(i18n.T(core.MsgDesktopHint)), plainText(i18n.Tf(core.MsgDesktopFallback, question.Code))}})
	return map[string]any{"config": map[string]any{"wide_screen_mode": true, "update_multi": true}, "header": map[string]any{"template": "blue", "title": plainText("Codex · " + i18n.T(core.MsgDesktopWaiting))}, "elements": elements}
}
func desktopReceiptCardMap(receipt core.DesktopAnswerReceipt) map[string]any {
	i18n := core.NewI18n(receipt.Language)
	i18n.DetectAndSet(receipt.Question)
	title, color := i18n.T(core.MsgDesktopSubmitted), "green"
	switch receipt.Status {
	case "resolved":
		title = i18n.T(core.MsgDesktopResolved)
	case "expired":
		title = i18n.T(core.MsgDesktopExpired)
		color = "grey"
	case "uncertain":
		title = i18n.T(core.MsgDesktopUncertain)
		color = "orange"
	}
	elements := []map[string]any{}
	if receipt.Question != "" {
		elements = append(elements, map[string]any{"tag": "div", "text": plainText(receipt.Question)})
	}
	if receipt.Answer != "" {
		elements = append(elements, map[string]any{"tag": "div", "text": plainText(i18n.Tf(core.MsgDesktopAnswer, receipt.Answer))})
	}
	elements = append(elements, map[string]any{"tag": "div", "text": plainText(receipt.Message)})
	return map[string]any{"config": map[string]any{"wide_screen_mode": true, "update_multi": true}, "header": map[string]any{"template": color, "title": plainText("Codex · " + title)}, "elements": elements}
}
func (p *Platform) SendDesktopQuestion(ctx context.Context, rctx any, question core.DesktopQuestionCard) (string, error) {
	if !p.useInteractiveCard {
		return "", core.ErrNotSupported
	}
	rc, ok := rctx.(replyContext)
	if !ok || rc.chatID == "" {
		return "", fmt.Errorf("invalid question destination")
	}
	if err := question.Validate(); err != nil {
		return "", err
	}
	body, err := json.Marshal(desktopQuestionCardMap(question))
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256([]byte(rc.chatID + "|" + question.Code + "|" + question.Token))
	request := larkim.NewCreateMessageReqBuilder().ReceiveIdType(larkim.CreateMessageV1ReceiveIDTypeChatId).Body(larkim.NewCreateMessageReqBodyBuilder().ReceiveId(rc.chatID).MsgType(larkim.MsgTypeInteractive).Content(string(body)).Uuid(hex.EncodeToString(hash[:16])).Build()).Build()
	var response *larkim.CreateMessageResp
	err = p.withTransientRetry(ctx, "send desktop question", func() error {
		return p.withFreshTenantAccessTokenRetry(ctx, "send desktop question", func(client *lark.Client, options ...larkcore.RequestOptionFunc) error {
			var err error
			response, err = client.Im.Message.Create(ctx, request, options...)
			if err != nil {
				return err
			}
			if !response.Success() {
				return fmt.Errorf("question card code=%d msg=%s", response.Code, response.Msg)
			}
			return nil
		})
	})
	if err != nil {
		return "", err
	}
	if response.Data == nil || response.Data.MessageId == nil {
		return "", fmt.Errorf("question card has no message id")
	}
	return *response.Data.MessageId, nil
}
func (p *Platform) UpdateDesktopQuestion(ctx context.Context, messageID string, receipt core.DesktopAnswerReceipt) error {
	if !strings.HasPrefix(messageID, "om_") {
		return fmt.Errorf("invalid card message id")
	}
	body, err := json.Marshal(desktopReceiptCardMap(receipt))
	if err != nil {
		return err
	}
	return p.patchCardMessage(ctx, messageID, string(body))
}
func desktopFormString(form map[string]any, name string) string {
	value := form[name]
	if text, ok := value.(string); ok {
		return text
	}
	if obj, ok := value.(map[string]any); ok {
		if text, ok := obj["value"].(string); ok {
			return text
		}
	}
	return ""
}
func (p *Platform) onDesktopQuestionAction(event *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error) {
	language, _ := event.Event.Action.Value["language"].(string)
	i18n := core.NewI18n(core.NormalizeLanguageString(language))
	fail := func(message string) (*callback.CardActionTriggerResponse, error) {
		return &callback.CardActionTriggerResponse{Toast: &callback.Toast{Type: "error", Content: message}}, nil
	}
	data := event.Event
	if data.Operator == nil || data.Context == nil || data.Operator.OpenID == "" || !core.AllowList(p.allowFrom, data.Operator.OpenID) || !core.AllowList(p.allowChat, data.Context.OpenChatID) {
		return fail(i18n.T(core.MsgDesktopUnauthorized))
	}
	code, _ := data.Action.Value["code"].(string)
	token, _ := data.Action.Value["token"].(string)
	if len(code) != 14 || !strings.HasPrefix(code, "Q-") || len(token) < 24 || len(token) > 80 || data.Context.OpenChatID == "" || data.Context.OpenMessageID == "" {
		return fail(i18n.T(core.MsgDesktopInvalidCard))
	}
	option := desktopFormString(data.Action.FormValue, "desktop_option")
	text := desktopFormString(data.Action.FormValue, "desktop_text")
	if strings.TrimSpace(text) == "" && option == "" {
		return fail(i18n.T(core.MsgDesktopEmptyAnswer))
	}
	if len([]rune(text)) > 1000 || len(option) > 4 {
		return fail(i18n.T(core.MsgDesktopTooLong))
	}
	chatID, userID := data.Context.OpenChatID, data.Operator.OpenID
	sessionKey := p.platformName + ":" + chatID + ":" + userID
	rctx := replyContext{chatID: chatID, sessionKey: sessionKey}
	go p.dispatchCoreMessage(&core.Message{SessionKey: sessionKey, Platform: p.platformName, UserID: userID, ReplyCtx: rctx, DesktopAnswer: &core.DesktopCardSubmission{Code: code, Token: token, Option: option, Text: text, MessageID: data.Context.OpenMessageID}})
	return &callback.CardActionTriggerResponse{Toast: &callback.Toast{Type: "info", Content: i18n.T(core.MsgDesktopSubmitting)}}, nil
}
