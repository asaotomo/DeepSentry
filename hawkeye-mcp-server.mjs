#!/usr/bin/env node
/**
 * Hx0 HawkEye MCP Server v1.0.12
 *
 * Zero-dependency local bridge:
 *   Any MCP host --stdio / Streamable HTTP / legacy SSE--> this process
 *   this process --WebSocket on 127.0.0.1--> Hx0 HawkEye extension
 *
 * stdout is reserved for JSON-RPC. Diagnostics are written to stderr.
 */
import crypto from 'node:crypto';
import { execFile } from 'node:child_process';
import fs from 'node:fs/promises';
import http from 'node:http';
import path from 'node:path';
import process from 'node:process';

const SERVER_NAME = 'hx0-hawkeye-mcp';
const SERVER_VERSION = '1.0.12';
const SEARCH_OPERATOR_GUIDE = 'When the user request is specific, or more public evidence is needed to finish the task, you MUST use Google-style search operators in browser_search/browser_research as a problem-solving step. Do not send bare keywords. Triggers: official/full names, a known site, file types (pdf/doc/xls), year/number ranges, excluding noise, unknown missing words, alternate names. Operators: "exact phrase"; " -term" (space before minus, none after); site:domain; filetype:pdf|doc|xls|ppt; * wildcard; intitle:/allintitle:; inurl:; uppercase OR or |; 2020..2025. Operator support varies by engine; relax overly strict syntax after empty results while preserving explicit user constraints. Combine to solve the question, e.g. "official title" filetype:pdf site:gov.cn. Also exactMatch, includeDomains, excludeDomains, timeRange. If the first page is noisy or incomplete, rewrite operators and search again. When general results are thin, also search well-known forums with site: zhihu.com, hupu.com, tieba.baidu.com, reddit.com, v2ex.com, github.com. For security, CVE, malware, or IOC questions, also check public threat-intel and vendor centers: threatbook.cn, x.threatbook.com, ti.360.net, ti.qianxin.com, nti.nsfocus.com, virustotal.com, freebuf.com. Example: keyword site:zhihu.com ; CVE-xxxx site:threatbook.cn. Use public pages only and cite the source. Smart search: query is the main question and queries adds distinct evidence variants; includeDomains means any listed domain. Prefer primary sources and read bodies with maxFetches or browser_research before verification claims. evidenceScore is retrieval relevance, not truth. Never cite challenge pages as evidence; disclose blockedByChallenge, degraded engines, stale cache and missing sources. Rewrite the query or change engines when evidence is insufficient.';
const DEFAULT_WS_PORT = 19016;
const WS_PATH = '/hx0-mcp';
const WS_GUID = '258EAFA5-E914-47DA-95CA-C5AB0DC85B11';
const TOOL_TIMEOUT_MS = 35_000;
const SUPPORTED_PROTOCOL_VERSIONS = new Set(['2025-11-25', '2025-06-18', '2025-03-26', '2024-11-05']);
const LATEST_PROTOCOL_VERSION = '2025-11-25';
const MAX_PENDING_CALLS = 16;
const MAX_WS_MESSAGE_BYTES = 32 * 1024 * 1024;
const MAX_WS_FRAGMENTS = 4096;
const MAX_HTTP_SESSIONS = 64;
const SESSION_IDLE_MS = 60 * 60 * 1000;

function stderr(message) {
  process.stderr.write('[Hx0 MCP] ' + String(message || '') + '\n');
}

function execNativeInput(file, args, { signal } = {}) {
  return new Promise((resolve, reject) => {
    execFile(file, args, { timeout: 8_000, windowsHide: true, signal }, (error, stdout, stderrText) => {
      if (error) {
        const detail = String(stderrText || error.message || error).trim();
        reject(new Error(detail || 'Native input command failed'));
      } else resolve(String(stdout || '').trim());
    });
  });
}

function normalizeNativeKeyRequest(request) {
  const row = request && typeof request === 'object' ? request : {};
  const raw = String(row.key || '').trim();
  if (!raw || raw.length > 80) throw new Error('Invalid native key');
  const parts = raw === '+' ? ['+'] : raw.split('+').map((item) => item.trim()).filter(Boolean);
  let key = parts.pop() || raw;
  const modifiers = { shift: row.shiftKey === true, control: row.ctrlKey === true || row.controlKey === true, alt: row.altKey === true, meta: row.metaKey === true };
  for (const part of parts) {
    const lower = part.toLowerCase();
    if (lower === 'shift') modifiers.shift = true;
    else if (lower === 'ctrl' || lower === 'control') modifiers.control = true;
    else if (lower === 'alt' || lower === 'option') modifiers.alt = true;
    else if (lower === 'meta' || lower === 'cmd' || lower === 'command') modifiers.meta = true;
  }
  const aliases = { esc: 'Escape', return: 'Enter', spacebar: 'Space', del: 'Delete', ins: 'Insert', left: 'ArrowLeft', right: 'ArrowRight', up: 'ArrowUp', down: 'ArrowDown' };
  const lower = key.toLowerCase();
  if (aliases[lower]) key = aliases[lower];
  else if (/^f(?:[1-9]|1[0-2])$/i.test(key)) key = key.toUpperCase();
  else if (/^arrow(left|right|up|down)$/i.test(key)) key = 'Arrow' + key.slice(5, 6).toUpperCase() + key.slice(6).toLowerCase();
  if (key.length === 1 && /[A-Z]/.test(key)) modifiers.shift = true;
  return { key, modifiers };
}

async function dispatchMacBrowserInput(request, action, signal) {
  if (!request || request.browserFamily !== 'firefox') {
    await execNativeInput('/usr/bin/osascript', ['-e', 'tell application "System Events" to ' + action], { signal });
    return 'current_frontmost';
  }
  const targetTitle = String(request.windowTitle || '').trim().slice(0, 240);
  const allowedApplicationIds = new Set(['org.mozilla.firefox', 'org.mozilla.firefoxdeveloperedition', 'org.mozilla.nightly']);
  const configuredApplicationId = String(process.env.HX0_MCP_MAC_BROWSER_APP_ID || '').trim();
  const applicationId = allowedApplicationIds.has(configuredApplicationId) ? configuredApplicationId : '';
  const preferredApplication = applicationId ? [
    'tell application id "' + applicationId + '" to activate',
    'delay 0.12',
    'tell application "System Events"',
    action,
    'end tell',
    'return "app_id:' + applicationId + '"',
  ] : [];
  const script = [
    'on run argv',
    'set targetTitle to ""',
    'if (count of argv) > 0 then set targetTitle to item 1 of argv',
    ...preferredApplication,
    'tell application "System Events"',
    'set browserProcesses to every application process whose bundle identifier starts with "org.mozilla.firefox"',
    'set browserProcesses to browserProcesses & (every application process whose bundle identifier starts with "org.mozilla.nightly")',
    'set browserProcesses to browserProcesses & (every application process whose name is "firefox")',
    'set browserProcesses to browserProcesses & (every application process whose name starts with "Firefox")',
    'if (count of browserProcesses) is 0 then error "Firefox is not running"',
    'if targetTitle is not "" then',
    'repeat with candidateProcess in browserProcesses',
    'repeat with candidateWindow in windows of candidateProcess',
    'try',
    'if (name of candidateWindow contains targetTitle) then',
    'set frontmost of candidateProcess to true',
    'perform action "AXRaise" of candidateWindow',
    'delay 0.12',
    action,
    'return "matched:" & targetTitle',
    'end if',
    'end try',
    'end repeat',
    'end repeat',
    'end if',
    'repeat with candidateProcess in browserProcesses',
    'try',
    'if (count of windows of candidateProcess) > 0 then',
    'set frontmost of candidateProcess to true',
    'perform action "AXRaise" of front window of candidateProcess',
    'delay 0.12',
    action,
    'return "fallback:" & targetTitle',
    'end if',
    'end try',
    'end repeat',
    'error "No Firefox browser window is available"',
    'end tell',
    'end run',
  ].join('\n');
  return execNativeInput('/usr/bin/osascript', ['-e', script, '--', targetTitle], { signal });
}

async function dispatchMacNativeInput(request, signal) {
  if (request.kind === 'mouse') {
    const x = Math.round(Number(request.x));
    const y = Math.round(Number(request.y));
    if (!Number.isFinite(x) || !Number.isFinite(y) || Math.abs(x) > 100_000 || Math.abs(y) > 100_000) throw new Error('Invalid native mouse coordinates');
    const focusTarget = await dispatchMacBrowserInput(request, 'click at {' + x + ', ' + y + '}', signal);
    return { ok: true, backend: 'macos_system_events', focusTarget, kind: 'mouse', action: 'click', x, y };
  }
  const normalized = normalizeNativeKeyRequest(request);
  const keyCodes = { Enter: 36, Tab: 48, Space: 49, Backspace: 51, Escape: 53, Meta: 55, Shift: 56, Alt: 58, Control: 59, F1: 122, F2: 120, F3: 99, F4: 118, F5: 96, F6: 97, F7: 98, F8: 100, F9: 101, F10: 109, F11: 103, F12: 111, Home: 115, PageUp: 116, Delete: 117, End: 119, PageDown: 121, ArrowLeft: 123, ArrowRight: 124, ArrowDown: 125, ArrowUp: 126 };
  const modifierNames = [];
  if (normalized.modifiers.shift) modifierNames.push('shift down');
  if (normalized.modifiers.control) modifierNames.push('control down');
  if (normalized.modifiers.alt) modifierNames.push('option down');
  if (normalized.modifiers.meta) modifierNames.push('command down');
  const usingClause = modifierNames.length ? ' using {' + modifierNames.join(', ') + '}' : '';
  let action;
  if (Object.prototype.hasOwnProperty.call(keyCodes, normalized.key)) action = 'key code ' + keyCodes[normalized.key] + usingClause;
  else if (normalized.key.length === 1) action = 'keystroke ' + JSON.stringify(normalized.key.toLowerCase()) + usingClause;
  else throw new Error('Unsupported native key on macOS: ' + normalized.key);
  const focusTarget = await dispatchMacBrowserInput(request, action, signal);
  return { ok: true, backend: 'macos_system_events', focusTarget, kind: 'key', key: normalized.key, modifiers: normalized.modifiers };
}

async function dispatchLinuxNativeInput(request, signal) {
  if (request.kind === 'mouse') {
    const x = Math.round(Number(request.x));
    const y = Math.round(Number(request.y));
    if (!Number.isFinite(x) || !Number.isFinite(y)) throw new Error('Invalid native mouse coordinates');
    await execNativeInput('xdotool', ['mousemove', '--sync', String(x), String(y), 'click', '1'], { signal });
    return { ok: true, backend: 'linux_xdotool', kind: 'mouse', action: 'click', x, y };
  }
  const normalized = normalizeNativeKeyRequest(request);
  const aliases = { Enter: 'Return', Space: 'space', Backspace: 'BackSpace', Escape: 'Escape', ArrowLeft: 'Left', ArrowRight: 'Right', ArrowUp: 'Up', ArrowDown: 'Down', PageUp: 'Prior', PageDown: 'Next', Meta: 'Super_L', Control: 'Control_L', Alt: 'Alt_L', Shift: 'Shift_L' };
  const parts = [];
  if (normalized.modifiers.control) parts.push('ctrl');
  if (normalized.modifiers.alt) parts.push('alt');
  if (normalized.modifiers.shift) parts.push('shift');
  if (normalized.modifiers.meta) parts.push('super');
  parts.push(aliases[normalized.key] || normalized.key);
  await execNativeInput('xdotool', ['key', '--clearmodifiers', parts.join('+')], { signal });
  return { ok: true, backend: 'linux_xdotool', kind: 'key', key: normalized.key, modifiers: normalized.modifiers };
}

