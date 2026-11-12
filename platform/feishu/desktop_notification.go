package feishu

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/chenhg5/cc-connect/core"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

var _ core.DesktopNotificationPlatform = (*Platform)(nil)

func (p *Platform) EnableDesktopNotificationReplies(enabled bool) {
	p.mu.Lock()
	p.desktopNotificationReplies = enabled
	p.mu.Unlock()
}

func (p *Platform) SendDesktopNotification(ctx context.Context, rctx any, notice core.DesktopNotification) (string, error) {
	rc, ok := rctx.(replyContext)
	if !ok || rc.chatID == "" {
		return "", fmt.Errorf("feishu: invalid desktop notice destination")
	}
	card := map[string]any{"config": map[string]any{"wide_screen_mode": true}, "elements": []map[string]any{{"tag": "div", "text": plainText(notice.Text)}}}
	body, err := json.Marshal(card)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256([]byte(rc.chatID + "|" + notice.Code))
	req := larkim.NewCreateMessageReqBuilder().ReceiveIdType(larkim.CreateMessageV1ReceiveIDTypeChatId).Body(larkim.NewCreateMessageReqBodyBuilder().ReceiveId(rc.chatID).MsgType(larkim.MsgTypeInteractive).Content(string(body)).Uuid(hex.EncodeToString(hash[:16])).Build()).Build()
	var response *larkim.CreateMessageResp
	err = p.withTransientRetry(ctx, "send desktop notice", func() error {
		return p.withFreshTenantAccessTokenRetry(ctx, "send desktop notice", func(client *lark.Client, options ...larkcore.RequestOptionFunc) error {
			var err error
			response, err = client.Im.Message.Create(ctx, req, options...)
			if err != nil {
				return err
			}
			if !response.Success() {
				return fmt.Errorf("feishu: desktop notice code=%d", response.Code)
			}
			return nil
		})
	})
	if err != nil {
		return "", err
	}
	if response.Data == nil || response.Data.MessageId == nil {
		return "", fmt.Errorf("feishu: desktop notice missing message id")
	}
	return *response.Data.MessageId, nil
}

func isDesktopNotificationText(text string) bool {
	// Covers current durable notices and older desktop notifications/cards. A
	// code or a session ID in arbitrary user text is never enough to route it.
	text = strings.TrimSpace(text)
	return strings.Contains(text, "[Codex · ") || strings.HasPrefix(text, "Codex · ")
}

// Intercept direct quotes before generic quote expansion. In particular, never
// forward an unverified desktop reply as a new ordinary agent prompt.
func (p *Platform) handleDesktopNotificationReply(ctx context.Context, msgType, content string, mentions []*larkim.MentionEvent, messageID, sessionKey, userID string, rctx replyContext, parentID string) bool {
	p.mu.RLock()
	enabled := p.desktopNotificationReplies
	p.mu.RUnlock()
	if !enabled || parentID == "" {
		return false
	}
	parent := p.fetchDesktopQuoteParent(ctx, parentID)
	if parent == nil {
		p.replyDesktopQuoteError(ctx, rctx, core.MsgDesktopQuoteUnavailable)
		return true
	}
	if parent.senderType != "app" || (parent.senderID != p.appID && parent.senderID != p.getBotOpenID()) || !isDesktopNotificationText(parent.text) {
		return false
	}
	var text string
	switch msgType {
	case "text":
		var body struct {
			Text string `json:"text"`
		}
		if json.Unmarshal([]byte(content), &body) != nil {
			p.replyDesktopQuoteError(ctx, rctx, core.MsgDesktopQuoteTextOnly)
			return true
		}
		text = stripMentions(body.Text, mentions, p.getBotOpenID())
	case "post":
		body := p.parsePostBody(content)
		if body == nil || len(body.Files) > 0 {
			p.replyDesktopQuoteError(ctx, rctx, core.MsgDesktopQuoteTextOnly)
			return true
		}
		// The text-only extractor avoids downloading or interpreting attachments.
		for _, line := range body.Content {
			for _, element := range line {
				if element.Tag != "text" && element.Tag != "at" {
					p.replyDesktopQuoteError(ctx, rctx, core.MsgDesktopQuoteTextOnly)
					return true
				}
				if element.Tag == "text" {
					text += element.Text
				}
			}
			text += "\n"
		}
	default:
		p.replyDesktopQuoteError(ctx, rctx, core.MsgDesktopQuoteTextOnly)
		return true
	}
	if strings.TrimSpace(text) == "" {
		p.replyDesktopQuoteError(ctx, rctx, core.MsgDesktopQuoteTextOnly)
		return true
	}
	p.dispatchCoreMessage(&core.Message{SessionKey: sessionKey, Platform: p.platformName, UserID: userID, MessageID: messageID, ReplyCtx: rctx, DesktopReply: &core.DesktopNotificationReply{ParentMessageID: parentID, Text: text}})
	return true
}

// Read only text and sender metadata for routing. Generic quote processing
// retains ownership of attachment downloads and sender-name lookups.
func (p *Platform) fetchDesktopQuoteParent(ctx context.Context, messageID string) *chainMessage {
	response, err := p.client.Get(ctx, "/open-apis/im/v1/messages/"+messageID+"?card_msg_content_type=raw_card_content", nil, larkcore.AccessTokenTypeTenant)
	if err != nil {
		return nil
	}
	var body struct {
		Code int `json:"code"`
		Data struct {
			Items []struct {
				MessageID string `json:"message_id"`
				MsgType   string `json:"msg_type"`
				Sender    struct {
					ID   string `json:"id"`
					Type string `json:"sender_type"`
				} `json:"sender"`
				Body struct {
					Content string `json:"content"`
				} `json:"body"`
			} `json:"items"`
		} `json:"data"`
	}
	if json.Unmarshal(response.RawBody, &body) != nil || body.Code != 0 || len(body.Data.Items) == 0 {
		return nil
	}
	item := body.Data.Items[0]
	for _, candidate := range body.Data.Items {
		if candidate.MessageID == messageID {
			item = candidate
			break
		}
	}
	var text string
	switch item.MsgType {
	case "text":
		var content struct {
			Text string `json:"text"`
		}
		if json.Unmarshal([]byte(item.Body.Content), &content) == nil {
			text = content.Text
		}
	case "interactive":
		text = extractInteractiveCardText(item.Body.Content)
	case "post":
		if post := p.parsePostBody(item.Body.Content); post != nil {
			text = post.Title
			for _, line := range post.Content {
				for _, element := range line {
					if element.Tag == "text" {
						text += "\n" + element.Text
					}
				}
			}
		}
	}
	return &chainMessage{senderType: item.Sender.Type, senderID: item.Sender.ID, text: text}
}

func (p *Platform) replyDesktopQuoteError(ctx context.Context, rctx replyContext, key core.MsgKey) {
	if err := p.Reply(ctx, rctx, core.NewI18n(core.LangChinese).T(key)); err != nil {
		slog.Warn(p.tag()+": desktop quote error reply failed", "error", err)
	}
}
