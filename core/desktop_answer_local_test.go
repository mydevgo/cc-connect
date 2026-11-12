package core

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
)

func TestDesktopAnswerNoHandlerFailsClosed(t *testing.T) {
	hm := NewHookManager("test", nil, "", "", "")
	if _, err := hm.callDesktopAnswer(HookEvent{Platform: "feishu", Content: "/answer Q-ABCDEF123456 test"}); err == nil {
		t.Fatal("missing handler must fail closed")
	}
}

func TestDesktopAnswerDoesNotExecuteWithoutPlatform(t *testing.T) {
	hm := NewHookManager("test", []HookConfig{{Event: "desktop.answer", Type: "command", Command: "this-must-not-execute"}}, "", "", "")
	if _, err := hm.callDesktopAnswer(HookEvent{Platform: "", Content: "/answer Q-ABCDEF123456 test"}); err == nil {
		t.Fatal("a missing platform must fail closed")
	}
}

func TestDesktopAnswerOriginalJSONOnStdin(t *testing.T) {
	event := HookEvent{Event: "desktop.answer", Platform: "feishu", Content: "/answer Q-ABCDEF123456 中文 \"; & $(not-a-command)\nnew line"}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	hm := NewHookManager("test", []HookConfig{{Event: "desktop.answer", Type: "command", Command: "desktop-answer-echo"}}, executable, "-test.run=^TestDesktopAnswerHelperProcess$", "")
	receipt, err := hm.callDesktopAnswer(event)
	if err != nil {
		t.Fatal(err)
	}
	var received HookEvent
	if err := json.Unmarshal([]byte(receipt), &received); err != nil {
		t.Fatal(err)
	}
	if received.Content != event.Content {
		t.Fatal("original content was changed or interpreted")
	}
}

// Reuse the already-running test executable as the external hook boundary.
// This avoids depending on Node or shell startup time for Go routing tests.
func TestDesktopAnswerHelperProcess(t *testing.T) {
	mode := os.Args[len(os.Args)-1]
	if mode == "desktop-answer-echo" {
		if _, err := io.Copy(os.Stdout, os.Stdin); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
	if !strings.HasPrefix(mode, "desktop-answer-bridge:") {
		return
	}
	file := strings.TrimPrefix(mode, "desktop-answer-bridge:")
	var event HookEvent
	if err := json.NewDecoder(os.Stdin).Decode(&event); err != nil {
		os.Exit(2)
	}
	code, _ := event.Extra["code"].(string)
	if code == "" {
		fields := strings.Fields(event.Content)
		if len(fields) < 2 {
			os.Exit(2)
		}
		code = fields[1]
	}
	var seen []string
	if body, err := os.ReadFile(file); err == nil {
		_ = json.Unmarshal(body, &seen)
	}
	duplicate := false
	for _, item := range seen {
		if item == code {
			duplicate = true
		}
	}
	if !duplicate {
		body, err := json.Marshal(append(seen, code))
		if err != nil || os.WriteFile(file, body, 0600) != nil {
			os.Exit(2)
		}
	}
	receipt := DesktopAnswerReceipt{OK: !duplicate, Status: "submitted", Message: "Answer submitted"}
	if duplicate {
		receipt.Message = "Already answered"
	}
	if event.Extra["source"] == "card" {
		receipt.CardMessageID = "om_question"
	}
	if json.NewEncoder(os.Stdout).Encode(receipt) != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func TestDesktopAnswerCommandConsumesWithoutAI(t *testing.T) {
	p := &stubPlatformEngine{n: "feishu"}
	e := &Engine{ctx: context.Background(), i18n: NewI18n("zh"), disabledCmds: map[string]bool{"answer": true}}
	if !e.handleCommand(p, &Message{Platform: "feishu", Content: "/answer Q-ABCDEF123456 x"}, "/answer Q-ABCDEF123456 x") {
		t.Fatal("answer must not fall through to agent")
	}
	if len(p.getSent()) != 1 {
		t.Fatal("expected one disabled receipt")
	}
}

func TestDesktopAnswerCardHonorsUserRole(t *testing.T) {
	p := &stubPlatformEngine{n: "bridge-test"}
	roles := NewUserRoleManager()
	roles.Configure("member", []RoleInput{{Name: "member", DisabledCommands: []string{"answer"}}})
	e := &Engine{ctx: context.Background(), i18n: NewI18n(LangEnglish), userRoles: roles}
	e.dispatchDesktopCardAnswer(p, &Message{UserID: "member", DesktopAnswer: &DesktopCardSubmission{Code: "Q-ABCDEF123456"}})
	if sent := p.getSent(); len(sent) != 1 || !strings.Contains(sent[0], "/answer") {
		t.Fatal("a card must obey the same disabled-command role policy as /answer")
	}
}

func TestDesktopAnswerWildcardDisablesBridge(t *testing.T) {
	if !resolveDisabledCmds([]string{"*"})["answer"] {
		t.Fatal("disable-all policy must include the desktop answer command")
	}
}
