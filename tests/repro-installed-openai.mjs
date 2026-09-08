import { mkdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import pty from 'node-pty';

const afterburn = 'C:\\Users\\noahbaertsch\\.afterburner\\bin\\afterburn.exe';
const capture = 'C:\\Users\\noahbaertsch\\.afterburner\\copilot-home\\session-state\\b518acc2-e4e8-4dac-ab53-f9b09666fce6\\files\\installed-openai-repro';
mkdirSync(capture, { recursive: true });
const env = { ...process.env, COPILOT_RUNTIME_EXTENSION_DEBUG: '1', AFTERBURNER_SKIP_PREFLIGHT: '1' };
delete env.COPILOT_AGENT_SESSION_ID;
delete env.COPILOT_LOADER_PID;
delete env.COPILOT_SUPERVISED;
let raw = '';
let trusted = false;
let restored = false;
let approved = false;
let terminalSetupDeclined = false;
let scheduled = false;
let sent = false;
let done = false;
const strip = s => s.replace(/\x1b\][^\x07]*(?:\x07|\x1b\\)/g, '').replace(/\x1b\[[0-?]*[ -/]*[@-~]/g, '').replace(/\x1b[()][A-Za-z0-9]/g, '');
const child = pty.spawn(afterburn, ['--disable-extension', 'black-box', '--disable-extension', 'byo-models', '--disable-extension', 'steward-burn', '--name', `installed-openai-repro-${process.pid}-${Date.now()}`, '--no-remote'], { name: 'xterm-256color', cols: 140, rows: 40, cwd: process.cwd(), env });
const inputs = [];
function write(data, label) { inputs.push({ at: Date.now(), label, display: data.replace(/\x1b/g, '<Esc>').replace(/\r/g, '<Enter>').replace(/\x15/g, '<Ctrl+U>') }); child.write(data); }
function finish(code, msg) {
  if (done) return;
  done = true;
  const text = strip(raw);
  const result = { code, msg, sent, nativeModal: /Copilot OpenAI Bridge[\s\S]*Native Afterburner modal overlay/i.test(text), canvasOpened: /Canvas opened:\s*Copilot OpenAI Bridge/i.test(text), unavailable: /native menu unavailable|interactive menu could not open/i.test(text), activated: /activated Afterburner extension 'copilot-openai'/i.test(text), inputs, tail: text.slice(-5000) };
  writeFileSync(join(capture, 'raw.ansi'), raw, 'utf8');
  writeFileSync(join(capture, 'text.txt'), text, 'utf8');
  writeFileSync(join(capture, 'result.json'), JSON.stringify(result, null, 2), 'utf8');
  try { child.kill(); } catch {}
  console.log(msg);
  process.exit(code);
}
child.onData(data => {
  raw += data;
  const text = strip(raw);
  const recent = text.slice(-5000);
  if (!trusted && /Do you trust the files in this folder/i.test(recent)) { trusted = true; setTimeout(() => write('\r', 'trust folder'), 250); return; }
  if (!restored && /Restore interrupted sessions/i.test(recent)) { restored = true; setTimeout(() => write('\x1b', 'dismiss restore'), 250); return; }
  if (!approved && /wants elevated permissions/i.test(recent)) { approved = true; setTimeout(() => write('\r', 'approve permissions'), 250); return; }
  if (!terminalSetupDeclined && /Set up terminal for multi-line input support/i.test(recent)) { terminalSetupDeclined = true; setTimeout(() => write('\x1b', 'dismiss terminal setup'), 250); return; }
  if (!scheduled && /activated Afterburner extension 'copilot-openai'/i.test(text) && /\/ commands|tab next tab|\? help/i.test(recent)) {
    scheduled = true;
    setTimeout(() => {
      sent = true;
      write('\x15', 'clear prompt');
      write('\x1b[200~/copilot-openai\x1b[201~\r', 'bracket-paste submit command');
    }, 5000);
  }
  if (sent && /Copilot OpenAI Bridge[\s\S]*Native Afterburner modal overlay/i.test(text)) finish(0, 'native modal opened in installed afterburn');
  if (sent && /Canvas opened:\s*Copilot OpenAI Bridge/i.test(text)) finish(2, 'only generic canvas opened');
  if (sent && /native menu unavailable|interactive menu could not open/i.test(text)) finish(3, 'menu unavailable');
});
child.onExit(({ exitCode }) => { if (!done) finish(1, `afterburn exited ${exitCode}`); });
setTimeout(() => finish(4, 'timeout'), 90000).unref?.();
