import fs from 'node:fs';
import path from 'node:path';
import os from 'node:os';
import crypto from 'node:crypto';
import { spawnSync } from 'node:child_process';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { registerQuestions } from './desktop-answer-store.mjs';
import { sendQuestionCards } from './desktop-question-cards.mjs';
import { sendDesktopNotification } from './desktop-notifications.mjs';

const text = value => typeof value === 'string' ? value : JSON.stringify(value ?? '');
const clip = (value, limit) => {
  const chars = Array.from(text(value).trim());
  return chars.length > limit ? chars.slice(0, limit).join('') + '\n…（已截断，请在桌面查看全文）' : chars.join('');
};

function questions(input) {
  const items = input?.questions;
  if (!Array.isArray(items)) return clip(input, 1800);
  return items.map(q => {
    const title = q.question || q.title || q.header || '请确认';
    const options = (q.options || []).map(o => typeof o === 'string' ? o : o.label).filter(Boolean);
    return title + (options.length ? '\n选项：' + options.join('；') : '');
  }).join('\n\n');
}

function parseJson(value) {
  if (typeof value !== 'string') return value;
  try { return JSON.parse(value); } catch { return value; }
}

function shape(value, depth = 0) {
  if (value === null || value === undefined) return String(value);
  if (typeof value === 'string') {
    const parsed = parseJson(value);
    return typeof parsed === 'string' ? { stringLength: value.length, hasExitMarker: /exit|exited/i.test(value) } : shape(parsed, depth + 1);
  }
  if (typeof value !== 'object') return value;
  if (depth > 4) return Array.isArray(value) ? 'array' : Object.keys(value);
  if (Array.isArray(value)) return value.slice(0, 3).map(item => shape(item, depth + 1));
  return Object.fromEntries(Object.entries(value).slice(0, 16).map(([key, item]) => [key, shape(item, depth + 1)]));
}

function transcriptToolExit(event) {
  if (!event.transcript_path || !event.tool_use_id) return null;
  const fd = fs.openSync(event.transcript_path, 'r');
  let data;
  try {
    const size = fs.fstatSync(fd).size;
    const start = Math.max(0, size - 1024 * 1024);
    const buffer = Buffer.alloc(size - start);
    const count = fs.readSync(fd, buffer, 0, buffer.length, start);
    data = buffer.subarray(0, count).toString('utf8');
  } finally { fs.closeSync(fd); }
  for (const line of data.split('\n').reverse()) {
    if (!line.includes(event.tool_use_id)) continue;
    try {
      const entry = JSON.parse(line);
      if (entry.type !== 'event_msg' || entry.payload.type !== 'item_completed') continue;
      const item = entry.payload.item;
      if (item?.id !== event.tool_use_id) continue;
      if (typeof item.exit_code === 'number') return item.exit_code;
      const output = item.output;
      if (output && typeof output.exit_code === 'number') return output.exit_code;
    } catch { /* The transcript may have an incomplete last line. */ }
  }
  return null;
}

