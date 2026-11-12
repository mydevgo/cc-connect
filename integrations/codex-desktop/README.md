# Codex Desktop question cards for Feishu

This optional Windows integration forwards local Codex Desktop main-chat notifications to an existing Feishu private chat. A desktop `request_user_input_async` question becomes an interactive card: select an option or enter text, then click **Submit answer**. Custom text takes precedence over the selected option. After submission the original card shows the result and removes its controls.

The bridge answers the original desktop chat and active turn. It does not start a new agent session. Ordinary Feishu messages still use the project's normal agent. `/answer Q-XXXXXXXXXXXX your answer` is a fallback; its question code appears at the bottom of the card.

## Prerequisites

- A patched cc-connect binary built from this repository, Node.js, and a running Windows Codex Desktop application.
- A working Feishu bot, private chat, and published `card.action.trigger` callback subscription using a long connection.
- Restrict the platform's `allow_from` to the intended user. Set `sessionKey` to that user's existing private-chat session.
- A desktop version exposing the Hooks events and local IPC protocol used here. The adapter was verified with Windows desktop version `26.1103.7773.0`; it uses an internal interface that can change after updates.

## Setup

1. Copy the five `.mjs` files to a private, persistent directory. Copy `settings.example.json` to `settings.json` there and replace every example path and identifier. Keep the scripts together. State and question directories are created automatically. No Feishu secret belongs in this settings file.
2. Add one exact `desktop.answer` command hook to the cc-connect configuration. Use absolute paths and the project's configured shell. For PowerShell, a hook is:

   ```toml
   [[hooks]]
   event = "desktop.answer"
   type = "command"
   command = "& 'C:/Program Files/nodejs/node.exe' 'C:/Tools/codex-desktop-bridge/desktop-answer-command.mjs'"
   ```

   The event JSON is passed on stdin to this fixed command; message text is never inserted into the shell command. The command returns one JSON receipt on stdout. Multiple handlers fail closed.
3. Merge the relevant entries from `hooks.example.json` into the desktop Hooks configuration, replacing the Node and script paths. Do not overwrite existing hooks. Review and trust the new command in the desktop application. The notifier always emits `{}` and cannot approve tools or block the original chat.
4. Restart cc-connect and create a fresh asynchronous question in an active desktop turn. In Feishu select or fill in an answer, then submit. Confirm the original desktop chat receives it and the card changes to **Submitted**.

`cc-connect desktop-card --project NAME --session SESSION` reads a question JSON object from stdin and prints `{ "message_id": "om_..." }`. It uses cc-connect's existing local API socket and platform connection. The integration invokes it automatically. Global help remains unchanged because this is an internal transport.

## Supported behavior and limits

- Completion, question, permission-request, model-error, and recognizable tool-error notifications cover local desktop main chats. CLI and subagent sessions are filtered out. The bundled notifier/bridge sample uses Chinese notification and receipt text; Go card chrome follows the project language and supports English, Simplified Chinese, Traditional Chinese, Japanese, and Spanish.
- Remote answers support asynchronous desktop questions in the original still-active turn. Blocking `request_user_input`, permission approvals, login prompts, and ended/interrupted turns require the desktop.
- Card custom input is limited to 1000 characters; `/answer` accepts up to 8000. Merely selecting or typing does not submit. Secret questions are skipped.
- Replies are restricted to the configured project, platform, private-chat session, user, question code, card token, and original message ID. Before sending, the adapter rechecks the desktop question, active turn, and prior replies.
- Question records expire after 24 hours. A directory lock serializes each question. Repeated replies, stale questions, and already-answered questions are rejected.
- If a send may have succeeded but cannot be confirmed, it is not retried automatically. Check the desktop first. Desktop and Feishu answers can still race between validation and submission; use one entry point for each question.
- Notifications are deduplicated after successful sends. There is no persistent retry queue for failed notifications.

Question records and logs stay in the configured private directories. Logs omit answer bodies; question records contain the question, options, and card binding token. Do not commit runtime settings, records, or credentials.

## Maintenance

The bridge is an optional local integration. No additional persistent worker is installed. Incoming answers launch a short-lived Node process. Updating cc-connect or Codex Desktop requires revalidating a fresh question. Replacing the installed binary or modifying trusted desktop hooks is a separate deployment step; copying or committing source files does not deploy them.

To stop remote answers, remove the exact `desktop.answer` hook and disable the desktop notifier hooks. Pending questions remain answerable on the desktop. Preserve a backup of the installed binary and configuration before replacing them.

## Validation

From the repository root:

```powershell
node integrations/codex-desktop/tests/test-desktop-answer.mjs
node integrations/codex-desktop/tests/test-question-cards.mjs
node integrations/codex-desktop/tests/test-codex-wechat-notify.mjs
node integrations/codex-desktop/tests/validate-answer-notification.mjs
go test ./core ./platform/feishu -run TestDesktop
go test ./core -run TestCUJ
```

The Node tests use temporary directories and mocked IPC boundaries. They do not send Feishu messages or change desktop settings. The Go tests cover command routing, JSON stdin, capability checks, card form metadata, explicit submission, callback authorization, and final card state. Full repository build/test commands remain those in `AGENTS.md`.
