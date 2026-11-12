package feishu

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

func testDesktopQuestion() core.DesktopQuestionCard {
	return core.DesktopQuestionCard{Code: "Q-ABCDEF123456", Token: "12345678-1234-1234-1234-123456789012", Title: "选择答案", Options: []core.DesktopQuestionOption{{Label: "一", Description: "第一项"}, {Label: "二"}}}
}
func TestDesktopQuestionFormExplicitSubmit(t *testing.T) {
	data := desktopQuestionCardMap(testDesktopQuestion())
	raw, _ := json.Marshal(data)
	body := string(raw)
	for _, want := range []string{`"tag":"form"`, `"tag":"input"`, `"tag":"select_static"`, `"form_action_type":"submit"`, `"behaviors":[{"type":"callback","value":`, `desktop-answer:submit`} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %s", want)
		}
	}
	if strings.Contains(body, "initial_option") {
		t.Fatal("must not preselect an answer")
	}
}
func TestDesktopQuestionReceiptRemovesInputs(t *testing.T) {
	raw, _ := json.Marshal(desktopReceiptCardMap(core.DesktopAnswerReceipt{Status: "submitted", Message: "已提交", Answer: "<at user_id=all>"}))
	body := string(raw)
	if strings.Contains(body, `"tag":"form"`) || strings.Contains(body, `"tag":"button"`) {
		t.Fatal("resolved card must not remain actionable")
	}
	if !strings.Contains(body, `"tag":"plain_text"`) {
		t.Fatal("answer must render as plain text")
	}
}
func desktopTestCallback(user string) *callback.CardActionTriggerEvent {
	return &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{Operator: &callback.Operator{OpenID: user}, Context: &callback.Context{OpenChatID: "oc_chat", OpenMessageID: "om_card"}, Action: &callback.CallBackAction{Value: map[string]any{"action": "desktop-answer:submit", "code": "Q-ABCDEF123456", "token": "12345678-1234-1234-1234-123456789012"}, FormValue: map[string]any{"desktop_option": "1", "desktop_text": ""}}}}
}
func TestDesktopQuestionCallbackAuthorizationAndExplicitSubmit(t *testing.T) {
	incoming := make(chan *core.Message, 2)
	p := &Platform{platformName: "feishu", allowFrom: "ou_owner", handler: func(_ core.Platform, m *core.Message) { incoming <- m }}
	p.self = p
	event := desktopTestCallback("ou_other")
	response, err := p.onCardAction(event)
	if err != nil || response.Toast.Type != "error" {
		t.Fatal("unauthorized callback must be denied")
	}
	event = desktopTestCallback("ou_owner")
	event.Event.Action.Value = map[string]any{}
	event.Event.Action.Option = "1"
	_, err = p.onCardAction(event)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-incoming:
		t.Fatal("selection alone submitted an answer")
	default:
	}
	event = desktopTestCallback("ou_owner")
	response, err = p.onCardAction(event)
	if err != nil || response.Toast.Type != "info" {
		t.Fatal("expected async submission toast")
	}
	select {
	case message := <-incoming:
		if message.DesktopAnswer == nil || message.DesktopAnswer.Option != "1" || message.SessionKey != "feishu:oc_chat:ou_owner" {
			t.Fatal("lost form data or destination")
		}
	case <-time.After(time.Second):
		t.Fatal("no form submission")
	}
}
func TestDesktopQuestionEmptyFormDoesNotSubmit(t *testing.T) {
	p := &Platform{platformName: "feishu", allowFrom: "ou_owner"}
	event := desktopTestCallback("ou_owner")
	event.Event.Action.FormValue = map[string]any{}
	response, err := p.onCardAction(event)
	if err != nil || response.Toast.Type != "error" {
		t.Fatal("empty answer must be rejected")
	}
}

func TestDesktopQuestionMissingCallbackMetadataFailsVisibly(t *testing.T) {
	p := &Platform{platformName: "feishu", allowFrom: "ou_owner"}
	event := desktopTestCallback("ou_owner")
	event.Event.Action.Name = "desktop_question_submit"
	event.Event.Action.Value = nil
	response, err := p.onCardAction(event)
	if err != nil || response == nil || response.Toast == nil || response.Toast.Type != "error" {
		t.Fatal("a form submit without question metadata must show an error instead of silently doing nothing")
	}
}

func TestDesktopQuestionFormSubmitCallbackBehaviorsCarryBinding(t *testing.T) {
	question := testDesktopQuestion()
	card := desktopQuestionCardMap(question)
	var submit map[string]any
	var visit func(any)
	visit = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			if v["name"] == "desktop_question_submit" {
				submit = v
			}
			for _, child := range v {
				visit(child)
			}
		case []map[string]any:
			for _, child := range v {
				visit(child)
			}
		}
	}
	visit(card)
	behaviors, ok := submit["behaviors"].([]map[string]any)
	if !ok || len(behaviors) != 1 || behaviors[0]["type"] != "callback" {
		t.Fatal("form submit requires callback behaviors; top-level value is dropped by Feishu")
	}
	value := behaviors[0]["value"].(map[string]string)
	if value["action"] != "desktop-answer:submit" || value["code"] != question.Code || value["token"] != question.Token {
		t.Fatal("form callback lost the original question binding")
	}
}

func TestDesktopQuestionCardLanguage(t *testing.T) {
	question := testDesktopQuestion()
	question.Language = core.LangSpanish
	raw, _ := json.Marshal(desktopQuestionCardMap(question))
	if !strings.Contains(string(raw), "Enviar respuesta") {
		t.Fatal("card controls must use the requested project language")
	}
}