// Only explicit error indicators count: quoted words such as "error" in a
// successful command's output must not generate an error notification.
export function toolFailure(value) {
  value = parseJson(value);
  if (typeof value === 'string') {
    const code = value.match(/(?:Process exited with code|exit_code["']?\s*[:=]|exit code[:\s]+)\s*(-?\d+)/i);
    if (code && Number(code[1]) !== 0) return clip(value, 1800);
    if (/^(?:Error executing tool|Tool execution failed|Function call failed|Error:)/i.test(value.trim())) return clip(value, 1800);
    return null;
  }
  if (!value || typeof value !== 'object') return null;
  if (Array.isArray(value)) {
    for (const child of value) { const failure = toolFailure(child); if (failure) return failure; }
    return null;
  }
  if (value.isError === true || value.is_error === true || value.success === false || value.status === 'failed' || value.status === 'rejected') return clip(value, 1800);
  for (const field of ['exit_code', 'exitCode']) {
    if (value[field] !== null && value[field] !== undefined && Number(value[field]) !== 0 && Number.isFinite(Number(value[field]))) return clip(value, 1800);
  }
  if (value.error && !['', 'null', 'false'].includes(text(value.error))) return clip(value.error, 1800);
  for (const field of ['result', 'output', 'content', 'text', 'value']) {
    if (value[field] !== undefined) { const failure = toolFailure(value[field]); if (failure) return failure; }
  }
  return null;
}

export function classify(event) {
  switch (event.hook_event_name) {
    case 'Stop': {
      const body = event.last_assistant_message;
      return body ? { category: 'complete', label: '回复完成', body: clip(body, 2600), detailKey: '' } : null;
    }
    case 'PermissionRequest':
      return {
        category: 'permission', label: '需要权限确认',
        body: '工具：' + (event.tool_name || '未知') + '\n' + clip(event.tool_input?.description || event.tool_input, 1800) + '\n\n请回到 Codex 桌面端审批。',
        detailKey: event.tool_use_id || crypto.createHash('sha256').update(text(event.tool_input)).digest('hex'),
      };
    case 'PreToolUse':
      if (!/(?:^|[._])request_user_input(?:_async)?$/.test(event.tool_name || '')) return null;
      return {
        category: 'question', label: '需要你确认',
        body: clip(questions(event.tool_input), 2000) + '\n\n请回到 Codex 桌面端回答。',
        detailKey: event.tool_use_id || crypto.createHash('sha256').update(text(event.tool_input)).digest('hex'),
      };
    case 'PostModelRequest':
      return event.status === 'failed' ? {
        category: 'model-error', label: '模型请求失败',
        body: clip(event.error || '模型请求失败，详情请查看桌面端。', 1800) + '\n\nCodex 可能会自动重试。', detailKey: '',
      } : null;
    case 'PostToolUse': {
      const nativeCode = event.tool_response_exit_code;
      const failure = toolFailure(event.tool_response) ||
        (typeof nativeCode === 'number' && nativeCode !== 0 ? '退出码：' + nativeCode + '\n' + clip(event.tool_response, 1600) : null);
      return failure ? {
        category: 'tool-error', label: '工具执行错误',
        body: '工具：' + (event.tool_name || '未知') + '\n' + failure,
        detailKey: event.tool_use_id || crypto.createHash('sha256').update(failure).digest('hex'),
      } : null;
    }
    default: return null;
  }
}

export function readSessionMeta(transcriptPath) {
  const fd = fs.openSync(transcriptPath, 'r');
  try {
    const buffer = Buffer.alloc(64 * 1024);
    const count = fs.readSync(fd, buffer, 0, buffer.length, 0);
    const line = buffer.subarray(0, count).toString('utf8').split('\n', 1)[0];
    const entry = JSON.parse(line);
    return entry.type === 'session_meta' ? entry.payload : null;
  } finally { fs.closeSync(fd); }
}

export function isDesktopMain(meta) {
  return typeof meta?.source === 'string' && /desktop/i.test(meta.originator || '') && meta.source !== 'cli';
}

function findTranscript(root, sessionId) {
  if (!/^[a-zA-Z0-9_-]{8,100}$/.test(sessionId)) return null;
  if (!fs.existsSync(root)) return null;
  for (const item of fs.readdirSync(root, { withFileTypes: true }).reverse()) {
    const full = path.join(root, item.name);
    if (item.isDirectory()) { const found = findTranscript(full, sessionId); if (found) return found; }
    else if (item.name.endsWith(sessionId + '.jsonl')) return full;
  }
  return null;
}

function sessionTitle(codexHome, sessionId) {
  const index = path.join(codexHome, 'session_index.jsonl');
  try {
    const fd = fs.openSync(index, 'r');
    let body;
    try {
      const size = fs.fstatSync(fd).size;
      const start = Math.max(0, size - 2 * 1024 * 1024);
      const buffer = Buffer.alloc(size - start);
      const count = fs.readSync(fd, buffer, 0, buffer.length, start);
      body = buffer.subarray(0, count).toString('utf8');
    } finally { fs.closeSync(fd); }
    for (const line of body.split('\n').reverse()) {
      if (!line.includes(sessionId)) continue;
      try { const row = JSON.parse(line); if (row.id === sessionId) return row.thread_name || ''; } catch { /* Partial first/last lines are harmless. */ }
    }
  } catch { /* A title is optional. */ }
  return '';
}

export function render(event, notice, meta, title = '') {
  const id = meta.id || event.session_id;
  return '[Codex · ' + notice.label + ']\n' +
    (title ? '会话：' + clip(title, 100) + '\n' : '') +
    '目录：' + (event.cwd || meta.cwd || '未知') + '\n' +
    'ID：' + id + '\n\n' + notice.body;
}

export function deliver(event, options) {
  const notice = classify(event);
  if (!notice) return { status: 'ignored' };
  const codexHome = options.codexHome || path.join(os.homedir(), '.codex');
  const transcript = event.transcript_path || findTranscript(path.join(codexHome, 'sessions'), event.session_id || '');
  if (!transcript) return { status: 'no-transcript' };
  const meta = readSessionMeta(transcript);
  if (!isDesktopMain(meta)) return { status: 'filtered-source' };
  if (meta.id !== event.session_id) return { status: 'session-mismatch' };

  let registered=[];
  if (notice.category === 'question' && options.answerDir) {
    registered = registerQuestions(event, options);
    if (registered.length) notice.body = registered.map(q =>
      '问题编号：' + q.code + '\n' + q.title +
      (q.options.length ? '\n选项：' + q.options.map(o => typeof o === 'string' ? o : o.label).filter(Boolean).join('；') : '') +
      '\n飞书回复：/answer ' + q.code + ' 你的答案'
    ).join('\n\n') + '\n\n也可以在 Codex 桌面端回答。';
  }

  const turn = event.turn_id || crypto.createHash('sha256').update(text(event.last_assistant_message || event.tool_input || event.error)).digest('hex');
  const key = crypto.createHash('sha256').update([meta.id, turn, notice.category, notice.detailKey].join('|')).digest('hex');
  const stateDir = options.stateDir;
  fs.mkdirSync(stateDir, { recursive: true });
  const marker = path.join(stateDir, key + '.sent');
  const lock = path.join(stateDir, key + '.lock');
  if (fs.existsSync(marker)) return { status: 'duplicate' };
  try { fs.mkdirSync(lock); } catch (error) {
    if (error.code !== 'EEXIST') throw error;
    if (Date.now() - fs.statSync(lock).mtimeMs < 90_000) return { status: 'in-flight' };
    fs.rmdirSync(lock);
    fs.mkdirSync(lock);
  }
  try {
    if (fs.existsSync(marker)) return { status: 'duplicate' };
    if (registered.length && options.questionCards) {
      sendQuestionCards(registered,event,options,{title:sessionTitle(codexHome,meta.id),cwd:meta.cwd});
      fs.writeFileSync(marker,JSON.stringify({at:new Date().toISOString(),session:meta.id,turn,category:notice.category,format:'card'}));
      return {status:'sent',category:notice.category,session:meta.id,format:'card'};
    }
    const message = render(event, notice, meta, sessionTitle(codexHome, meta.id));
    if(notice.category==='complete'&&options.questionCards&&options.answerDir){
      const messageId=sendDesktopNotification(event,options,message);
      fs.writeFileSync(marker,JSON.stringify({at:new Date().toISOString(),session:meta.id,turn,category:notice.category,format:'desktop-notice',messageId}));
      return {status:'sent',category:notice.category,session:meta.id,format:'desktop-notice'};
    }
    const result = (options.sender || ((body) => spawnSync(options.ccBinary, [
      'send', '--project', options.project, '--session', options.sessionKey, '--stdin',
    ], { input: body, encoding: 'utf8', windowsHide: true, timeout: 25_000, maxBuffer: 256 * 1024 })))(message);
    if (result.error || result.status !== 0) throw new Error('cc-connect send failed: ' + (result.error?.code || 'exit ' + result.status) + ' ' + clip(result.stderr || result.stdout || '', 350));
    fs.writeFileSync(marker, JSON.stringify({ at: new Date().toISOString(), session: meta.id, turn, category: notice.category }));
    return { status: 'sent', category: notice.category, session: meta.id };
  } finally { try { fs.rmdirSync(lock); } catch { /* Another stale-lock cleanup may have run. */ } }
}

function run() {
  const scriptDir = path.dirname(fileURLToPath(import.meta.url));
  const options = JSON.parse(fs.readFileSync(path.join(scriptDir, 'settings.json'), 'utf8'));
  const log = (entry) => {
    try {
      fs.mkdirSync(path.dirname(options.logFile), { recursive: true });
      if (fs.existsSync(options.logFile) && fs.statSync(options.logFile).size > 2 * 1024 * 1024) fs.renameSync(options.logFile, options.logFile + '.previous');
      fs.appendFileSync(options.logFile, JSON.stringify({ at: new Date().toISOString(), ...entry }) + '\n');
    } catch { /* Notification diagnostics must not interrupt Codex. */ }
  };
  try {
    const event = JSON.parse(fs.readFileSync(0, 'utf8'));
    if (event.hook_event_name === 'PostToolUse' && event.tool_name === 'Bash' && !toolFailure(event.tool_response)) {
      try { event.tool_response_exit_code = transcriptToolExit(event); } catch { /* Exit metadata is optional. */ }
    }
    const result = deliver(event, options);
    const trace = Number(options.traceUntil || 0) > Date.now();
    if (trace || !['ignored', 'duplicate', 'filtered-source'].includes(result.status)) log({
      event: event.hook_event_name,
      ...(trace && event.hook_event_name === 'PostToolUse' ? { tool: event.tool_name, responseShape: shape(event.tool_response), eventShape: shape(event) } : {}),
      ...result,
    });
  } catch (error) { log({ status: 'error', error: String(error.message).slice(0, 500) }); }
}

if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
  try { run(); } catch { /* Even missing settings must not block a chat. */ }
  process.stdout.write('{}\n');
}