async function dispatchWindowsNativeInput(request, signal) {
  const powershell = process.env.SystemRoot ? path.join(process.env.SystemRoot, 'System32', 'WindowsPowerShell', 'v1.0', 'powershell.exe') : 'powershell.exe';
  if (request.kind === 'mouse') {
    const x = Math.round(Number(request.x));
    const y = Math.round(Number(request.y));
    if (!Number.isFinite(x) || !Number.isFinite(y)) throw new Error('Invalid native mouse coordinates');
    const script = 'Add-Type -TypeDefinition \"using System; using System.Runtime.InteropServices; public static class Hx0Input { [DllImport(\\\"user32.dll\\\")] public static extern bool SetCursorPos(int X,int Y); [DllImport(\\\"user32.dll\\\")] public static extern void mouse_event(uint f,uint dx,uint dy,uint data,UIntPtr extra); }\"; [Hx0Input]::SetCursorPos(' + x + ',' + y + ') | Out-Null; [Hx0Input]::mouse_event(2,0,0,0,[UIntPtr]::Zero); [Hx0Input]::mouse_event(4,0,0,0,[UIntPtr]::Zero)';
    await execNativeInput(powershell, ['-NoProfile', '-NonInteractive', '-Command', script], { signal });
    return { ok: true, backend: 'windows_sendinput', kind: 'mouse', action: 'click', x, y };
  }
  const normalized = normalizeNativeKeyRequest(request);
  const specials = { Enter: '{ENTER}', Tab: '{TAB}', Space: ' ', Backspace: '{BACKSPACE}', Escape: '{ESC}', Delete: '{DELETE}', Insert: '{INSERT}', Home: '{HOME}', End: '{END}', PageUp: '{PGUP}', PageDown: '{PGDN}', ArrowLeft: '{LEFT}', ArrowRight: '{RIGHT}', ArrowUp: '{UP}', ArrowDown: '{DOWN}', F1: '{F1}', F2: '{F2}', F3: '{F3}', F4: '{F4}', F5: '{F5}', F6: '{F6}', F7: '{F7}', F8: '{F8}', F9: '{F9}', F10: '{F10}', F11: '{F11}', F12: '{F12}' };
  let sendKey = specials[normalized.key] || (normalized.key.length === 1 ? normalized.key.toLowerCase().replace(/[+^%~(){}\[\]]/g, '{$&}') : '');
  if (!sendKey) throw new Error('Unsupported native key on Windows: ' + normalized.key);
  if (normalized.modifiers.shift) sendKey = '+' + sendKey;
  if (normalized.modifiers.control) sendKey = '^' + sendKey;
  if (normalized.modifiers.alt) sendKey = '%' + sendKey;
  const escaped = sendKey.replace(/'/g, "''");
  await execNativeInput(powershell, ['-NoProfile', '-NonInteractive', '-Command', "Add-Type -AssemblyName System.Windows.Forms; [System.Windows.Forms.SendKeys]::SendWait('" + escaped + "')"], { signal });
  return { ok: true, backend: 'windows_sendkeys', kind: 'key', key: normalized.key, modifiers: normalized.modifiers };
}

async function dispatchNativeInputRequest(request, signal) {
  const row = request && typeof request === 'object' ? request : {};
  if (row.kind !== 'key' && row.kind !== 'mouse') throw new Error('Unsupported native input request');
  if (process.platform === 'darwin') return dispatchMacNativeInput(row, signal);
  if (process.platform === 'win32') return dispatchWindowsNativeInput(row, signal);
  if (process.platform === 'linux') return dispatchLinuxNativeInput(row, signal);
  throw new Error('Native input is unsupported on ' + process.platform);
}

async function completeNativeInput(tool, preparedInput, result, signal, hostId) {
  signal?.throwIfAborted();
  if (result?.ok === false) return result;
  const request = result && result.nativeInputRequest;
  if (!request) {
    const pendingMode = String(result && (result.inputMode || result.input_mode) || '').toLowerCase();
    if (pendingMode === 'native_pending') {
      return Object.assign({}, result, {
        ok: false,
        error: 'Firefox native input relay did not receive nativeInputRequest. Update and restart the HawkEye MCP server; native_pending is an internal extension/server handshake state.',
        category: 'native_input_protocol_mismatch',
        hint: 'Replace the MCP server file with the one bundled in the same HawkEye release, then restart the MCP host.',
      });
    }
    return result;
  }
  let inputDispatched = false;
  try {
    signal?.throwIfAborted();
    const nativeInput = await dispatchNativeInputRequest(request, signal);
    inputDispatched = true;
    await new Promise((resolve) => setTimeout(resolve, 160));
    const finalized = await callExtension('browser_native_input_finalize', { tabId: result.tabId, frameId: result.frameId, probeToken: result.probeToken }, { signal, hostId });
    if (!finalized || typeof finalized !== 'object') throw new Error('No native input verification result');
    const merged = Object.assign({}, result, finalized || {}, { ok: finalized?.ok !== false, inputDispatched: true, outcomeVerified: false, inputMode: request.kind === 'key' ? 'native_os_key' : 'native_os_mouse', input_mode: request.kind === 'key' ? 'native_os_key' : 'native_os_mouse', inputModes: [request.kind === 'key' ? 'native_os_key' : 'native_os_mouse'], nativeInput });
    delete merged.nativeInputRequest;
    delete merged.probeToken;
    return merged;
  } catch (error) {
    signal?.throwIfAborted();
    if (inputDispatched) return { ok: false, inputDispatched: true, outcomeVerified: false, category: 'input_verification_failed', error: String(error && error.message || error), hint: 'Input was already sent. Inspect the page before retrying to avoid duplicate actions.' };
    const mode = String(preparedInput && (preparedInput.inputMode || preparedInput.input_mode || preparedInput.clickMode || preparedInput.click_mode) || 'auto').toLowerCase();
    if (mode === 'auto') {
      const fallbackInput = Object.assign({}, preparedInput, tool === 'browser_click' ? { clickMode: 'js' } : { inputMode: 'js' });
      const fallback = await callExtension(tool, fallbackInput, { signal, hostId });
      return Object.assign({}, fallback || {}, { nativeFallbackUsed: true, nativeInputError: String(error && error.message || error), inputModes: ['native_failed', fallback && (fallback.input_mode || fallback.inputMode) || 'js'] });
    }
    return Object.assign({}, result, { ok: false, error: 'Native input failed: ' + String(error && error.message || error), category: 'trusted_input_unavailable', hint: process.platform === 'darwin' ? 'Allow Accessibility control for the terminal/MCP host in macOS Privacy & Security, then retry.' : 'Install/enable the local OS input backend, then retry.' });
  }
}

function parsePort() {
  const argv = process.argv.slice(2);
  const at = argv.findIndex((item) => item === '--ws-port' || item === '--port');
  const raw = at >= 0 ? argv[at + 1] : process.env.HX0_MCP_WS_PORT;
  const port = Number(raw || DEFAULT_WS_PORT);
  if (!Number.isInteger(port) || port < 1024 || port > 65535) {
    throw new Error('Invalid WebSocket port: ' + String(raw));
  }
  return port;
}

if (process.argv.includes('--version')) {
  process.stdout.write(SERVER_VERSION + '\n');
  process.exit(0);
}
if (process.argv.includes('--help')) {
  process.stdout.write([
    'Hx0 HawkEye MCP Server v' + SERVER_VERSION,
    '',
    'Usage: node hawkeye-mcp-server.mjs [--port 19016] [--profile full|core|research|security] [--caps vision,files,forms,devtools,research,security]',
    'Environment: HX0_MCP_WS_PORT=19016 HX0_MCP_PROFILE=full HX0_MCP_CAPS=',
    '',
    'VIP / Pro access is required to enable the HawkEye extension bridge and call tools.',
    'Enable “HawkEye Browser Automation MCP” in the HawkEye extension popup, then add this file',
    'as a stdio MCP server in any MCP client. HTTP-capable hosts may connect to:',
    '  Streamable HTTP: http://127.0.0.1:19016/mcp',
    '  Legacy SSE:      http://127.0.0.1:19016/sse',
    'Use --keep-alive for an HTTP-only service with closed stdin.',
    'Chrome and Firefox use this same file. HX0_MCP_BROWSER=chrome|firefox optionally pins a browser.',
    'Without a preference the first ready browser stays active; others remain on standby until it disconnects.',
    '',
  ].join('\n'));
  process.exit(0);
}

const TOOLS = [
  {
    name: 'browser_navigate',
    description: 'Navigate the HawkEye-connected real browser tab to an HTTP(S) URL and return a bounded page preview. Private/lab hosts (RFC1918, localhost, link-local) with a self-signed or otherwise untrusted certificate are auto-proceeded past the browser interstitial (Chrome: Advanced → Proceed; Firefox: Accept the Risk). The address bar may still show Not secure; use browser_security for diagnosis. Public-site certificate warnings are not auto-proceeded.',
    inputSchema: { type: 'object', properties: { url: { type: 'string', description: 'Absolute or current-page-relative HTTP(S) URL.' } }, required: ['url'], additionalProperties: false },
  },
  {
    name: 'browser_search',
    description: 'Search the public web with multiple engines and/or query variants, deduplicate URLs, fuse rankings, expose engine telemetry, and optionally fetch top evidence. Supported engines: auto, bing, baidu, google, duckduckgo, sogou, so360, yahoo, yandex, brave, sm. ' + SEARCH_OPERATOR_GUIDE + ' This is HawkEye-owned and has no external search-service dependency.',
    inputSchema: { type: 'object', properties: { query: { type: 'string', description: SEARCH_OPERATOR_GUIDE }, queries: { type: 'array', maxItems: 6, items: { type: 'string', description: SEARCH_OPERATOR_GUIDE }, description: 'Optional extra query variants. Each item should also use search operators when a tighter query is possible.' }, engine: { type: 'string', enum: ['auto', 'bing', 'baidu', 'google', 'duckduckgo', 'ddg', 'sogou', 'so360', '360', 'qihoo', 'so', 'yahoo', 'yandex', 'brave', 'sm', 'shenma'] }, engines: { type: 'array', maxItems: 8, items: { type: 'string', enum: ['auto', 'bing', 'baidu', 'google', 'duckduckgo', 'ddg', 'sogou', 'so360', '360', 'qihoo', 'so', 'yahoo', 'yandex', 'brave', 'sm', 'shenma'] } }, maxResults: { type: 'integer', minimum: 1, maximum: 25 }, maxFetches: { type: 'integer', minimum: 0, maximum: 5 }, includeDomains: { type: 'array', maxItems: 12, items: { type: 'string' } }, excludeDomains: { type: 'array', maxItems: 12, items: { type: 'string' } }, timeRange: { type: 'string', enum: ['day', 'week', 'month', 'year'] }, exactMatch: { type: 'boolean' }, cacheMode: { type: 'string', enum: ['prefer', 'refresh', 'only'] }, context_budget_chars: { type: 'integer', minimum: 12000, maximum: 300000 } }, additionalProperties: false },
  },
  {
    name: 'browser_fetch',
    description: 'Open a public HTTP(S) URL in an isolated temporary browser tab (session cookies apply), extract rendered readable text, detect challenge pages, cache bounded evidence, and record the exchange into HawkEye capture history unless capture=false. Private/local addresses are rejected.',
    inputSchema: { type: 'object', properties: { url: { type: 'string' }, maxChars: { type: 'integer', minimum: 4000, maximum: 160000 }, cacheMode: { type: 'string', enum: ['prefer', 'refresh', 'only'] }, capture: { type: 'boolean', description: 'Record the fetched page into HawkEye capture history. Defaults to true.' }, timeoutMs: { type: 'integer', minimum: 500, maximum: 20000 }, context_budget_chars: { type: 'integer', minimum: 12000, maximum: 300000 } }, required: ['url'], additionalProperties: false },
  },
  {
    name: 'browser_research',
    description: 'Run a bounded autonomous public-web evidence-gathering loop: derive query variants, search multiple engines, fuse rankings, fetch top pages, and return citation-ready sources plus gaps and transparent degradation telemetry. When supplying queries, ' + SEARCH_OPERATOR_GUIDE,
    inputSchema: { type: 'object', properties: { prompt: { type: 'string' }, queries: { type: 'array', maxItems: 6, items: { type: 'string', description: SEARCH_OPERATOR_GUIDE } }, depth: { type: 'string', enum: ['quick', 'standard', 'deep'] }, engines: { type: 'array', maxItems: 8, items: { type: 'string', enum: ['auto', 'bing', 'baidu', 'google', 'duckduckgo', 'ddg', 'sogou', 'so360', '360', 'qihoo', 'so', 'yahoo', 'yandex', 'brave', 'sm', 'shenma'] } }, maxPages: { type: 'integer', minimum: 1, maximum: 15 }, maxTimeMs: { type: 'integer', minimum: 10000, maximum: 180000 }, includeDomains: { type: 'array', maxItems: 12, items: { type: 'string' } }, excludeDomains: { type: 'array', maxItems: 12, items: { type: 'string' } }, timeRange: { type: 'string', enum: ['day', 'week', 'month', 'year'] }, cacheMode: { type: 'string', enum: ['prefer', 'refresh', 'only'] }, context_budget_chars: { type: 'integer', minimum: 16000, maximum: 300000 } }, required: ['prompt'], additionalProperties: false },
  },
  {
    name: 'browser_go_back',
    description: 'Go back in the connected browser tab and return a fresh accessibility snapshot.',
    inputSchema: { type: 'object', properties: {}, additionalProperties: false },
  },
  {
    name: 'browser_go_forward',
    description: 'Go forward in the connected browser tab and return a fresh accessibility snapshot.',
    inputSchema: { type: 'object', properties: {}, additionalProperties: false },
  },
  {
    name: 'browser_snapshot',
    description: 'Return a compact, viewport-first accessibility snapshot with stable frame refs and stable data ids. Heavy pages default to in-viewport nodes (max ~200). Pass viewport_only=false or raise max_elements for the full tree. Compact mode notes folded interactive items. Decorative images and HawkEye/Monica/Coco overlays are omitted by default.',
    inputSchema: { type: 'object', properties: { boxes: { type: 'boolean', description: 'Include viewport-relative x/y/width/height for exposed elements.' }, compact: { type: 'boolean', description: 'Deduplicate semantic rows and omit decorative noise. Defaults to true.' }, expand_selector: { type: 'string', description: 'Keep matching nodes even when compact folding would hide them.' }, diff: { type: 'boolean', description: 'Return only new/changed/removed semantic nodes since the previous snapshot for this focus scope.' }, exclude_img: { type: 'boolean', description: 'Exclude all standalone images; decorative images are already excluded in compact mode.' }, focus: { type: 'string', description: 'Scope to main, list, or a CSS selector.' }, max_elements: { type: 'integer', minimum: 20, maximum: 1000 }, viewport_only: { type: 'boolean', description: 'Only include nodes that intersect the current viewport. Defaults to true.' }, exclude_selectors: { type: 'array', items: { type: 'string' }, maxItems: 20 }, include_overlays: { type: 'boolean' }, context_budget_chars: { type: 'integer', minimum: 12000, maximum: 400000, description: 'Maximum snapshot characters returned in this context chunk.' }, cursor: { type: 'integer', minimum: 0, description: 'Character cursor returned by a previous snapshot chunk.' } }, additionalProperties: false },
  },
  {
    name: 'browser_find',
    description: 'Search the compact accessibility snapshot for plain text or a regular expression and return only bounded surrounding snippets. Prefer this over a full snapshot when locating content on a large page.',
    inputSchema: { type: 'object', properties: { text: { type: 'string' }, regex: { type: 'string' }, flags: { type: 'string' }, before: { type: 'integer', minimum: 0, maximum: 8 }, after: { type: 'integer', minimum: 0, maximum: 8 }, limit: { type: 'integer', minimum: 1, maximum: 100 }, compact: { type: 'boolean' }, focus: { type: 'string' }, max_elements: { type: 'integer', minimum: 20, maximum: 1000 } }, additionalProperties: false },
  },
  {
    name: 'browser_security',
    description: 'Read structured TLS/certificate evidence for the connected page and diagnose hostname mismatch, expiry, not-yet-valid, untrusted-chain, or browser TLS errors. Auto-proceeding a private/lab interstitial does not make the certificate trusted. Never infer certificate validity from a screenshot or page body. If evidence is absent, refresh=true performs one diagnostic reload.',
    inputSchema: { type: 'object', properties: { refresh: { type: 'boolean' }, timeoutMs: { type: 'integer', minimum: 500, maximum: 20000 } }, additionalProperties: false },
  },
  {
    name: 'browser_click',
    description: 'Click the smallest real interactive target (inner a/button, not a wrapping card). Auto uses Chrome CDP or the Firefox bundled local native-input relay so activation-gated APIs receive a real OS pointer event, then falls back to JS if native input is unavailable. trusted/native fail closed. Returns eventEvidence/userActivationObserved when measurable. Input delivery does not prove fullscreen, playback or navigation succeeded; verify the resulting page state.',
    inputSchema: { type: 'object', properties: { element: { type: 'string', description: 'Human-readable description only; it is not exact-match text.' }, text: { type: 'string', description: 'Authoritative accessible text. Required when exact=true.' }, exact: { type: 'boolean', description: 'Exact accessible-name match; folds whitespace only and is case-sensitive by default.' }, caseSensitive: { type: 'boolean', description: 'Override text case sensitivity.' }, clickMode: { type: 'string', enum: ['auto', 'js', 'trusted', 'native'], description: 'auto prefers browser-supported trusted input and falls back to JS; trusted/native require trusted input; js sends a synthetic DOM click.' }, stableId: { type: 'string', description: 'Stable business id returned by browser_snapshot, such as data-challenge-id:42 or route-id:42.' }, ref: { type: 'string', description: 'Element ref from browser_snapshot.' }, selector: { type: 'string', description: 'Optional CSS selector fallback.' } }, additionalProperties: false },
  },
  {
    name: 'browser_hover',
    description: 'Hover an element in the connected page. Prefer a ref returned by browser_snapshot.',
    inputSchema: { type: 'object', properties: { element: { type: 'string' }, ref: { type: 'string' }, selector: { type: 'string' } }, additionalProperties: false },
  },
  {
    name: 'browser_type',
    description: 'Type text into a native input, textarea, contenteditable, or same-origin rich-text editor. A visible form label may be used as element. Replaces existing value unless clear=false; submit=true submits its form.',
    inputSchema: { type: 'object', properties: { element: { type: 'string' }, ref: { type: 'string' }, selector: { type: 'string' }, text: { type: 'string' }, clear: { type: 'boolean' }, submit: { type: 'boolean' } }, required: ['text'], additionalProperties: false },
  },
  {
    name: 'browser_select_option',
    description: 'Select options from native or custom dropdowns (including Ant Design/Element-style controls). Pass the visible field label in element and the desired visible option in value; this performs the trusted open-and-option click sequence.',
    inputSchema: { type: 'object', properties: { element: { type: 'string' }, ref: { type: 'string' }, selector: { type: 'string' }, values: { type: 'array', items: { type: 'string' } }, value: { type: 'string' } }, additionalProperties: false },
  },
  {
    name: 'browser_press_key',
    description: 'Send a key such as f, Enter, Tab, Escape, ArrowDown, or Ctrl+L. Auto uses Chrome CDP or the Firefox bundled local native-input relay so activation-gated APIs receive a real OS keyboard event; trusted/native fail closed rather than silently emitting synthetic DOM events. Input delivery does not prove fullscreen or playback succeeded; verify the resulting page state.',
    inputSchema: { type: 'object', properties: { key: { type: 'string' }, element: { type: 'string' }, ref: { type: 'string' }, selector: { type: 'string' }, inputMode: { type: 'string', enum: ['auto', 'js', 'trusted', 'native'], description: 'auto prefers browser-supported trusted input and falls back to JS; trusted/native require trusted input; js sends synthetic DOM KeyboardEvents.' }, shiftKey: { type: 'boolean' }, ctrlKey: { type: 'boolean' }, altKey: { type: 'boolean' }, metaKey: { type: 'boolean' } }, required: ['key'], additionalProperties: false },
  },
  {
    name: 'browser_wait',
    description: 'Wait briefly for dynamic UI. Defaults to 0.3s and does not return a page snapshot unless includeSnapshot is true.',
    inputSchema: { type: 'object', properties: { time: { type: 'number', minimum: 0, maximum: 10, description: 'Seconds to wait. Defaults to 0.3.' }, includeSnapshot: { type: 'boolean', description: 'If true, return a fresh accessibility snapshot after waiting.' } }, additionalProperties: false },
  },
  {
    name: 'browser_get_console_logs',
    description: 'Read console messages and unhandled page errors captured since HawkEye connected.',
    inputSchema: { type: 'object', properties: { limit: { type: 'integer', minimum: 1, maximum: 300 } }, additionalProperties: false },
  },
  {
    name: 'browser_screenshot',
    description: 'Capture the viewport, full page, or one element and return the image together with a compact accessibility note by default, so both vision and text-only models can reason about the same state.',
    inputSchema: { type: 'object', properties: { fullPage: { type: 'boolean', description: 'Capture the complete scrollable page. Cannot be combined with an element target.' }, element: { type: 'string', description: 'Human-readable element description.' }, ref: { type: 'string', description: 'Element ref from browser_snapshot.' }, selector: { type: 'string', description: 'CSS selector fallback for an element screenshot.' }, target: { type: 'string', description: 'Playwright-compatible shorthand: an element ref or CSS selector.' }, type: { type: 'string', enum: ['png', 'jpeg'] }, quality: { type: 'integer', minimum: 1, maximum: 100, description: 'JPEG quality. Ignored for PNG.' }, includeSnapshot: { type: 'boolean', description: 'Include a compact accessibility note. Defaults to true.' }, compact: { type: 'boolean' }, exclude_img: { type: 'boolean' }, focus: { type: 'string' }, max_elements: { type: 'integer', minimum: 20, maximum: 1000 }, save_to_file: { type: 'boolean', description: 'Also save the screenshot to a local image file. Defaults to false; the image response is always preserved.' }, file_path: { type: 'string', maxLength: 4096, description: 'Optional destination. Absolute paths are used directly; relative paths are resolved below HAWKEYE_SCREENSHOT_DIR or $CWD/screenshots.' }, overwrite: { type: 'boolean', description: 'Allow replacement of an existing screenshot file. Defaults to false.' } }, additionalProperties: false },
  },
  {
    name: 'browser_captcha_assist',
    description: 'Detect and assist with a custom or first-party CAPTCHA on the connected page. For login image codes (checkCode next to 验证码), always use this tool: action=analyze returns an upscaled crop. Read only the large dark foreground characters; ignore colorful lines and faint background glyphs. Do not hawkeye_evaluate pixel dumps, download the img src, or click the image (click refreshes). Then action=solve with authorized=true and answer. For sliders, analyze may open a gated dialog and return suggestedOffsetRatio; solve drags with a paced pointer and waits for READY/granted. Do not immediately re-solve while verification.phase is busy or the copy still says SCANNING. reCAPTCHA, hCaptcha, Turnstile, Arkose, GeeTest, Tencent and Alibaba anti-bot challenges are reported as manual_required and are never automated.',
    inputSchema: {
      type: 'object',
      properties: {
        action: { type: 'string', enum: ['analyze', 'solve'], description: 'Defaults to analyze.' },
        authorized: { type: 'boolean', description: 'Required for solve. Confirms the caller is authorized to automate this first-party challenge.' },
        challengeType: { type: 'string', enum: ['auto', 'image_text', 'copy_text', 'math', 'slider'], description: 'Optional detection preference.' },
        candidateIndex: { type: 'integer', minimum: 0, maximum: 11, description: 'Choose one candidate returned by analyze. Defaults to 0.' },
        answer: { type: 'string', description: 'Recognized image/copy text or calculated arithmetic answer.' },
        offsetRatio: { type: 'number', minimum: 0, maximum: 1, description: 'Slider target center measured from the track left edge (0) to right edge (1).' },
        submit: { type: 'boolean', description: 'Submit the containing form after filling. Defaults to false.' },
        type: { type: 'string', enum: ['png', 'jpeg'] },
      },
      additionalProperties: false,
    },
  },
  {
    name: 'browser_resize',
    description: 'Resize the page CSS viewport for responsive-layout testing. Prefers Chrome DevTools Emulation.setDeviceMetricsOverride; falls back to resizing the browser window. Returns requested vs applied viewport. Use action=reset after testing.',
    inputSchema: { type: 'object', properties: { width: { type: 'integer', minimum: 320, maximum: 3840 }, height: { type: 'integer', minimum: 240, maximum: 2160 }, action: { type: 'string', enum: ['resize', 'reset'] }, reset: { type: 'boolean', description: 'Equivalent to action=reset.' } }, additionalProperties: false },
  },
  {
    name: 'browser_tabs',
    description: 'List, create, bind/select, or close real browser tabs. Selecting a tab updates the HawkEye MCP binding for subsequent calls.',
    inputSchema: { type: 'object', properties: { action: { type: 'string', enum: ['list', 'new', 'select', 'close'] }, tabId: { type: 'integer' }, url: { type: 'string' }, active: { type: 'boolean' }, bind: { type: 'boolean' }, allWindows: { type: 'boolean' } }, additionalProperties: false },
  },
  {
    name: 'browser_reload',
    description: 'Reload the connected tab, optionally bypassing cache, then return a fresh accessibility snapshot.',
    inputSchema: { type: 'object', properties: { bypassCache: { type: 'boolean' }, timeoutMs: { type: 'integer', minimum: 500, maximum: 20000 } }, additionalProperties: false },
  },
  {
    name: 'browser_wait_for',
    description: 'Wait until page readiness, URL text, visible/hidden element state, or page text matches, then return the wait result.',
    inputSchema: { type: 'object', properties: { ref: { type: 'string' }, selector: { type: 'string' }, element: { type: 'string' }, text: { type: 'string' }, urlContains: { type: 'string' }, state: { type: 'string', enum: ['visible', 'hidden', 'attached', 'detached'] }, timeoutMs: { type: 'integer', minimum: 100, maximum: 30000 }, pollMs: { type: 'integer', minimum: 50, maximum: 2000 } }, additionalProperties: false },
  },
  {
    name: 'browser_fill_form',
    description: 'Fill multiple native or custom form controls in one operation using refs, selectors, or visible labels. Supports custom dropdowns and contenteditable/rich-text fields; optionally submits the containing form.',
    inputSchema: { type: 'object', properties: { fields: { type: 'array', minItems: 1, maxItems: 50, items: { type: 'object', properties: { ref: { type: 'string' }, selector: { type: 'string' }, element: { type: 'string' }, name: { type: 'string' }, label: { type: 'string' }, value: {}, values: { type: 'array', items: {} } }, additionalProperties: false } }, submit: { type: 'boolean' } }, required: ['fields'], additionalProperties: false },
  },
  {
    name: 'browser_exam_questions',
    description: 'Extract structured choice, true/false, and text questions from the page or nested iframes, with frame-scoped refs.',
    inputSchema: { type: 'object', properties: { context_budget_chars: { type: 'integer', minimum: 12000, maximum: 400000 }, cursor: { type: 'integer', minimum: 0 } }, additionalProperties: false },
  },
  {
    name: 'browser_answer_questions',
    description: 'Fill a bounded set of exam/form answers without submitting. Match questions by index or text.',
    inputSchema: { type: 'object', properties: { answers: { type: 'array', minItems: 1, maxItems: 100, items: { type: 'object', properties: { index: { type: 'integer' }, number: { type: 'integer' }, question: { type: 'string' }, answer: {}, answers: { type: 'array', items: {} }, value: {} }, additionalProperties: false } } }, required: ['answers'], additionalProperties: false },
  },
  {
    name: 'browser_handle_dialog',
    description: 'Configure the next JavaScript confirm/prompt answer and read recently captured alert/confirm/prompt evidence without blocking browser automation.',
    inputSchema: { type: 'object', properties: { action: { type: 'string', enum: ['accept', 'dismiss'] }, promptText: { type: 'string' }, clear: { type: 'boolean' }, limit: { type: 'integer', minimum: 1, maximum: 50 } }, additionalProperties: false },
  },
  {
    name: 'browser_file_upload',
    description: 'Attach local files to a file input. The local MCP Server reads the paths and securely transfers bounded file bytes to the connected page.',
    inputSchema: { type: 'object', properties: { ref: { type: 'string' }, selector: { type: 'string' }, element: { type: 'string' }, path: { type: 'string' }, paths: { type: 'array', minItems: 1, maxItems: 10, items: { type: 'string' } } }, additionalProperties: false },
  },
  {
    name: 'browser_download_file',
    description: 'Discover downloadable resources on the connected page or add an explicit HTTP(S) file URL to the browser native download queue. Supports video, audio, documents, spreadsheets, PDFs, archives, installers, and other browser-downloadable files. action=discover never starts a download; follow it with action=download. A download is successful only when nativeDownloadStarted=true and downloadId are returned.',
    inputSchema: {
      type: 'object',
      properties: {
        action: { type: 'string', enum: ['discover', 'download'] },
        url: { type: 'string', description: 'Absolute or current-page-relative HTTP(S) file URL. Omit to discover from page media and file links.' },
        filename: { type: 'string', description: 'Optional basename for the downloaded file.' },
        resourceHint: { type: 'string', description: 'Filename, extension, label, or media hint used to rank discovered resources.' },
        saveAs: { type: 'boolean', description: 'Show the browser Save As dialog. Defaults to false.' },
        conflictAction: { type: 'string', enum: ['uniquify', 'overwrite', 'prompt'] },
      },
      additionalProperties: false,
    },
  },
  {
    name: 'browser_drag',
    description: 'Drag one element and drop it onto another using a trusted pointer path plus HTML5 drag-and-drop events. Use for ordinary sliders, sortable lists, kanban cards, and canvas drag interactions. Use browser_captcha_assist for an authorized first-party CAPTCHA. Prefer refs from browser_snapshot for both the start and end targets; both must be in the same frame.',
    inputSchema: { type: 'object', properties: { startElement: { type: 'string', description: 'Human-readable description of the element to drag.' }, startRef: { type: 'string', description: 'Start element ref from browser_snapshot.' }, startSelector: { type: 'string', description: 'Optional CSS selector for the start element.' }, endElement: { type: 'string', description: 'Human-readable description of the drop target.' }, endRef: { type: 'string', description: 'End element ref from browser_snapshot.' }, endSelector: { type: 'string', description: 'Optional CSS selector for the drop target.' } }, additionalProperties: false },
  },
  {
    name: 'browser_scroll',
    description: 'Scroll the page or a scrollable container to trigger lazy-loading and reach off-screen content, then return a fresh accessibility snapshot. Provide direction plus optional amount, or to=top/bottom, or a ref/selector/element to scroll that target into view.',
    inputSchema: { type: 'object', properties: { direction: { type: 'string', enum: ['up', 'down', 'left', 'right'], description: 'Scroll direction. Defaults to down.' }, amount: { type: 'integer', minimum: 1, maximum: 40000, description: 'Pixels to scroll. Defaults to ~85% of the viewport height.' }, to: { type: 'string', enum: ['top', 'bottom'], description: 'Jump to the top or bottom instead of scrolling by an amount.' }, element: { type: 'string', description: 'Human-readable description of an element or container to scroll into view or scroll within.' }, ref: { type: 'string', description: 'Element ref from browser_snapshot.' }, selector: { type: 'string', description: 'Optional CSS selector fallback.' } }, additionalProperties: false },
  },
  {
    name: 'browser_read_text',
    description: 'Extract cleaned, human-readable text from the connected page or a specific container, including open Shadow DOM and injectable iframes. Use to read long articles, documentation, or page source that the accessibility snapshot condenses. Returns the current text chunk in value, with text_length for the full extraction and context.next_page_token for immutable continuation pages.',
    inputSchema: { type: 'object', properties: { selector: { type: 'string', description: 'Optional CSS selector to scope extraction; defaults to the main/article/body content.' }, maxChars: { type: 'integer', minimum: 1000, maximum: 400000, description: 'Maximum characters to extract from the page.' }, context_budget_chars: { type: 'integer', minimum: 12000, maximum: 400000, description: 'Maximum text characters returned in this context chunk.' }, cursor: { type: 'integer', minimum: 0, description: 'Character cursor returned by a previous chunk.' } }, additionalProperties: false },
  },
  {
    name: 'hawkeye_capture_start',
    description: 'Enable HawkEye browser traffic capture.',
    inputSchema: { type: 'object', properties: {}, additionalProperties: false },
  },
  {
    name: 'hawkeye_capture_stop',
    description: 'Disable HawkEye browser traffic capture.',
    inputSchema: { type: 'object', properties: {}, additionalProperties: false },
  },
  {
    name: 'hawkeye_capture_state',
    description: 'Return HawkEye capture state and MCP-bound tab information.',
    inputSchema: { type: 'object', properties: {}, additionalProperties: false },
  },
  {
    name: 'hawkeye_capture_history',
    description: 'Read redacted HTTP/WebSocket record summaries captured by HawkEye. With no host, results stay scoped to the MCP-bound tab and its current host. Pass targetHost to search captured history across tabs for that host. Use hawkeye_request_get for selected details.',
    inputSchema: {
      type: 'object',
      properties: {
        targetHost: { type: 'string' }, search: { type: 'string' }, method: { type: 'string' },
        resourceType: { type: 'string' }, channelType: { type: 'string' },
        limit: { type: 'integer', minimum: 1, maximum: 200 },
      },
      additionalProperties: false,
    },
  },
  {
    name: 'hawkeye_capture_inspect',
    description: 'Inspect one HawkEye capture record for sensitive-data, dark-link, and flag evidence.',
    inputSchema: { type: 'object', properties: { recordId: { type: 'string' }, id: { type: 'string' }, url: { type: 'string' } }, additionalProperties: false },
  },
  {
    name: 'hawkeye_request_get',
    description: 'Read selected portions of one locally captured request/response, keeping large or sensitive bodies out of context unless explicitly requested.',
    inputSchema: { type: 'object', properties: { recordId: { type: 'string' }, id: { type: 'string' }, url: { type: 'string' }, parts: { type: 'array', items: { type: 'string', enum: ['summary', 'request_headers', 'request_body', 'response_headers', 'response_body', 'raw_request', 'raw_response'] } }, maxChars: { type: 'integer', minimum: 1000, maximum: 100000 } }, additionalProperties: false },
  },
  {
    name: 'hawkeye_request_replay',
    description: 'Send a direct HTTP request or replay a captured request inside the explicitly authorized HawkEye testing scope. For ordinary API calls provide url plus query/params, headers, json or form; rawRequest/recordId remain available for exact replays.',
    inputSchema: { type: 'object', properties: { recordId: { type: 'string' }, rawRequest: { type: 'string' }, url: { type: 'string' }, method: { type: 'string' }, headers: { type: 'object' }, query: { type: 'object', additionalProperties: true }, params: { type: 'object', additionalProperties: true }, body: { type: 'string' }, json: {}, form: { type: 'object', additionalProperties: true }, timeoutMs: { type: 'integer', minimum: 100, maximum: 60000 } }, additionalProperties: false },
  },
  {
    name: 'hawkeye_request_mutate',
    description: 'Generate bounded request variants without sending them. Use a captured record or raw request plus a parameter and up to eight values.',
    inputSchema: { type: 'object', properties: { recordId: { type: 'string' }, rawRequest: { type: 'string' }, url: { type: 'string' }, parameter: { type: 'string' }, variants: { type: 'array', minItems: 1, maxItems: 8, items: {} } }, required: ['parameter', 'variants'], additionalProperties: false },
  },
  {
    name: 'hawkeye_fuzz_run',
    description: 'Run a bounded security-test fuzz job (maximum 12 requests) only for a host explicitly authorized in HawkEye scope with fuzzing enabled.',
    inputSchema: { type: 'object', properties: { recordId: { type: 'string' }, rawRequest: { type: 'string' }, url: { type: 'string' }, parameter: { type: 'string' }, variants: { type: 'array', maxItems: 6, items: {} }, payloads: { type: 'array', maxItems: 12, items: { type: 'string' } }, executeLimit: { type: 'integer', minimum: 1, maximum: 12 }, stopOnFlags: { type: 'boolean' } }, additionalProperties: false },
  },
  {
    name: 'hawkeye_response_compare',
    description: 'Compare two captured or inline responses by status, headers, size, timing, and bounded text similarity.',
    inputSchema: { type: 'object', properties: { leftRecordId: { type: 'string' }, rightRecordId: { type: 'string' }, left: { type: 'object' }, right: { type: 'object' } }, additionalProperties: false },
  },
  {
    name: 'hawkeye_scope',
    description: 'Read or configure the explicit authorized-host scope and active-testing/fuzzing switches used by HawkEye security-test request tools.',
    inputSchema: { type: 'object', properties: { action: { type: 'string', enum: ['get', 'set', 'add', 'remove', 'clear'] }, host: { type: 'string' }, hosts: { type: 'array', maxItems: 100, items: { type: 'string' } }, includeSubdomains: { type: 'boolean' }, activeTestingEnabled: { type: 'boolean' }, fuzzingEnabled: { type: 'boolean' }, acknowledgeAuthorization: { type: 'boolean', description: 'Confirms written authorization for hosts being added.' }, confirm: { type: 'boolean' } }, additionalProperties: false },
  },
  {
    name: 'hawkeye_findings',
    description: 'Create, update, list, get, or remove locally stored security findings with evidence and remediation guidance.',
    inputSchema: { type: 'object', properties: { action: { type: 'string', enum: ['list', 'get', 'upsert', 'create', 'update', 'remove', 'clear'] }, id: { type: 'string' }, severity: { type: 'string' }, status: { type: 'string' }, host: { type: 'string' }, limit: { type: 'integer', minimum: 1, maximum: 200 }, finding: { type: 'object' }, confirm: { type: 'boolean' } }, additionalProperties: true },
  },
  {
    name: 'hawkeye_codec',
    description: "HawkEye's built-in encode/decode, hashing and crypto helpers. Runs a single transform locally in the extension (no network). Encoders: base64, base64url, url, hex, unicode, html, base32, rot13 (encode/decode). Digests: md5, sha1, sha256, sha512, sm3, hmacsha256 (needs key). Utilities: jwt_parse, timestamp_convert, json_pretty/compact/escape/unescape. Use action=auto_probe to auto-detect and decode suspicious base64/url tokens (e.g. flags) inside arbitrary text.",
    inputSchema: { type: 'object', properties: { action: { type: 'string', description: 'Transform to run, e.g. base64_decode, base64url_encode, url_encode, hex_decode, md5, sha256, sm3, hmacsha256, jwt_parse, timestamp_convert, json_pretty, auto_probe.' }, text: { type: 'string', description: 'The input string to transform.' }, key: { type: 'string', description: 'Secret key for keyed transforms such as hmacsha256.' } }, required: ['action'], additionalProperties: true },
  },
  {
    name: 'hawkeye_script_list',
    description: 'List the user script plugins installed in the HawkEye extension (Tampermonkey-style), including each script id, name, enabled state, match rules, and how well it matches a given page URL or host. Use this before hawkeye_script_run to pick the right script.',
    inputSchema: { type: 'object', properties: { url: { type: 'string', description: 'Optional page URL to rank scripts by match relevance.' }, host: { type: 'string', description: 'Optional host to rank scripts by match relevance.' } }, additionalProperties: false },
  },
  {
    name: 'hawkeye_script_run',
    description: 'Execute an installed HawkEye user script plugin on the bound page. Identify the script by scriptId, or by name/query for a best-effort match. The script runs in its configured world (page or isolated) exactly as a manual run would.',
    inputSchema: { type: 'object', properties: { scriptId: { type: 'string', description: 'The exact script id from hawkeye_script_list.' }, name: { type: 'string', description: 'Script name or search query when the id is unknown.' }, tabId: { type: 'integer', description: 'Optional tab id; defaults to the MCP-bound tab.' } }, additionalProperties: false },
  },
  {
    name: 'hawkeye_evaluate',
    description: "Run arbitrary JavaScript in the bound page MAIN world via chrome.debugger Runtime.evaluate (DevTools-level, not subject to page script-src or the MV3 extension CSP that blocks eval/new Function). returnByValue serializes the result; console and exceptions are captured. Do not switch to USER_SCRIPT to bypass CSP: that isolated world cannot see page variables and still uses eval. Failures are classified as csp_blocked, script_syntax_error, script_runtime_error, timeout, permission_denied, debugger_unavailable, or injection_no_result.",
    inputSchema: { type: 'object', properties: { code: { type: 'string', description: 'JavaScript to execute. Runtime completion values are captured for return, bare expressions, async/await, DOM nodes, arrays, objects, and functions.' }, world: { type: 'string', enum: ['MAIN', 'USER_SCRIPT', 'ISOLATED'], description: 'Execution world. MAIN (default) sees page variables/libraries; USER_SCRIPT is CSP-exempt but isolated; ISOLATED is the content-script world.' }, tabId: { type: 'integer', description: 'Optional tab id; defaults to the MCP-bound tab.' }, maxLength: { type: 'integer', minimum: 256, maximum: 1000000, description: 'Max characters of the serialized result/preview. Defaults to 500000.' }, timeoutMs: { type: 'integer', minimum: 250, maximum: 60000 } }, required: ['code'], additionalProperties: true },
  },
  {
    name: 'hawkeye_script_upsert',
    description: "Create or update a persistent HawkEye user script plugin (Tampermonkey-style). Provide the JavaScript in `code` (a full ==UserScript== metadata block is also accepted). Use this to author a reusable script that solves a page problem, then run it with hawkeye_script_run. Prefer hawkeye_evaluate for one-off computations; use this when the logic should persist and re-run on matching pages.",
    inputSchema: { type: 'object', properties: { code: { type: 'string', description: 'The script source. May include a Tampermonkey ==UserScript== header.' }, scriptId: { type: 'string', description: 'Existing script id to update; omit to create a new script.' }, name: { type: 'string' }, description: { type: 'string' }, matchMode: { type: 'string', enum: ['manual', 'auto'], description: 'manual = only runs when explicitly invoked; auto = runs on matching pages.' }, matches: { type: 'array', items: { type: 'string' }, description: 'URL match patterns for auto mode, e.g. https://example.com/*.' }, runAt: { type: 'string', enum: ['document_start', 'document_end', 'document_idle'] }, world: { type: 'string', enum: ['USER_SCRIPT', 'MAIN'] }, enabled: { type: 'boolean' } }, required: ['code'], additionalProperties: true },
  },
  {
    name: 'hawkeye_sensitive_scan',
    description: "Scan for exposed sensitive information (API keys, tokens, credentials, phone/ID numbers, private endpoints, etc.) using HawkEye's sensitive-info detector. Provide inline `html`/`text`/`responseBody` to scan arbitrary content, a `recordId` to scan one captured request/response, or a `targetHost` (optionally with `recordIds`) to batch-scan captured history. Read-only local analysis. Requires the sensitive detector to be enabled in the extension.",
    inputSchema: { type: 'object', properties: { text: { type: 'string' }, html: { type: 'string' }, responseBody: { type: 'string' }, requestBody: { type: 'string' }, recordId: { type: 'string', description: 'Captured record id to scan.' }, recordIds: { type: 'array', items: { type: 'string' } }, targetHost: { type: 'string', description: 'Host to batch-scan from captured history.' }, limit: { type: 'integer', minimum: 1, maximum: 200 } }, additionalProperties: true },
  },
  {
    name: 'hawkeye_darklink_scan',
    description: "Detect dark links / malicious or hidden links, black-hat SEO injection, hidden iframes and suspicious outbound targets on a page using HawkEye's threat analyzer. With no content it scans the live bound-tab DOM (use_dom). You can also pass inline `html`, a `recordId`, or a `targetHost`/`recordIds` to batch-scan captured history. scan_mode selects auto, worker_analyzer (offline HTML), or dom_injected. Read-only. Requires dark-link detection to be enabled in the extension.",
    inputSchema: { type: 'object', properties: { html: { type: 'string' }, url: { type: 'string' }, recordId: { type: 'string' }, recordIds: { type: 'array', items: { type: 'string' } }, targetHost: { type: 'string' }, use_dom: { type: 'boolean', description: 'Scan the live tab DOM (default true when no content is provided).' }, scan_mode: { type: 'string', enum: ['auto', 'worker_analyzer', 'dom_injected'], description: 'Force analyzer mode. auto picks DOM injection when a tab is available.' }, mode: { type: 'string', enum: ['auto', 'worker_analyzer', 'dom_injected'] }, batch: { type: 'boolean' }, limit: { type: 'integer', minimum: 1, maximum: 200 }, tabId: { type: 'integer' } }, additionalProperties: true },
  },
  {
    name: 'hawkeye_intercept',
    description: "Control HawkEye's request interceptor to pause, inspect, modify, forward or drop live traffic on the bound tab (like Burp intercept). Actions: enable (start pausing requests), status (enabled/queued count), queue (list paused requests with id/url/method/headers/body and stage=request|response), release (forward a paused request by id, optionally modifying it via payload{url,method,requestHeaders,requestBody,statusCode,responseHeaders,responseBody}), drop (abort a paused request by id), release_all (forward everything), disable (stop and release all). Typical loop: enable -> trigger the request in the page -> queue -> release/drop by id -> disable. Always disable when finished so the page is not left hanging.",
    inputSchema: { type: 'object', properties: { action: { type: 'string', enum: ['enable', 'disable', 'status', 'queue', 'release', 'drop', 'release_all'] }, id: { type: 'string', description: 'Paused record id from action=queue (for release/drop).' }, payload: { type: 'object', description: 'Optional modifications when releasing: url, method, requestHeaders, requestBody, statusCode, responseHeaders, responseBody.', additionalProperties: true }, limit: { type: 'integer', minimum: 1, maximum: 100 } }, required: ['action'], additionalProperties: true },
  },
];

TOOLS.find((tool) => tool.name === 'hawkeye_evaluate').inputSchema.properties.include_logs = { type: 'boolean', description: 'Include captured console logs. Defaults to false; execution errors are always preserved. Use browser_get_console_logs for page console inspection.' };
TOOLS.find((tool) => tool.name === 'browser_navigate').inputSchema.properties.include_snapshot = { type: 'boolean', description: 'Include the full post-navigation snapshot and elements. Defaults to a bounded preview with refs. Use browser_find or browser_snapshot for details without navigating again.' };
TOOLS.find((tool) => tool.name === 'hawkeye_evaluate').description += ' Console logs are omitted by default; include_logs=true opts in.';
TOOLS.find((tool) => tool.name === 'browser_navigate').description += ' Default output is a bounded page preview; include_snapshot=true restores the full observation.';

const ACTION_RECEIPT_TOOLS = new Set(['browser_click', 'browser_type', 'browser_fill_form', 'browser_select_option', 'browser_press_key', 'browser_wait', 'browser_wait_for']);
for (const tool of TOOLS) {
  if (!ACTION_RECEIPT_TOOLS.has(tool.name)) continue;
  tool.inputSchema.properties.include_elements = { type: 'boolean', description: 'Include the post-action page snapshot and element list. Defaults to false. Prefer browser_snapshot for a separate observation; never repeat an action just to fetch its page details.' };
  tool.description += ' Returns a concise action receipt by default, retaining outcome and input evidence. Set include_elements=true to include the post-action snapshot and elements.';
}

const READ_ONLY_TOOLS = new Set(['browser_snapshot', 'browser_find', 'browser_search', 'browser_fetch', 'browser_research', 'browser_exam_questions', 'browser_read_text', 'browser_get_console_logs', 'browser_wait', 'browser_wait_for', 'hawkeye_capture_state', 'hawkeye_capture_history', 'hawkeye_capture_inspect', 'hawkeye_request_get', 'hawkeye_request_mutate', 'hawkeye_response_compare', 'hawkeye_codec', 'hawkeye_script_list', 'hawkeye_sensitive_scan', 'hawkeye_darklink_scan']);
const CLOSED_WORLD_TOOLS = new Set(['hawkeye_capture_start', 'hawkeye_capture_stop', 'hawkeye_capture_state', 'hawkeye_capture_history', 'hawkeye_capture_inspect', 'hawkeye_request_get', 'hawkeye_request_mutate', 'hawkeye_response_compare', 'hawkeye_scope', 'hawkeye_findings', 'hawkeye_codec', 'hawkeye_script_list', 'hawkeye_sensitive_scan']);
const TOOL_BY_NAME = new Map(TOOLS.map((tool) => [tool.name, tool]));
for (const tool of TOOLS) {
  const readOnly = READ_ONLY_TOOLS.has(tool.name);
  // Tool-level hints must cover every action, including optional file writes and page scripts.
  tool.annotations = { title: tool.name.replace(/_/g, ' '), readOnlyHint: readOnly, destructiveHint: !readOnly, idempotentHint: readOnly, openWorldHint: !CLOSED_WORLD_TOOLS.has(tool.name) };
}

// This is the complete JSON Schema vocabulary used by the shipped tool contracts.
// Reject unsupported keywords at startup so future schemas cannot silently skip checks.
const SCHEMA_KEYWORDS = new Set(['type', 'properties', 'additionalProperties', 'required', 'enum', 'const', 'minimum', 'maximum', 'exclusiveMinimum', 'exclusiveMaximum', 'multipleOf', 'minLength', 'maxLength', 'pattern', 'items', 'prefixItems', 'minItems', 'maxItems', 'uniqueItems', 'minProperties', 'maxProperties', 'oneOf', 'anyOf', 'allOf', 'not', 'if', 'then', 'else', 'description', 'title', 'default', 'examples', 'deprecated', '$schema']);
function checkSchemaContract(schema) {
  if (typeof schema === 'boolean') return;
  for (const key of Object.keys(schema)) if (!SCHEMA_KEYWORDS.has(key)) throw new Error('Unsupported tool schema keyword: ' + key);
  for (const child of Object.values(schema.properties || {})) checkSchemaContract(child);
  for (const key of ['items', 'additionalProperties', 'not', 'if', 'then', 'else']) if (schema[key] && typeof schema[key] === 'object') checkSchemaContract(schema[key]);
  for (const key of ['oneOf', 'anyOf', 'allOf', 'prefixItems']) for (const child of schema[key] || []) checkSchemaContract(child);
}
function stableJson(value) {
  if (Array.isArray(value)) return '[' + value.map(stableJson).join(',') + ']';
  if (value && typeof value === 'object') return '{' + Object.keys(value).sort().map((key) => JSON.stringify(key) + ':' + stableJson(value[key])).join(',') + '}';
  return JSON.stringify(value);
}
function validateSchema(schema, value, location = '$', depth = 0) {
  if (depth > 64) return [{ path: location, keyword: 'depth', message: 'Nesting exceeds 64 levels' }];
  if (schema === true) return [];
  if (schema === false) return [{ path: location, keyword: 'false', message: 'Value is not allowed' }];
  const errors = [];
  const fail = (keyword, message) => { if (errors.length < 16) errors.push({ path: location, keyword, message }); };
  const matches = (child, candidate = value, at = location) => validateSchema(child, candidate, at, depth + 1);
  const object = value !== null && typeof value === 'object' && !Array.isArray(value);
  const typeMatches = (type) => type === 'null' ? value === null : type === 'array' ? Array.isArray(value) : type === 'object' ? object : type === 'integer' ? Number.isInteger(value) : type === 'number' ? typeof value === 'number' && Number.isFinite(value) : typeof value === type;
  if (schema.type && !(Array.isArray(schema.type) ? schema.type : [schema.type]).some(typeMatches)) { fail('type', 'Expected ' + JSON.stringify(schema.type)); return errors; }
  if (schema.enum && !schema.enum.some((item) => stableJson(item) === stableJson(value))) fail('enum', 'Expected one of ' + JSON.stringify(schema.enum));
  if (Object.hasOwn(schema, 'const') && stableJson(schema.const) !== stableJson(value)) fail('const', 'Expected ' + JSON.stringify(schema.const));
  for (const key of ['allOf', 'anyOf', 'oneOf']) if (schema[key]) {
    const branches = schema[key].map((child) => matches(child));
    const count = branches.filter((branch) => !branch.length).length;
    if ((key === 'allOf' && count !== branches.length) || (key === 'anyOf' && count === 0) || (key === 'oneOf' && count !== 1)) {
      fail(key, key === 'oneOf' ? 'Must match exactly one allowed alternative' : 'Does not match ' + key);
      if (count === 0) errors.push(...branches[0].slice(0, 3));
    }
  }
  if (schema.not && !matches(schema.not).length) fail('not', 'This parameter combination is not allowed');
  if (Object.hasOwn(schema, 'if')) {
    const ifValid = !matches(schema.if).length;
    errors.push(...matches(ifValid ? (Object.hasOwn(schema, 'then') ? schema.then : true) : (Object.hasOwn(schema, 'else') ? schema.else : true)));
  }
  if (typeof value === 'number') {
    for (const [key, bad] of [['minimum', value < schema.minimum], ['maximum', value > schema.maximum], ['exclusiveMinimum', value <= schema.exclusiveMinimum], ['exclusiveMaximum', value >= schema.exclusiveMaximum]]) if (bad) fail(key, 'Must satisfy ' + key + ' ' + schema[key]);
    if (schema.multipleOf && Math.abs(value / schema.multipleOf - Math.round(value / schema.multipleOf)) > 1e-10) fail('multipleOf', 'Must be a multiple of ' + schema.multipleOf);
  }
  if (typeof value === 'string') {
    const length = Array.from(value).length;
    if (length < schema.minLength) fail('minLength', 'Must have at least ' + schema.minLength + ' characters');
    if (length > schema.maxLength) fail('maxLength', 'Must have at most ' + schema.maxLength + ' characters');
    if (schema.pattern && !new RegExp(schema.pattern, 'u').test(value)) fail('pattern', 'Must match ' + schema.pattern);
  }
  if (Array.isArray(value)) {
    if (value.length < schema.minItems) fail('minItems', 'Must contain at least ' + schema.minItems + ' items');
    if (value.length > schema.maxItems) fail('maxItems', 'Must contain at most ' + schema.maxItems + ' items');
    if (schema.uniqueItems && new Set(value.map((item) => stableJson(item))).size !== value.length) fail('uniqueItems', 'Items must be unique');
    for (let i = 0; i < value.length && errors.length < 16; i += 1) {
      const child = schema.prefixItems?.[i] ?? schema.items;
      if (child !== undefined) errors.push(...matches(child, value[i], location + '[' + i + ']'));
    }
  }
  if (object) {
    const keys = Object.keys(value);
    if (keys.length < schema.minProperties) fail('minProperties', 'Too few properties');
    if (keys.length > schema.maxProperties) fail('maxProperties', 'Too many properties');
    for (const key of schema.required || []) if (!Object.hasOwn(value, key)) fail('required', 'Missing required property: ' + key);
    for (const key of keys) {
      if (errors.length >= 16) break;
      if (Object.hasOwn(schema.properties || {}, key)) errors.push(...matches(schema.properties[key], value[key], location + '.' + key));
      else if (schema.additionalProperties === false) errors.push({ path: location + '.' + key, keyword: 'additionalProperties', message: 'Unknown parameter; remove it or use a documented parameter' });
      else if (schema.additionalProperties && typeof schema.additionalProperties === 'object') errors.push(...matches(schema.additionalProperties, value[key], location + '.' + key));
    }
  }
  return errors.slice(0, 16);
}

const TOOL_CAPABILITIES = {
  core: ['browser_navigate', 'browser_go_back', 'browser_go_forward', 'browser_snapshot', 'browser_find', 'browser_click', 'browser_hover', 'browser_type', 'browser_select_option', 'browser_press_key', 'browser_wait', 'browser_tabs', 'browser_reload', 'browser_wait_for', 'browser_fill_form', 'browser_handle_dialog', 'browser_scroll', 'browser_read_text'],
  research: ['browser_search', 'browser_fetch', 'browser_research'],
  vision: ['browser_screenshot', 'browser_captcha_assist', 'browser_resize', 'browser_drag'],
  files: ['browser_file_upload', 'browser_download_file'],
  forms: ['browser_exam_questions', 'browser_answer_questions'],
  devtools: ['browser_get_console_logs', 'browser_security', 'hawkeye_evaluate', 'hawkeye_script_list', 'hawkeye_script_run', 'hawkeye_script_upsert'],
  security: ['hawkeye_capture_start', 'hawkeye_capture_stop', 'hawkeye_capture_state', 'hawkeye_capture_history', 'hawkeye_capture_inspect', 'hawkeye_request_get', 'hawkeye_request_replay', 'hawkeye_request_mutate', 'hawkeye_fuzz_run', 'hawkeye_response_compare', 'hawkeye_scope', 'hawkeye_findings', 'hawkeye_codec', 'hawkeye_sensitive_scan', 'hawkeye_darklink_scan', 'hawkeye_intercept'],
};
function resolveToolProfile(argv = process.argv.slice(2), env = process.env) {
  const option = (key, fallback) => { const at = argv.indexOf(key); if (at < 0) return fallback; if (!argv[at + 1] || argv[at + 1].startsWith('--')) throw new Error('Missing value for ' + key); return argv[at + 1]; };
  const profile = option('--profile', env.HX0_MCP_PROFILE || 'full');
  const profiles = { full: Object.keys(TOOL_CAPABILITIES), core: ['core'], research: ['core', 'research'], security: ['core', 'security', 'devtools'] };
  if (!Object.hasOwn(profiles, profile)) throw new Error('Unknown MCP profile: ' + profile);
  const capabilities = new Set([...profiles[profile], ...String(option('--caps', env.HX0_MCP_CAPS || '')).split(',').map((part) => part.trim()).filter(Boolean)]);
  for (const capability of capabilities) if (!Object.hasOwn(TOOL_CAPABILITIES, capability)) throw new Error('Unknown MCP capability: ' + capability);
  const enabled = new Set([...capabilities].flatMap((key) => TOOL_CAPABILITIES[key]));
  return { profile, capabilities: [...capabilities], tools: TOOLS.filter((tool) => enabled.has(tool.name)) };
}
for (const tool of TOOLS) {
  const schema = tool.inputSchema;
  const requirements = {};
  for (const key of ['required', 'anyOf', 'oneOf', 'allOf']) if (schema[key]) { requirements[key] = schema[key]; delete schema[key]; }
  if (tool.name === 'browser_find') requirements.oneOf = [{ required: ['text'] }, { required: ['regex'] }];
  if (tool.name === 'browser_search') requirements.anyOf = [{ required: ['query'] }, { required: ['queries'] }];
  schema.properties.page_token = { type: 'string', minLength: 32, maxLength: 64, pattern: '^[A-Za-z0-9_-]+$', description: 'Opaque continuation token from context.next_page_token. Send only this parameter to read the same cached result; never rerun an action to get the next page. Valid for two minutes in this MCP session.' };
  if (schema.properties.cursor) schema.properties.cursor = { type: 'integer', minimum: 0, maximum: 0, description: 'Deprecated. Only 0 is accepted for a fresh result; use page_token for stable continuation.' };
  schema.anyOf = [{ ...requirements, not: { required: ['page_token'] } }, { required: ['page_token'] }];
  checkSchemaContract(schema);
}
const TOOL_PROFILE = resolveToolProfile();
const ENABLED_TOOL_NAMES = new Set(TOOL_PROFILE.tools.map((tool) => tool.name));

// Model providers accept a narrower vocabulary than MCP's JSON Schema contract.
// Keep the executable schemas above intact; publish a simple object at the root.
// Root fields must be optional because page_token alone is a valid continuation.
// Build once, rather than cloning 51 schemas on each tools/list request.
function publicToolDefinition(tool) {
  const schema = JSON.parse(JSON.stringify(tool.inputSchema));
  const requirements = schema.anyOf[0];
  for (const key of ['required', 'anyOf', 'oneOf', 'allOf', 'not', 'if', 'then', 'else']) delete schema[key];
  for (const key of requirements.required || []) {
    const property = schema.properties[key];
    property.description = 'Required for a fresh call; omit when using page_token. ' + (property.description || '');
  }
  const alternatives = tool.name === 'browser_find' ? 'Fresh calls require exactly one of text or regex.'
    : tool.name === 'browser_search' ? 'Fresh calls require query or queries.' : '';
  const required = requirements.required?.length ? 'Fresh calls require: ' + requirements.required.join(', ') + '.' : '';
  return { ...tool, description: [tool.description, required, alternatives].filter(Boolean).join(' '), inputSchema: schema };
}
const PUBLIC_TOOLS = TOOL_PROFILE.tools.map(publicToolDefinition);
const LEGACY_PUBLIC_TOOLS = PUBLIC_TOOLS.map(({ annotations, ...tool }) => tool);
function compatibleToolResult(content, context) {
  if (context.protocolVersion >= '2025-06-18') return content;
  const { structuredContent, ...legacy } = content;
  return legacy;
}


const SNAPSHOT_TTL_MS = 120_000;
const MAX_SNAPSHOT_BYTES = 4 * 1024 * 1024;
const MAX_SNAPSHOT_CACHE_BYTES = 32 * 1024 * 1024;
const snapshots = new Map();
const snapshotTokens = new Map();
let snapshotCacheBytes = 0;
function removeSnapshot(id) {
  const entry = snapshots.get(id);
  if (!entry) return;
  snapshotCacheBytes -= entry.bytes;
  for (const token of entry.tokens.values()) snapshotTokens.delete(token);
  snapshots.delete(id);
}
function pruneSnapshots(now = Date.now(), hostId) {
  for (const [id, entry] of snapshots) if (entry.expiresAt <= now || entry.hostId === hostId) removeSnapshot(id);
}
function pageError(message, category = 'invalid_page_token') {
  return Object.assign(new Error(message), { rpcCode: -32602, category, hint: 'Start a fresh read without cursor/page_token. Never retry a write merely to obtain its old output.' });
}
function cacheResultPage(tool, text, metadata, input, context, defaultBudget) {
  const budget = Math.max(12_000, Math.min(400_000, Number(input.context_budget_chars) || defaultBudget));
  const entry = { id: crypto.randomBytes(16).toString('hex'), hostId: context.hostId, tool, text, metadata, input, budget, capturedAt: Date.now(), expiresAt: Date.now() + SNAPSHOT_TTL_MS, tokens: new Map() };
  entry.bytes = Buffer.byteLength(text) + Buffer.byteLength(JSON.stringify(metadata)) + Buffer.byteLength(JSON.stringify(input));
  if (text.length > budget) {
    if (entry.bytes > MAX_SNAPSHOT_BYTES) throw pageError('Result exceeds the 4 MiB immutable-page limit; narrow the query or reduce extraction size', 'result_too_large');
    pruneSnapshots();
    const sameHost = [...snapshots.values()].filter((row) => row.hostId === context.hostId);
    while (sameHost.length >= 8) removeSnapshot(sameHost.shift().id);
    while (snapshots.size >= 64 || snapshotCacheBytes + entry.bytes > MAX_SNAPSHOT_CACHE_BYTES) removeSnapshot(snapshots.keys().next().value);
    snapshots.set(entry.id, entry); snapshotCacheBytes += entry.bytes;
  }
  return renderResultPage(entry, 0);
}
function renderResultPage(entry, offset) {
  const end = Math.min(entry.text.length, offset + entry.budget);
  let token = null;
  if (end < entry.text.length) {
    token = entry.tokens.get(end);
    if (!token) { token = crypto.randomBytes(24).toString('base64url'); entry.tokens.set(end, token); snapshotTokens.set(token, { id: entry.id, offset: end }); }
  }
  const context = { snapshot_id: entry.id, captured_at: entry.capturedAt, expires_at: entry.expiresAt, cursor: offset, next_cursor: null, next_page_token: token, total_chars: entry.text.length, budget_chars: entry.budget, complete: token === null, immutable: true };
  if (entry.tool === 'browser_read_text') {
    const structuredContent = { ...entry.metadata, value: entry.text.slice(offset, end), context };
    // Keep structured-only and text-only MCP hosts consistent, including continuation pages.
    return { content: [{ type: 'text', text: JSON.stringify(structuredContent) }], structuredContent };
  }
  return { content: [{ type: 'text', text: entry.text.slice(offset, end) }, { type: 'text', text: JSON.stringify({ ...entry.metadata, context }) }], structuredContent: { ...entry.metadata, context } };
}
function continueResultPage(tool, input, context) {
  pruneSnapshots();
  const cursor = snapshotTokens.get(input.page_token);
  const entry = cursor && snapshots.get(cursor.id);
  if (!entry || entry.hostId !== context.hostId || entry.tool !== tool) throw pageError('Unknown, expired, evicted, or wrong-session page_token');
  const provided = { ...input }; delete provided.page_token;
  if (Object.hasOwn(provided, 'cursor')) throw pageError('Do not combine cursor and page_token');
  if (stableJson({ ...entry.input, ...provided }) !== stableJson(entry.input)) throw pageError('Continuation parameters differ from the captured result; send page_token alone');
  const validation = validateSchema(TOOL_BY_NAME.get(tool).inputSchema, entry.input);
  if (validation.length) throw pageError('Captured tool input no longer matches the active schema');
  return renderResultPage(entry, cursor.offset);
}

function encodeFrame(text, opcode = 0x1) {
  const payload = Buffer.from(String(text), 'utf8');
  let head;
  if (payload.length < 126) {
    head = Buffer.from([0x80 | opcode, payload.length]);
  } else if (payload.length <= 0xffff) {
    head = Buffer.alloc(4);
    head[0] = 0x80 | opcode;
    head[1] = 126;
    head.writeUInt16BE(payload.length, 2);
  } else {
    head = Buffer.alloc(10);
    head[0] = 0x80 | opcode;
    head[1] = 127;
    head.writeBigUInt64BE(BigInt(payload.length), 2);
  }
  return Buffer.concat([head, payload]);
}

class BrowserSocket {
  constructor(socket, onMessage, onClose) {
    this.socket = socket;
    this.onMessage = onMessage;
    this.onClose = onClose;
    this.buffer = Buffer.alloc(0);
    this.fragments = [];
    this.fragmentOpcode = 0;
    this.fragmentBytes = 0;
    this.lastSeenAt = Date.now();
    this.closed = false;
    socket.on('data', (chunk) => this.feed(chunk));
    socket.on('close', () => this.finish());
    socket.on('end', () => this.finish());
    socket.on('error', () => this.finish());
  }

  sendJson(value) {
    if (this.closed || !this.socket.writable) return false;
    try {
      const frame = encodeFrame(JSON.stringify(value));
      if (frame.length > MAX_WS_MESSAGE_BYTES || this.socket.writableLength + frame.length > MAX_WS_MESSAGE_BYTES) return false;
      this.socket.write(frame);
      return true;
    } catch {
      return false;
    }
  }

  close() {
    if (this.closed) return;
    try { this.socket.write(encodeFrame('', 0x8)); } catch {}
    try { this.socket.end(); } catch {}
    this.finish();
  }

  finish() {
    if (this.closed) return;
    this.closed = true;
    this.buffer = Buffer.alloc(0);
    this.fragments = [];
    this.fragmentBytes = 0;
    try { this.socket.destroy(); } catch {}
    try { this.onClose(this); } catch {}
  }

  feed(chunk) {
    if (this.closed) return;
    this.lastSeenAt = Date.now();
    if (this.buffer.length + chunk.length > MAX_WS_MESSAGE_BYTES + 14) { this.close(); return; }
    this.buffer = this.buffer.length ? Buffer.concat([this.buffer, chunk]) : chunk;
    while (this.buffer.length >= 2) {
      const first = this.buffer[0];
      const second = this.buffer[1];
      const fin = !!(first & 0x80);
      const opcode = first & 0x0f;
      const masked = !!(second & 0x80);
      const control = opcode >= 0x8;
      if (!masked || (first & 0x70) || ![0, 1, 2, 8, 9, 10].includes(opcode) || (control && !fin)) { this.close(); return; }
      let length = second & 0x7f;
      let offset = 2;
      if (length === 126) {
        if (this.buffer.length < 4) return;
        length = this.buffer.readUInt16BE(2);
        offset = 4;
      } else if (length === 127) {
        if (this.buffer.length < 10) return;
        const large = this.buffer.readBigUInt64BE(2);
        if (large > BigInt(MAX_WS_MESSAGE_BYTES)) { this.close(); return; }
        length = Number(large);
        offset = 10;
      }
      if ((control && length > 125) || length > MAX_WS_MESSAGE_BYTES || (!control && this.fragmentBytes + length > MAX_WS_MESSAGE_BYTES)) { this.close(); return; }
      const maskOffset = masked ? 4 : 0;
      if (this.buffer.length < offset + maskOffset + length) return;
      let payload = this.buffer.subarray(offset + maskOffset, offset + maskOffset + length);
      if (masked) {
        const mask = this.buffer.subarray(offset, offset + 4);
        const decoded = Buffer.alloc(length);
        for (let i = 0; i < length; i += 1) decoded[i] = payload[i] ^ mask[i % 4];
        payload = decoded;
      }
      this.buffer = this.buffer.subarray(offset + maskOffset + length);
      if (opcode === 0x8) { this.close(); return; }
      if (opcode === 0x9) {
        try { this.socket.write(Buffer.concat([Buffer.from([0x8a, payload.length]), payload])); } catch {}
        continue;
      }
      if (opcode === 0xa) continue;
      if (opcode === 0x1 || opcode === 0x2) {
        if (this.fragments.length) { this.close(); return; }
        this.fragments = [payload];
        this.fragmentBytes = payload.length;
        this.fragmentOpcode = opcode;
      } else if (opcode === 0x0 && this.fragments.length) {
        if (this.fragments.length >= MAX_WS_FRAGMENTS) { this.close(); return; }
        this.fragments.push(payload);
        this.fragmentBytes += payload.length;
      } else {
        this.close(); return;
      }
      if (!fin) continue;
      const complete = Buffer.concat(this.fragments);
      const messageOpcode = this.fragmentOpcode;
      this.fragments = [];
      this.fragmentOpcode = 0;
      this.fragmentBytes = 0;
      if (messageOpcode !== 0x1) continue;
      try { this.onMessage(complete.toString('utf8'), this); } catch {}
    }
  }
}

const pendingCalls = new Map();
const requestBrowsers = new WeakMap();
let activeToolCalls = 0;
let extensionSocket = null;
let extensionReady = false;
let extensionInfo = null;
let callSequence = 0;
const extensionClients = new Set();
const preferredBrowser = String(process.env.HX0_MCP_BROWSER || '').trim().toLowerCase();
if (preferredBrowser && !['chrome', 'firefox'].includes(preferredBrowser)) throw new Error('HX0_MCP_BROWSER must be chrome or firefox');
function selectExtension() {
  if (extensionSocket && !extensionSocket.closed && extensionSocket.info) return;
  extensionSocket = [...extensionClients].find((client) => !client.closed && client.info && (!preferredBrowser || client.info.browser.toLowerCase().includes(preferredBrowser))) || null;
  extensionReady = !!extensionSocket;
  extensionInfo = extensionSocket?.info || null;
}

function rejectPending(reason, client) {
  for (const row of [...pendingCalls.values()]) if (!client || row.client === client) row.reject(new Error(reason));
}

function handleExtensionMessage(raw, client) {
  if (!extensionClients.has(client) || client.closed) return;
  let message;
  try { message = JSON.parse(raw); } catch { return; }
  if (!message || typeof message !== 'object') return;
  if (message.type === 'hx0_mcp_hello') {
    if (message.protocol !== 'hx0-mcp-v1') { client.close(); return; }
    client.info = {
      version: String(message.extensionVersion || ''),
      browser: String(message.browser || ''),
      tabId: message.tabId == null ? null : Number(message.tabId),
    };
    selectExtension();
    client.sendJson({ type: 'hx0_mcp_ready', protocol: 'hx0-mcp-v1', serverVersion: SERVER_VERSION });
    stderr('HawkEye extension connected' + ' (' + client.info.browser + (client === extensionSocket ? ', active)' : ', standby)'));
    return;
  }
  if (message.type === 'hx0_mcp_pong') return;
  if (message.type === 'hx0_mcp_tool_result' && message.id) {
    const row = pendingCalls.get(String(message.id));
    if (!row || row.client !== client) return;
    // Tool failures are data: preserve hints, categories and recovery metadata.
    if (message.ok === false) row.resolve({ ...(message.result && typeof message.result === 'object' ? message.result : {}), ok: false, error: String(message.error || message.result?.error || 'HawkEye tool failed') });
    else row.resolve(message.result);
  }
}

function hx0ToolTimeoutMs(tool) {
  if (tool === 'browser_research') return 190_000;
  if (tool === 'browser_search' || tool === 'browser_fetch' || tool === 'browser_captcha_assist' || tool === 'hawkeye_fuzz_run') return 120_000;
  if (
    tool === 'browser_snapshot' || tool === 'browser_navigate' || tool === 'browser_reload' ||
    tool === 'browser_read_text' || tool === 'browser_find' || tool === 'browser_screenshot' ||
    tool === 'browser_go_back' || tool === 'browser_go_forward' || tool === 'browser_wait_for' ||
    tool === 'browser_scroll'
  ) return 90_000;
  return TOOL_TIMEOUT_MS;
}

function slimCaptchaAssistResult(result) {
  const row = result && typeof result === 'object' ? result : {};
  const slimCandidate = (item) => {
    const cand = item && typeof item === 'object' ? item : {};
    return {
      type: cand.type || '',
      score: cand.score,
      provider: cand.provider || '',
      expression: cand.expression || '',
      answer: cand.answer || '',
      containerRef: cand.containerRef || '',
      imageRef: cand.imageRef || '',
      inputRef: cand.inputRef || '',
      handleRef: cand.handleRef || '',
      trackRef: cand.trackRef || '',
      text: String(cand.text || '').slice(0, 280),
    };
  };
  const candidates = Array.isArray(row.candidates) ? row.candidates.slice(0, 4).map(slimCandidate) : [];
  return {
    ok: row.ok !== false,
    value: row.value == null ? null : row.value,
    error: row.error || null,
    hint: row.hint || null,
    action: row.action,
    tabId: row.tabId,
    url: row.url,
    detected: row.detected === true,
    manual_required: !!row.manual_required,
    suggestedOffsetRatio: row.suggestedOffsetRatio,
    expectedLength: row.expectedLength,
    recognitionHint: row.recognitionHint,
    clickRefreshes: row.clickRefreshes,
    scope: row.scope,
    width: row.width,
    height: row.height,
    imageSource: row.imageSource,
    challenge: slimCandidate(row.challenge),
    candidates,
    count: Number(row.count != null ? row.count : candidates.length) || candidates.length,
  };
}

function normalizeBrowserInput(tool, input, browser) {
  const result = input && typeof input === 'object' ? { ...input } : {};
  // Chrome uses CDP for its trusted backend; Firefox also accepts the native alias.
  if (String(browser || '').toLowerCase() === 'chrome') {
    if (tool === 'browser_click' && result.clickMode === 'native') result.clickMode = 'trusted';
    if (tool === 'browser_press_key' && result.inputMode === 'native') result.inputMode = 'trusted';
  }
  return result;
}

function callExtension(tool, input, { signal, hostId } = {}) {
  if (signal?.aborted) return Promise.reject(signal.reason || new Error('MCP request cancelled'));
  if (!extensionSocket || extensionSocket.closed || !extensionReady) {
    return Promise.reject(new Error('HawkEye extension is not connected. Enable “HawkEye Browser Automation MCP” in the extension popup.'));
  }
  if (pendingCalls.size >= MAX_PENDING_CALLS) return Promise.reject(new Error('MCP bridge is busy; retry after outstanding calls finish'));
  const client = (signal && requestBrowsers.get(signal)) || extensionSocket;
  if (client.closed || !extensionClients.has(client)) return Promise.reject(new Error('The browser for this request disconnected; retry after taking a new snapshot'));
  if (signal) requestBrowsers.set(signal, client);
  const id = 'mcp_' + Date.now().toString(36) + '_' + (++callSequence).toString(36);
  return new Promise((resolve, reject) => {
    const timeoutMs = hx0ToolTimeoutMs(tool);
    const finish = (callback, value) => {
      if (!pendingCalls.has(id)) return;
      pendingCalls.delete(id);
      clearTimeout(timer);
      signal?.removeEventListener('abort', onAbort);
      callback(value);
    };
    const onAbort = () => {
      client.sendJson({ type: 'hx0_mcp_cancel', id });
      finish(reject, signal.reason || new Error('MCP request cancelled'));
    };
    const timer = setTimeout(() => {
      client.sendJson({ type: 'hx0_mcp_cancel', id });
      finish(reject, new Error('HawkEye extension did not respond within ' + Math.round(timeoutMs / 1000) + ' seconds'));
    }, timeoutMs);
    pendingCalls.set(id, { client, resolve: (value) => finish(resolve, value), reject: (error) => finish(reject, error) });
    signal?.addEventListener('abort', onAbort, { once: true });
    const sent = client.sendJson({ type: 'hx0_mcp_tool_call', id, tool, hostId, input: normalizeBrowserInput(tool, input, client.info?.browser), timeoutMs });
    if (!sent) finish(reject, new Error('Failed to send command to HawkEye extension: transport unavailable or buffer full'));
  });
}

function mimeTypeForFile(filePath) {
  const ext = path.extname(filePath).toLowerCase();
  return ({ '.txt': 'text/plain', '.json': 'application/json', '.html': 'text/html', '.htm': 'text/html', '.csv': 'text/csv', '.xml': 'application/xml', '.pdf': 'application/pdf', '.png': 'image/png', '.jpg': 'image/jpeg', '.jpeg': 'image/jpeg', '.gif': 'image/gif', '.webp': 'image/webp', '.zip': 'application/zip' })[ext] || 'application/octet-stream';
}

const SCREENSHOT_ILLEGAL_FILE_CHARS = /[\u0000-\u001f<>:"|?*]/g;
const SCREENSHOT_WINDOWS_RESERVED_NAME = /^(con|prn|aux|nul|com[1-9]|lpt[1-9])(\..*)?$/i;

function sanitizeScreenshotPathPart(value, fallback = 'capture') {
  let safe = String(value == null ? '' : value)
    .replace(SCREENSHOT_ILLEGAL_FILE_CHARS, '_')
    .replace(/[\/\\]/g, '_')
    .replace(/\s+/g, '_')
    .replace(/_+/g, '_')
    .replace(/^\.+|\.+$/g, '')
    .slice(0, 96);
  if (!safe || safe === '.' || safe === '..' || SCREENSHOT_WINDOWS_RESERVED_NAME.test(safe)) safe = fallback;
  return safe;
}

function screenshotFileExtension(mimeType, requestedType) {
  const mime = String(mimeType || '').toLowerCase();
  const type = String(requestedType || '').toLowerCase();
  return mime === 'image/jpeg' || type === 'jpeg' || type === 'jpg' ? '.jpg' : '.png';
}

function screenshotFileTimestamp(date = new Date()) {
  const two = (value) => String(value).padStart(2, '0');
  const three = (value) => String(value).padStart(3, '0');
  return String(date.getFullYear()) + two(date.getMonth() + 1) + two(date.getDate()) + '_'
    + two(date.getHours()) + two(date.getMinutes()) + two(date.getSeconds()) + '_' + three(date.getMilliseconds());
}

function withScreenshotImageExtension(filePath, extension) {
  const parsed = path.parse(filePath);
  const current = parsed.ext.toLowerCase();
  if (!current) return filePath + extension;
  if (current === extension || (extension === '.jpg' && current === '.jpeg')) return filePath;
  return path.join(parsed.dir, parsed.name + extension);
}

function relativeScreenshotFilePath(rawPath) {
  const parts = String(rawPath || '').replace(/\\/g, '/').split('/').filter((part) => part && part !== '.')
    .map((part) => part === '..' ? '_parent_' : sanitizeScreenshotPathPart(part, 'capture'));
  return parts.length ? path.join(...parts) : '';
}

function defaultScreenshotDirectory() {
  const configured = String(process.env.HAWKEYE_SCREENSHOT_DIR || '').trim();
  return path.resolve(configured || path.join(process.cwd(), 'screenshots'));
}

function resolveScreenshotFilePath(requestInput = {}, result = {}, mimeType = 'image/png', now = new Date()) {
  const extension = screenshotFileExtension(mimeType, requestInput.type);
  const requestedPath = String(requestInput.file_path || '').trim();
  let targetPath;
  if (requestedPath && path.isAbsolute(requestedPath)) {
    const resolved = path.resolve(requestedPath);
    targetPath = path.join(path.dirname(resolved), sanitizeScreenshotPathPart(path.basename(resolved), 'screenshot' + extension));
  } else {
    const root = defaultScreenshotDirectory();
    const relative = relativeScreenshotFilePath(requestedPath);
    if (relative) {
      targetPath = path.resolve(root, relative);
      const boundary = path.relative(root, targetPath);
      if (boundary.startsWith('..' + path.sep) || boundary === '..' || path.isAbsolute(boundary)) {
        throw new Error('relative screenshot path escapes the configured screenshot directory');
      }
    } else {
      const label = sanitizeScreenshotPathPart(result.scope || requestInput.element || requestInput.ref || requestInput.selector || requestInput.target || 'viewport', 'viewport');
      targetPath = path.join(root, 'screenshot_' + screenshotFileTimestamp(now) + '_' + label + extension);
    }
  }
  return path.resolve(withScreenshotImageExtension(targetPath, extension));
}

async function saveScreenshotImage({ base64, mimeType, requestInput = {}, result = {} } = {}) {
  let targetPath = null;
  try {
    targetPath = resolveScreenshotFilePath(requestInput, result, mimeType);
    const imageBuffer = Buffer.from(String(base64 || ''), 'base64');
    if (!imageBuffer.length) throw new Error('decoded screenshot is empty');
    await fs.mkdir(path.dirname(targetPath), { recursive: true });
    await fs.writeFile(targetPath, imageBuffer, { flag: requestInput.overwrite === true ? 'w' : 'wx' });
    return { saved_to: targetPath, file_path: targetPath, file_bytes: imageBuffer.length, save_error: null };
  } catch (error) {
    return { saved_to: null, file_path: targetPath, file_bytes: 0, save_error: String(error && error.message || error || 'unknown file save error') };
  }
}

async function prepareToolInput(tool, rawInput) {
  const input = rawInput && typeof rawInput === 'object' ? { ...rawInput } : {};
  if (tool !== 'browser_file_upload') return input;
  const filePaths = (Array.isArray(input.paths) ? input.paths : [input.path]).map((item) => String(item || '').trim()).filter(Boolean).slice(0, 10);
  if (!filePaths.length) throw new Error('browser_file_upload requires path or paths');
  const files = [];
  let totalBytes = 0;
  for (const filePath of filePaths) {
    const stat = await fs.stat(filePath);
    if (!stat.isFile()) throw new Error('Upload path is not a regular file: ' + filePath);
    if (stat.size > 5 * 1024 * 1024) throw new Error('Each upload file must be 5 MB or smaller: ' + filePath);
    totalBytes += stat.size;
    if (totalBytes > 10 * 1024 * 1024) throw new Error('Combined upload size must be 10 MB or smaller');
    const data = await fs.readFile(filePath);
    files.push({ name: path.basename(filePath), mimeType: mimeTypeForFile(filePath), lastModified: Math.round(stat.mtimeMs), base64: data.toString('base64') });
  }
  delete input.path;
  delete input.paths;
  input.files = files;
  return input;
}

function extensionToolInput(tool, preparedInput) {
  const input = { ...preparedInput };
  if (ACTION_RECEIPT_TOOLS.has(tool)) delete input.include_elements;
  if (tool === 'hawkeye_evaluate') delete input.include_logs;
  if (tool === 'browser_navigate') delete input.include_snapshot;
  if (tool === 'browser_screenshot') {
    delete input.save_to_file;
    delete input.file_path;
    delete input.overwrite;
  }
  return input;
}

function compactObservationResult(tool, result, input) {
  if (tool === 'hawkeye_evaluate' && input?.include_logs !== true) {
    const { logs, ...receipt } = result;
    return receipt;
  }
  if (tool !== 'browser_navigate' || input?.include_snapshot === true) return result;
  const { snapshot, elements, frames, ...receipt } = result;
  const rows = Array.isArray(elements) ? elements : [];
  const priority = { textbox: 0, searchbox: 0, combobox: 0, button: 1, heading: 2, link: 3 };
  const selected = rows.filter((row) => row && row.ref && Object.hasOwn(priority, row.role))
    .map((row, index) => ({ row, index })).sort((a, b) => priority[a.row.role] - priority[b.row.role] || a.index - b.index).slice(0, 12);
  const lines = selected.map(({ row }) => row.role + ' ' + JSON.stringify(String(row.name || '').replace(/\s+/g, ' ').slice(0, 120)) + ' [ref=' + String(row.ref).slice(0, 80) + ']');
  let preview = '';
  let previewElements = 0;
  for (const line of lines) {
    if (preview.length + line.length + 1 > 2400) break;
    preview += (preview ? '\n' : '') + line;
    previewElements++;
  }
  if (!preview && typeof snapshot === 'string') {
    for (const line of snapshot.split('\n')) {
      if (preview.length + line.length + 1 > 2400) break;
      preview += (preview ? '\n' : '') + line;
    }
  }
  receipt.page_preview = preview.slice(0, 2400);
  receipt.preview_complete = false;
  receipt.observation_hint = 'This is a bounded preview, not the full page. Use browser_find for a target or browser_snapshot for full observation; do not navigate again just to read details.';
  if (Array.isArray(frames)) receipt.frames = frames.map((frame) => {
    if (!frame || typeof frame !== 'object') return frame;
    const { snapshot, elements, ...metadata } = frame;
    return metadata;
  });
  receipt.transport_compacted = true;
  receipt.transport_compaction = { scope: 'navigation_preview', preview_elements: previewElements, omitted_fields: ['snapshot', 'elements'] };
  return receipt;
}

function compactActionReceipt(tool, result, input) {
  if (!ACTION_RECEIPT_TOOLS.has(tool) || input?.include_elements === true) return result;
  const receipt = {};
  for (const key of ['ok', 'error', 'category', 'hint', 'action', 'operation', 'tabId', 'frameId', 'url', 'urlBefore', 'urlAfter', 'stateChanged', 'routeChanged', 'navigatedTo', 'outcomeVerified', 'inputDispatched', 'eventEvidence', 'userActivationObserved', 'nativeInput']) {
    if (Object.hasOwn(result, key)) receipt[key] = result[key];
  }
  for (const [key, value] of Object.entries(result)) {
    if (key !== 'snapshot' && key !== 'elements' && key !== 'frames' && !Object.hasOwn(receipt, key)) receipt[key] = value;
  }
  if (Array.isArray(result.frames)) {
    receipt.frames = result.frames.map((frame) => {
      if (!frame || typeof frame !== 'object') return frame;
      const { snapshot, elements, ...metadata } = frame;
      return metadata;
    });
  }
  const omitted = ['snapshot', 'elements'].filter((key) => Object.hasOwn(result, key));
  if (Array.isArray(result.frames) && result.frames.some((frame) => frame && (Object.hasOwn(frame, 'snapshot') || Object.hasOwn(frame, 'elements')))) omitted.push('frames[].snapshot/elements');
  if (omitted.length) {
    receipt.transport_compacted = true;
    receipt.transport_compaction = { scope: 'action_receipt', omitted_fields: omitted, omitted_element_count: Array.isArray(result.elements) ? result.elements.length : 0 };
  }
  return receipt;
}

function compactValue(value, depth = 0, limits = {}) {
  const maxString = Math.max(2000, Number(limits.maxString) || 48_000);
  const maxArray = Math.max(20, Number(limits.maxArray) || 400);
  if (depth > 7) return '[depth truncated]';
  if (typeof value === 'string') {
    if (value.length <= maxString) return value;
    const marker = '\n[adaptive context: kept ' + maxString + ' of ' + value.length + ' chars; middle omitted]\n';
    const room = Math.max(200, maxString - marker.length);
    const head = Math.floor(room * 0.68);
    return value.slice(0, head) + marker + value.slice(value.length - (room - head));
  }
  if (value == null || typeof value === 'number' || typeof value === 'boolean') return value;
  if (Array.isArray(value)) {
    const rows = value.length <= maxArray
      ? value
      : value.slice(0, Math.floor(maxArray * 0.7)).concat([{ _context_notice: 'kept ' + maxArray + ' of ' + value.length + ' items' }], value.slice(-Math.ceil(maxArray * 0.3)));
    return rows.map((item) => compactValue(item, depth + 1, limits));
  }
  if (typeof value === 'object') {
    const out = {};
    for (const key of Object.keys(value).slice(0, 240)) {
      if (key === 'dataUrl' || key === 'imageDataUrl') continue;
      out[key] = compactValue(value[key], depth + 1, limits);
    }
    return out;
  }
  return String(value);
}

function contextChunk(text, input, defaultBudget) {
  const source = String(text == null ? '' : text);
  const budget = Math.max(12_000, Math.min(400_000, Number(input?.context_budget_chars) || defaultBudget || 160_000));
  return { text: source.slice(0, budget) + (source.length > budget ? '\n[Accessibility note truncated; use browser_snapshot for stable paged text.]' : '') };
}

async function toolResultContent(tool, result, requestInput, context = stdioContext) {
  result = result && typeof result === 'object' ? { ...result } : { ok: true, value: result == null ? null : result };
  result.ok = result.ok !== false;
  if (!Object.prototype.hasOwnProperty.call(result, 'value')) result.value = null;
  result.error = result.ok ? null : String(result.error || 'tool failed');
  if (!Object.prototype.hasOwnProperty.call(result, 'hint')) result.hint = null;
  if (tool === 'browser_read_text' && result.ok) {
    const text = typeof result.snapshot === 'string' ? result.snapshot : typeof result.value === 'string' ? result.value : null;
    if (text === null) {
      return toolResultContent(tool, { ...result, ok: false, value: null, category: 'missing_text_payload', error: 'browser_read_text succeeded without a text payload', hint: 'Update the extension and MCP server together, then retry the read.' }, requestInput, context);
    }
    const metadata = compactValue(Object.fromEntries(Object.entries(result).filter(([key]) => key !== 'snapshot' && key !== 'value')), 0, { maxString: 24_000, maxArray: 300 });
    metadata.text_length = text.length;
    return cacheResultPage(tool, text, metadata, requestInput, context, 160_000);
  }
  result = compactObservationResult(tool, result, requestInput);
  result = compactActionReceipt(tool, result, requestInput);
  if (tool === 'browser_captcha_assist') result = Object.assign({}, result, slimCaptchaAssistResult(result));
  if ((tool === 'browser_screenshot' || tool === 'browser_captcha_assist') && result && typeof result.dataUrl === 'string') {
    const match = result.dataUrl.match(/^data:(image\/[^;]+);base64,(.+)$/s);
    if (match) {
      const saveResult = tool === 'browser_screenshot' && requestInput && requestInput.save_to_file === true
        ? await saveScreenshotImage({ base64: match[2], mimeType: match[1], requestInput, result })
        : null;
      const saveNote = !saveResult
        ? ''
        : saveResult.saved_to
          ? '\n已保存到: ' + saveResult.saved_to
          : '\n截图已生成但保存失败: ' + saveResult.save_error;
      const screenshotMetadata = { ok: result.ok !== false, value: null, error: result.error, hint: result.hint, url: result.url, scope: result.scope, fullPage: result.fullPage, element: result.element, mimeType: result.mimeType, bytes: result.bytes, width: result.width, height: result.height, sourceWidth: result.sourceWidth, sourceHeight: result.sourceHeight, scale: result.scale, accessibility: result.accessibility };
      return {
        content: [
          { type: 'image', data: match[2], mimeType: match[1] },
          { type: 'text', text: (tool === 'browser_captcha_assist' ? 'CAPTCHA assistance result' : 'Screenshot captured') + ' (' + String(result.scope || 'viewport') + (result.width && result.height ? ', ' + result.width + 'x' + result.height : '') + ') from ' + String(result.url || 'the connected tab') + (tool === 'browser_captcha_assist' ? '\n\n' + JSON.stringify(slimCaptchaAssistResult(result), null, 2) : (result.accessibilitySnapshot ? '\n\nAccessibility note:\n' + contextChunk(result.accessibilitySnapshot, requestInput, 80_000).text : '')) + saveNote },
        ],
        structuredContent: tool === 'browser_captcha_assist'
          ? slimCaptchaAssistResult(result)
          : compactValue(saveResult ? { ...screenshotMetadata, ...saveResult } : screenshotMetadata),
      };
    }
  }
  if (typeof result.snapshot === 'string' && result.snapshot.trim()) {
    const metadata = compactValue(Object.fromEntries(Object.entries(result).filter(([key]) => key !== 'snapshot')), 0, { maxString: 24_000, maxArray: 300 });
    return cacheResultPage(tool, result.snapshot, metadata, requestInput, context, 160_000);
  }
  const budget = Math.max(12_000, Math.min(400_000, Number(requestInput?.context_budget_chars) || 120_000));
  // Page the original bounded result before compactValue can omit the middle of long text.
  const serialized = JSON.stringify(result, null, 2);
  if (serialized.length <= budget) return { content: [{ type: 'text', text: serialized }], structuredContent: result };
  const metadata = { ok: result.ok, value: null, error: result.error, hint: result.hint };
  return cacheResultPage(tool, serialized, metadata, requestInput, context, budget);
}

function rpcResult(id, result) {
  return { jsonrpc: '2.0', id, result };
}

function rpcError(id, code, message, data) {
  const error = { code, message: String(message || 'Error') };
  if (data !== undefined) error.data = data;
  return { jsonrpc: '2.0', id: id == null ? null : id, error };
}

function createRpcContext() {
  return { hostId: crypto.randomUUID(), active: new Map(), lastSeenAt: Date.now(), protocolVersion: LATEST_PROTOCOL_VERSION };
}

function cancelRpcContext(context, reason) {
  pruneSnapshots(Date.now(), context.hostId);
  extensionSocket?.sendJson({ type: 'hx0_mcp_host_closed', hostId: context.hostId });
  for (const controller of context.active.values()) controller.abort(new Error(reason));
  context.active.clear();
}

const stdioContext = createRpcContext();

async function handleRpc(message, context = createRpcContext()) {
  if (!message || typeof message !== 'object' || Array.isArray(message) || message.jsonrpc !== '2.0') return rpcError(null, -32600, 'Invalid Request');
  const id = message.id;
  const hasId = Object.prototype.hasOwnProperty.call(message, 'id');
  const validId = typeof id === 'string' || (typeof id === 'number' && Number.isFinite(id));
  if (hasId && !validId) return rpcError(null, -32600, 'Invalid request id');
  if (!message.method && hasId && (Object.prototype.hasOwnProperty.call(message, 'result') || Object.prototype.hasOwnProperty.call(message, 'error'))) return null;
  if (typeof message.method !== 'string' || !message.method) return rpcError(hasId ? id : null, -32600, 'Invalid method');
  const method = message.method;
  if (message.params !== undefined && (!message.params || typeof message.params !== 'object' || Array.isArray(message.params))) return hasId ? rpcError(id, -32602, 'Params must be an object') : null;
  context.lastSeenAt = Date.now();
  if (!hasId) {
    if (method === 'notifications/cancelled') {
      const controller = context.active.get(message.params?.requestId);
      if (controller) controller.abort(new Error('MCP request cancelled'));
    }
    return null;
  }
  try {
    if (method === 'initialize') {
      return rpcResult(id, {
        protocolVersion: (context.protocolVersion = SUPPORTED_PROTOCOL_VERSIONS.has(message.params?.protocolVersion) ? message.params.protocolVersion : LATEST_PROTOCOL_VERSION),
        capabilities: { tools: { listChanged: false } },
        serverInfo: { name: SERVER_NAME, title: 'Hx0 HawkEye Browser Automation & Security Testing', version: SERVER_VERSION },
        instructions: 'Enable HawkEye Browser Automation MCP in the Hx0 HawkEye extension popup. Start with browser_snapshot, then prefer browser_find for a specific target and diff=true for incremental state; request boxes only for spatial reasoning. Use fresh refs and stable business ids; refresh the snapshot after navigation or a stale_ref error. Use browser_fill_form for multiple known fields. Each MCP session has its own tab binding; same-tab writes are ordered and reads have bounded concurrency. Read context.next_page_token with the same tool and page_token alone to continue the immutable cached result within two minutes. Never repeat a write to obtain another output page. Numeric cursors above zero are rejected. Tools are filtered by the configured capability profile. Cancellation is best-effort and does not undo completed actions. For exact clicks put the accessible name in text (element is description-only). browser_click auto mode resolves the smallest real target and verifies route changes. Use browser_select_option for selects/comboboxes/custom dropdowns and never click a decorative arrow. For a custom first-party CAPTCHA, call browser_captcha_assist action=analyze and inspect its returned image; never hawkeye_evaluate or download the captcha img. Image codes: read the large dark characters only, then action=solve with authorized=true and answer. Sliders: confirm suggestedOffsetRatio then solve. Third-party anti-bot challenges always require manual completion. For public knowledge, use browser_search, browser_fetch, or browser_research. ' + SEARCH_OPERATOR_GUIDE + ' Active request replay and bounded fuzz testing require an explicitly authorized hawkeye_scope.',
      });
    }
    if (method === 'ping') {
      return rpcResult(id, {});
    }
    if (method === 'tools/list') {
      if (message.params?.cursor !== undefined) return rpcError(id, -32602, 'tools/list is not paginated; omit cursor');
      return rpcResult(id, { tools: context.protocolVersion === '2024-11-05' ? LEGACY_PUBLIC_TOOLS : PUBLIC_TOOLS });
    }
    if (method === 'tools/call') {
      const params = message.params && typeof message.params === 'object' ? message.params : {};
      const name = String(params.name || '');
      if (!ENABLED_TOOL_NAMES.has(name)) {
        return rpcError(id, -32602, 'Unknown or disabled tool: ' + name);
      }
      if (params.arguments !== undefined && (!params.arguments || typeof params.arguments !== 'object' || Array.isArray(params.arguments))) return rpcError(id, -32602, 'Tool arguments must be an object');
      const input = params.arguments || {};
      if (Object.hasOwn(input, 'cursor') && input.cursor !== 0) return rpcError(id, -32602, 'Numeric continuation cursors are unstable and no longer accepted; use context.next_page_token', { category: 'unstable_cursor', hint: 'Request a fresh result with cursor omitted, then send page_token alone.' });
      const validation = validateSchema(TOOL_BY_NAME.get(name).inputSchema, input);
      if (validation.length) return rpcError(id, -32602, 'Invalid tool arguments', { category: 'invalid_arguments', hint: 'Fix the listed parameter paths before retrying; no browser action was executed.', errors: validation });
      if (input.page_token) {
        const content = continueResultPage(name, input, context);
        return rpcResult(id, compatibleToolResult({ ...content, ...(content.structuredContent.ok === false ? { isError: true } : {}) }, context));
      }
      if (context.active.has(id)) return rpcError(id, -32600, 'Duplicate in-flight request id');
      if (activeToolCalls >= MAX_PENDING_CALLS) return rpcError(id, -32000, 'MCP bridge is busy; retry after outstanding calls finish');
      const controller = new AbortController();
      context.active.set(id, controller);
      activeToolCalls += 1;
      const signal = controller.signal;
      try {
        const preparedInput = await prepareToolInput(name, params.arguments || {});
        signal.throwIfAborted();
        const bridgeInput = extensionToolInput(name, preparedInput);
        const initialResult = await callExtension(name, bridgeInput, { signal, hostId: context.hostId });
        signal.throwIfAborted();
        const result = await completeNativeInput(name, bridgeInput, initialResult, signal, context.hostId);
        signal.throwIfAborted();
        const content = await toolResultContent(name, result, preparedInput, context);
        if (signal.aborted) return null;
        return rpcResult(id, compatibleToolResult({ ...content, ...(result?.ok === false ? { isError: true } : {}) }, context));
      } catch (error) {
        if (signal.aborted) return null;
        if (error.rpcCode) return rpcError(id, error.rpcCode, error.message, { category: error.category, hint: error.hint });
        const failed = { ok: false, value: null, error: String(error && error.message || error), hint: null };
        return rpcResult(id, compatibleToolResult({ isError: true, ...await toolResultContent(name, failed, params.arguments || {}) }, context));
      } finally {
        activeToolCalls -= 1;
        if (context.active.get(id) === controller) context.active.delete(id);
      }
    }
    return rpcError(id, -32601, 'Method not found: ' + method);
  } catch (error) {
    return rpcError(id, error.rpcCode || -32603, String(error && error.message || error), error.rpcCode ? { category: error.category, hint: error.hint } : undefined);
  }
}

const wsPort = parsePort();
const legacySseClients = new Map();
const httpSessions = new Map();

function requestOriginAllowed(request) {
  const origin = String(request.headers.origin || '');
  if (!origin) return true;
  try {
    const parsed = new URL(origin);
    return (parsed.protocol === 'http:' || parsed.protocol === 'https:') && (parsed.hostname === '127.0.0.1' || parsed.hostname === 'localhost');
  } catch {
    return false;
  }
}

function extensionOriginAllowed(origin) {
  return String(origin || '').startsWith('chrome-extension://') || String(origin || '').startsWith('moz-extension://');
}

function loopbackCorsHeaders(request) {
  const origin = String(request.headers.origin || '');
  if (!extensionOriginAllowed(origin)) return {};
  return {
    'access-control-allow-origin': origin,
    'access-control-allow-methods': 'GET, OPTIONS',
    'access-control-allow-headers': 'content-type',
    'access-control-allow-private-network': 'true',
    vary: 'Origin',
  };
}

function readJsonBody(request, limitBytes = 1024 * 1024) {
  return new Promise((resolve, reject) => {
    const chunks = [];
    let total = 0;
    request.on('data', (chunk) => {
      total += chunk.length;
      if (total > limitBytes) {
        reject(new Error('Request body too large'));
        request.destroy();
        return;
      }
      chunks.push(chunk);
    });
    request.on('end', () => {
      try { resolve(JSON.parse(Buffer.concat(chunks).toString('utf8'))); }
      catch { reject(new Error('Invalid JSON')); }
    });
    request.on('error', reject);
  });
}

function writeJson(res, status, value, extraHeaders) {
  if (res.destroyed || res.writableEnded) return;
  const body = value == null ? '' : JSON.stringify(value);
  res.writeHead(status, Object.assign({
    'content-type': 'application/json; charset=utf-8',
    'content-length': Buffer.byteLength(body),
    'cache-control': 'no-store',
  }, extraHeaders || {}));
  res.end(body);
}

async function handleHttpRpc(request, res) {
  if (!requestOriginAllowed(request)) {
    writeJson(res, 403, rpcError(null, -32000, 'Cross-origin access denied'));
    return;
  }
  const contentType = String(request.headers['content-type'] || '').toLowerCase();
  if (!contentType.startsWith('application/json')) {
    writeJson(res, 415, rpcError(null, -32600, 'Content-Type must be application/json'));
    return;
  }
  let payload;
  try { payload = await readJsonBody(request); }
  catch (error) {
    writeJson(res, 400, rpcError(null, -32700, error.message));
    return;
  }
  if (Array.isArray(payload)) {
    writeJson(res, 400, rpcError(null, -32600, 'MCP messages must be individual requests, responses or notifications'));
    return;
  }
  const requestedVersion = String(request.headers['mcp-protocol-version'] || '');
  if (requestedVersion && !SUPPORTED_PROTOCOL_VERSIONS.has(requestedVersion)) {
    writeJson(res, 400, rpcError(null, -32600, 'Unsupported MCP protocol version'));
    return;
  }
  const sessionId = String(request.headers['mcp-session-id'] || '');
  let context = sessionId ? httpSessions.get(sessionId) : null;
  if (sessionId && !context) { writeJson(res, 404, rpcError(null, -32001, 'Unknown MCP session')); return; }
  const headers = {};
  if (!context && payload?.method === 'initialize' && Object.prototype.hasOwnProperty.call(payload, 'id')) {
    if (httpSessions.size >= MAX_HTTP_SESSIONS) { writeJson(res, 503, rpcError(payload.id, -32000, 'MCP session capacity reached')); return; }
    context = createRpcContext();
    const newSessionId = crypto.randomBytes(24).toString('hex');
    httpSessions.set(newSessionId, context);
    headers['mcp-session-id'] = newSessionId;
  }
  const transient = !context;
  context ||= createRpcContext(); // Stateless calls have no stable binding or pagination session.
  const response = await handleRpc(payload, context);
  if (transient) cancelRpcContext(context, 'Stateless HTTP request finished');
  headers['mcp-protocol-version'] = context.protocolVersion;
  if (!response) res.writeHead(202, { 'cache-control': 'no-store', ...headers }).end();
  else writeJson(res, 200, response, headers);
}

const httpServer = http.createServer((request, res) => {
  const url = new URL(String(request.url || '/'), 'http://127.0.0.1');
  if (request.method === 'OPTIONS' && url.pathname === '/health') {
    const origin = String(request.headers.origin || '');
    if (!extensionOriginAllowed(origin)) { res.writeHead(403).end(); return; }
    res.writeHead(204, loopbackCorsHeaders(request)).end();
    return;
  }
  if (request.method === 'POST' && url.pathname === '/mcp') {
    handleHttpRpc(request, res).catch((error) => writeJson(res, 500, rpcError(null, -32603, error.message)));
    return;
  }
  if (request.method === 'GET' && url.pathname === '/mcp') {
    if (!requestOriginAllowed(request)) { res.writeHead(403).end(); return; }
    res.writeHead(405, { allow: 'POST, DELETE', 'cache-control': 'no-store' }).end();
    return;
  }
  if (request.method === 'DELETE' && url.pathname === '/mcp') {
    if (!requestOriginAllowed(request)) { res.writeHead(403).end(); return; }
    const sessionId = String(request.headers['mcp-session-id'] || '');
    const context = httpSessions.get(sessionId);
    if (!context) { res.writeHead(404).end(); return; }
    cancelRpcContext(context, 'MCP session closed');
    httpSessions.delete(sessionId);
    setImmediate(exitIfUnused);
    res.writeHead(204, { 'cache-control': 'no-store' }).end();
    return;
  }
  if (request.method === 'GET' && url.pathname === '/sse') {
    if (!requestOriginAllowed(request)) { res.writeHead(403).end(); return; }
    if (legacySseClients.size >= MAX_HTTP_SESSIONS) { res.writeHead(503).end(); return; }
    const sessionId = crypto.randomBytes(18).toString('hex');
    const context = createRpcContext();
    res.writeHead(200, {
      'content-type': 'text/event-stream',
      'cache-control': 'no-store',
      connection: 'keep-alive',
    });
    legacySseClients.set(sessionId, { stream: res, context });
    res.write('event: endpoint\ndata: /messages?sessionId=' + sessionId + '\n\n');
    const timer = setInterval(() => { try { res.write(': keepalive\n\n'); } catch {} }, 20_000);
    timer.unref();
    res.on('close', () => { clearInterval(timer); cancelRpcContext(context, 'SSE session closed'); legacySseClients.delete(sessionId); setImmediate(exitIfUnused); });
    return;
  }
  if (request.method === 'POST' && url.pathname === '/messages') {
    if (!requestOriginAllowed(request)) { res.writeHead(403).end(); return; }
    const session = legacySseClients.get(String(url.searchParams.get('sessionId') || ''));
    if (!session) { writeJson(res, 404, rpcError(null, -32001, 'Unknown SSE session')); return; }
    const contentType = String(request.headers['content-type'] || '').toLowerCase();
    if (!contentType.startsWith('application/json')) { writeJson(res, 415, rpcError(null, -32600, 'Content-Type must be application/json')); return; }
    const { stream, context } = session;
    readJsonBody(request).then((payload) => handleRpc(payload, context)).then((response) => {
      if (response && !stream.destroyed && !stream.writableEnded) {
        if (stream.writableLength > MAX_WS_MESSAGE_BYTES) stream.destroy();
        else stream.write('event: message\ndata: ' + JSON.stringify(response) + '\n\n');
      }
      res.writeHead(202, { 'cache-control': 'no-store' }).end();
    }).catch((error) => writeJson(res, 400, rpcError(null, -32700, error.message)));
    return;
  }
  if (request.method === 'GET' && url.pathname === '/health') {
    const origin = String(request.headers.origin || '');
    if (origin && !extensionOriginAllowed(origin) && !requestOriginAllowed(request)) { res.writeHead(403).end(); return; }
    writeJson(res, 200, { ok: true, name: SERVER_NAME, version: SERVER_VERSION, extensionConnected: extensionReady, extension: extensionInfo, connectedBrowsers: [...extensionClients].filter((client) => client.info).map((client) => ({ ...client.info, active: client === extensionSocket })), pendingCalls: pendingCalls.size, activeToolCalls, maxPendingCalls: MAX_PENDING_CALLS, httpSessions: httpSessions.size, toolProfile: TOOL_PROFILE.profile, toolCapabilities: TOOL_PROFILE.capabilities, enabledTools: TOOL_PROFILE.tools.length, snapshots: snapshots.size, snapshotCacheBytes, stdio: { ...stdioWriter.stats, ...stdioInput.stats, activeRequests: stdioRequests, transportClosed: stdioClosed } }, loopbackCorsHeaders(request));
    return;
  }
  res.writeHead(404, { 'content-type': 'text/plain; charset=utf-8', 'cache-control': 'no-store' });
  res.end('Hx0 HawkEye MCP server\n');
});

httpServer.on('upgrade', (request, socket, head) => {
  const origin = String(request.headers.origin || '');
  const allowedOrigin = origin.startsWith('chrome-extension://') || origin.startsWith('moz-extension://') || (process.env.HX0_MCP_ALLOW_TEST_ORIGIN === '1' && origin === 'hx0-test://local');
  const url = new URL(String(request.url || '/'), 'http://127.0.0.1');
  const key = String(request.headers['sec-websocket-key'] || '');
  if (url.pathname !== WS_PATH || !allowedOrigin || !/^[A-Za-z0-9+/]{22}==$/.test(key) || request.headers['sec-websocket-version'] !== '13' || String(request.headers.upgrade || '').toLowerCase() !== 'websocket') {
    stderr('Rejected WebSocket bridge request (origin=' + (origin || 'missing') + ', path=' + url.pathname + ').');
    socket.write('HTTP/1.1 403 Forbidden\r\nConnection: close\r\n\r\n');
    socket.destroy();
    return;
  }
  const accept = crypto.createHash('sha1').update(key + WS_GUID).digest('base64');
  socket.write([
    'HTTP/1.1 101 Switching Protocols',
    'Upgrade: websocket',
    'Connection: Upgrade',
    'Sec-WebSocket-Accept: ' + accept,
    '', '',
  ].join('\r\n'));
  const client = new BrowserSocket(socket, handleExtensionMessage, (closed) => {
    extensionClients.delete(closed);
    rejectPending('HawkEye extension disconnected', closed);
    if (extensionSocket === closed) {
      extensionSocket = null;
      selectExtension();
      stderr('Active HawkEye extension disconnected; refreshed available browser');
    }
  });
  // Bound unauthenticated/idle connections as well as normal browser connections.
  if (extensionClients.size >= 8) { client.close(); return; }
  extensionClients.add(client);
  if (head?.length) client.feed(head);
});

httpServer.on('error', (error) => {
  if (error && error.code === 'EADDRINUSE') {
    stderr('Port ' + wsPort + ' is already in use. Stop the other MCP process or set HX0_MCP_WS_PORT to another port in both the MCP client and HawkEye popup.');
  } else {
    stderr(error && error.stack || error);
  }
  shutdown();
  process.exit(2);
});

httpServer.listen(wsPort, '127.0.0.1', () => {
  stderr('WebSocket bridge listening on ws://127.0.0.1:' + wsPort + WS_PATH);
  stderr('Streamable HTTP endpoint: http://127.0.0.1:' + wsPort + '/mcp');
  stderr('Legacy SSE endpoint: http://127.0.0.1:' + wsPort + '/sse');
});

const keepAlive = setInterval(() => {
  const now = Date.now();
  pruneSnapshots(now);
  exitIfUnused();
  for (const client of extensionClients) {
    if (now - client.lastSeenAt > 65_000) client.close();
    else client.sendJson({ type: 'hx0_mcp_ping', at: now });
  }
  for (const [id, context] of httpSessions) {
    if (!context.active.size && now - context.lastSeenAt > SESSION_IDLE_MS) { cancelRpcContext(context, 'MCP session expired'); httpSessions.delete(id); }
  }
}, 20_000);
keepAlive.unref();

// A blocked stdout never blocks input parsing: cancellation stays on the control path.
// Sustained overload closes this transport instead of retaining unbounded replies.
function createBoundedWriter(stream, onFatal, { maxBytes = 32 * 1024 * 1024, maxMessages = 128 } = {}) {
  const queue = [];
  let bytes = 0;
  let blocked = false;
  let closed = false;
  const fail = (reason) => { if (!closed) { closed = true; queue.length = 0; bytes = 0; onFatal(reason); } };
  const flush = () => {
    if (closed || blocked) return;
    while (queue.length) {
      const row = queue.shift(); bytes -= row.length;
      try { if (!stream.write(row)) { blocked = true; break; } }
      catch (error) { fail('stdio output failed: ' + error.message); break; }
    }
  };
  stream.on('drain', () => { blocked = false; flush(); });
  stream.on('error', (error) => fail('stdio output failed: ' + error.message));
  return {
    send(value) {
      if (closed) return false;
      const row = Buffer.from(JSON.stringify(value) + '\n');
      if (queue.length >= maxMessages || bytes + row.length + (stream.writableLength || 0) > maxBytes) { fail('stdio output capacity exceeded; reconnect the slow client'); return false; }
      queue.push(row); bytes += row.length; flush(); return true;
    },
    get blocked() { return blocked; },
    get stats() { return { queuedBytes: bytes, writableBytes: stream.writableLength || 0, queuedMessages: queue.length, blocked, closed, maxBytes }; },
  };
}
function createBoundedInput(onLine, onFatal, { maxLineBytes = 1024 * 1024, maxBufferedBytes = 4 * 1024 * 1024, batchSize = 64 } = {}) {
  let buffer = Buffer.alloc(0);
  let scheduled = false;
  let closed = false;
  let ended = false;
  const fail = (reason) => { if (!closed) { closed = true; buffer = Buffer.alloc(0); onFatal(reason); } };
  const pump = () => {
    scheduled = false;
    if (closed) return;
    let processed = 0;
    while (processed++ < batchSize) {
      const at = buffer.indexOf(10);
      if (at < 0) break;
      if (at > maxLineBytes) { fail('stdio input line exceeds 1 MiB'); return; }
      const line = buffer.subarray(0, at).toString('utf8');
      buffer = buffer.subarray(at + 1);
      onLine(line);
      if (closed) return;
    }
    if (buffer.indexOf(10) >= 0) { scheduled = true; setImmediate(pump); }
    else if (buffer.length > maxLineBytes) fail('stdio input line exceeds 1 MiB');
    else if (ended && buffer.length) fail('stdio input ended with an unterminated JSON-RPC message');
    // Copy small tails so a tiny partial line does not retain a large chunk.
    else if (buffer.length && buffer.byteLength < buffer.buffer.byteLength / 4) buffer = Buffer.from(buffer);
  };
  return {
    feed(chunk) {
      if (closed) return;
      if (buffer.length + chunk.length > maxBufferedBytes) { fail('stdio input backlog exceeds 4 MiB'); return; }
      buffer = buffer.length ? Buffer.concat([buffer, chunk]) : Buffer.from(chunk);
      if (!scheduled) { scheduled = true; setImmediate(pump); }
    },
    end() { ended = true; if (!scheduled) pump(); },
    get stats() { return { bufferedBytes: buffer.length, maxLineBytes, maxBufferedBytes, closed }; },
  };
}
let stdioClosed = false;
let stdioRequests = 0;
function closeStdio(reason) {
  if (stdioClosed) return;
  stdioClosed = true;
  stderr(reason);
  cancelRpcContext(stdioContext, reason);
  process.stdin.destroy();
  // HTTP users may still be sharing the process. Only the failed stdio transport closes.
}
const stdioWriter = createBoundedWriter(process.stdout, closeStdio);
const stdioInput = createBoundedInput((line) => {
  if (stdioClosed || !line.trim()) return;
  let message;
  try { message = JSON.parse(line); }
  catch { stdioWriter.send(rpcError(null, -32700, 'Parse error')); return; }
  if (message?.method === 'notifications/cancelled' && !Object.hasOwn(message, 'id')) {
    void handleRpc(message, stdioContext); return;
  }
  if (stdioWriter.blocked || stdioRequests >= 32) {
    if (Object.hasOwn(message || {}, 'id')) stdioWriter.send(rpcError(message.id, -32000, 'stdio client is busy; drain responses before retrying', { category: 'capacity_exceeded', retryable: true }));
    return;
  }
  stdioRequests += 1;
  Promise.resolve(handleRpc(message, stdioContext)).then((response) => {
    if (response && !stdioClosed) stdioWriter.send(response);
  }).catch((error) => stderr(error && error.stack || error)).finally(() => { stdioRequests -= 1; });
}, closeStdio);
const ownedParentPID = Number(process.env.DEEPSENTRY_MCP_PARENT_PID || '');
const ownedByDeepSentry = Number.isInteger(ownedParentPID) && ownedParentPID > 0 && ownedParentPID !== process.pid;
if (ownedByDeepSentry) {
  const parentWatch = setInterval(() => {
    try { process.kill(ownedParentPID, 0); }
    catch { shutdown(); process.exit(0); }
  }, 1000);
  parentWatch.unref();
}
process.stdin.on('data', (chunk) => stdioInput.feed(chunk));
let stdinEnded = false;
const keepHttpAlive = process.argv.includes('--keep-alive') || process.env.HX0_MCP_KEEP_ALIVE === '1';
function exitIfUnused() {
  if (stdinEnded && !keepHttpAlive && !httpSessions.size && !legacySseClients.size) {
    shutdown();
    process.exit(0);
  }
}
process.stdin.on('end', () => {
  stdioInput.end();
  stdinEnded = true;
  if (ownedByDeepSentry) {
    stderr('stdio input ended; owned MCP server exiting with DeepSentry');
    shutdown();
    process.exit(0);
  }
  cancelRpcContext(stdioContext, 'stdio input ended');
  setImmediate(exitIfUnused);
});
process.stdin.on('error', (error) => closeStdio('stdio input failed: ' + error.message));

function shutdown() {
  clearInterval(keepAlive);
  cancelRpcContext(stdioContext, 'MCP server shutting down');
  for (const context of httpSessions.values()) cancelRpcContext(context, 'MCP server shutting down');
  httpSessions.clear();
  rejectPending('MCP server shutting down');
  for (const client of extensionClients) client.close();
  extensionClients.clear();
  for (const { stream, context } of legacySseClients.values()) {
    cancelRpcContext(context, 'MCP server shutting down');
    try { stream.end(); } catch {}
  }
  legacySseClients.clear();
  try { httpServer.close(); } catch {}
}
process.once('SIGINT', () => { shutdown(); process.exit(0); });
process.once('SIGTERM', () => { shutdown(); process.exit(0); });
process.once('exit', shutdown);
