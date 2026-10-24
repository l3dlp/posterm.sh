'use strict';
// POSTERM, the wall. Vanilla, no build: one SSE stream paints the post-its,
// one WebSocket per open lab is its terminal. Words come from /i18n.js.

const $ = (s, el = document) => el.querySelector(s);
const el = (tag, cls, text) => {
  const n = document.createElement(tag);
  if (cls) n.className = cls;
  if (text != null) n.textContent = text;
  return n;
};
const TITLE = document.title; // POSTERM_TITLE, filled in by the hub
// t('key', {name}) from the dictionary of POSTERM_LANG; tn adds the plural form.
const I18N = window.I18N || {};
const t = (key, vars = {}) => (I18N[key] ?? key).replace(/\{(\w+)\}/g, (m, k) => (k in vars ? vars[k] : m));
const PLURAL = new Intl.PluralRules(document.documentElement.lang);
const tn = (key, n, vars = {}) => t(`${key}_${PLURAL.select(n) === 'one' ? 'one' : 'other'}`, { n, ...vars });
const STATES = Object.fromEntries(['question', 'encours', 'pose', 'eteint'].map(s => [s, t(`state.${s}`)]));
const store = {
  get: (k, def) => { try { return JSON.parse(localStorage.getItem('posterm.' + k) ?? localStorage.getItem('monia.' + k)) ?? def; } catch { return def; } },
  set: (k, v) => { try { localStorage.setItem('posterm.' + k, JSON.stringify(v)); localStorage.removeItem('monia.' + k); } catch {} },
};

let world = { hub: '', machines: [] };
let labs = [];             // flat list, wall order
let cur = null;            // key ("machine/id") of the open lab
const picked = new Set();  // keys ticked for broadcast
const filter = { state: '', q: '' };
let order = store.get('order', []); // custom lab order (rail, wall, Ctrl+Tab), per browser

const path = (key, rest = '') => {
  const i = key.indexOf('/');
  return `/m/${encodeURIComponent(key.slice(0, i))}/api/labs/${encodeURIComponent(key.slice(i + 1))}${rest}`;
};
const byKey = key => labs.find(l => l.key === key);

// ---------------------------------------------------------------- network

async function api(url, body, method) {
  const res = await fetch(url, {
    method: method || (body ? 'POST' : 'GET'),
    headers: body ? { 'Content-Type': 'application/json' } : {},
    body: body ? JSON.stringify(body) : undefined,
  });
  if (res.status === 401) { askLogin(); throw new Error(t('ui.token_needed')); }
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || t('ui.http_error', { code: res.status }));
  return data;
}

let toastTimer;
function toast(msg) {
  const t = $('#toast');
  t.textContent = msg;
  t.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { t.hidden = true; }, 3500);
}
const guard = fn => (...a) => Promise.resolve(fn(...a)).catch(e => toast(e.message));

let es;
function listen() {
  es?.close();
  es = new EventSource('/api/stream');
  es.onopen = () => document.body.classList.remove('offline');
  es.onmessage = e => { world = JSON.parse(e.data); refresh(); };
  es.onerror = () => {
    document.body.classList.add('offline');
    if (es.readyState !== EventSource.CLOSED) return; // the browser retries by itself
    fetch('/api/journal').then(r => (r.status === 401 ? askLogin() : setTimeout(listen, 3000)), () => setTimeout(listen, 3000));
  };
}

function askLogin() {
  const d = $('#dlg-login');
  if (!d.open) d.showModal();
}
$('#dlg-login').addEventListener('cancel', e => e.preventDefault());
$('#form-login').addEventListener('submit', async e => {
  e.preventDefault();
  const err = $('#login-error');
  const res = await fetch('/api/login', {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ token: $('#login-token').value }),
  });
  err.hidden = res.ok;
  if (!res.ok) { err.textContent = t('ui.token_bad'); return; }
  $('#login-token').value = '';
  $('#dlg-login').close();
  listen();
  inbox();
  route();
});

// ---------------------------------------------------------------- painting

function ago(since) {
  const s = Math.max(0, Date.now() / 1000 - since);
  if (s < 10) return t('ago.now');
  if (s < 90) return t('ago.s', { n: Math.round(s) });
  if (s < 5400) return t('ago.min', { n: Math.round(s / 60) });
  if (s < 129600) return t('ago.h', { n: Math.round(s / 3600) });
  return t('ago.d', { n: Math.round(s / 86400) });
}
const label = l => !l.online ? t('state.offline')
  : STATES[l.state] + (l.code ? ` · ${t('state.code', { code: l.code })}` : '') + (l.seen ? ` · ${t('state.seen')}` : '');
const put = (node, text) => { if (node.textContent !== text) node.textContent = text; };
const shown = l => (!filter.state || l.state === filter.state)
  && (!filter.q || `${l.name} ${l.dir} ${l.cmd} ${l.m}`.toLowerCase().includes(filter.q));

function refresh() {
  labs = world.machines.flatMap(m => m.labs.map(l => ({
    ...l, m: m.name, online: m.online, key: `${m.name}/${l.id}`, state: m.online ? l.state : 'eteint',
  })));
  // Custom order, inside each machine; labs never placed keep the hub's order, at the end.
  const rank = new Map(order.map((k, i) => [k, i]));
  const mi = new Map(world.machines.map((m, i) => [m.name, i]));
  labs.sort((a, b) => mi.get(a.m) - mi.get(b.m) || (rank.get(a.key) ?? 1e9) - (rank.get(b.key) ?? 1e9));
  for (const key of picked) if (!byKey(key)) picked.delete(key);

  const count = Object.fromEntries(Object.keys(STATES).map(s => [s, 0]));
  for (const l of labs) count[l.state]++;
  document.title = count.question ? `(${count.question}) ${TITLE}` : TITLE;
  if (navigator.setAppBadge) (count.question ? navigator.setAppBadge(count.question) : navigator.clearAppBadge()).catch(() => {});

  paintChips(count);
  paintWall();
  paintBroadcast();
  if (cur) paintLab();
}

function paintChips(count) {
  const nav = $('#chips');
  if (!nav.children.length) {
    for (const s of Object.keys(STATES)) {
      const b = el('button');
      b.type = 'button';
      b.dataset.state = s;
      b.append(el('i', s), el('b'), el('span', '', STATES[s]));
      b.addEventListener('click', () => { filter.state = filter.state === s ? '' : s; refresh(); });
      nav.append(b);
    }
  }
  for (const b of nav.children) {
    put($('b', b), String(count[b.dataset.state]));
    b.classList.toggle('on', filter.state === b.dataset.state);
    b.setAttribute('aria-pressed', filter.state === b.dataset.state);
  }
}

const cards = new Map(); // key → post-it, kept across refreshes so nothing flickers

function card(l) {
  const a = el('article', 'postit');
  a.tabIndex = 0;
  const pick = el('label', 'pick');
  const box = el('input');
  box.type = 'checkbox';
  box.setAttribute('aria-label', t('ui.tick'));
  box.addEventListener('change', () => { box.checked ? picked.add(l.key) : picked.delete(l.key); refresh(); });
  pick.addEventListener('click', e => e.stopPropagation());
  pick.append(box);
  const foot = el('footer');
  foot.append(a._state = el('span', 'state'), a._since = el('span', 'since'));
  a.append(pick, a._name = el('h3'), a._dir = el('p', 'dir'), a._screen = el('div', 'screen'), foot);
  a._box = box;
  const open = () => { location.hash = l.key; };
  a.addEventListener('click', open);
  a.addEventListener('keydown', e => { if (e.key === 'Enter') open(); });
  return a;
}

function paintWall() {
  const wall = $('#wall');
  if (!labs.length) {
    cards.clear();
    const p = el('p', 'empty');
    p.append(t('ui.empty_1'), el('b', '', t('ui.empty_2')), t('ui.empty_3'), el('b', '', t('ui.empty_4')), t('ui.empty_5'));
    wall.replaceChildren(p);
    return;
  }
  $('.empty', wall)?.remove();
  const alive = new Set();
  for (const m of world.machines) {
    let sec = [...wall.children].find(s => s.dataset.m === m.name);
    if (!sec) {
      sec = el('section');
      sec.dataset.m = m.name;
      sec.append(el('h2'), el('div', 'grid'));
      wall.append(sec);
    }
    alive.add(sec);
    const visible = labs.filter(l => l.m === m.name && shown(l));
    sec.hidden = !visible.length;
    $('h2', sec).replaceChildren(el('b', '', m.name), ` · ${m.online ? tn('n.labs', visible.length) : t('state.offline')}`);
    $('h2', sec).hidden = world.machines.length < 2;
    const grid = $('.grid', sec);
    labs.filter(l => l.m === m.name).forEach((l, i) => {
      let a = cards.get(l.key);
      if (!a) cards.set(l.key, a = card(l));
      if (grid.children[i] !== a) grid.insertBefore(a, grid.children[i] || null);
      a.className = `postit ${l.state}${picked.has(l.key) ? ' picked' : ''}`;
      a.hidden = !shown(l);
      a._box.checked = picked.has(l.key);
      put(a._name, l.name);
      put(a._dir, '‎' + l.dir + (l.cmd ? ` · ${l.cmd}` : ''));
      a._screen.classList.toggle('note', !!l.note);
      put(a._screen, l.note || l.screen || '');
      put(a._state, label(l));
      put(a._since, ago(l.since));
    });
  }
  for (const sec of [...wall.children]) if (!alive.has(sec)) sec.remove();
  for (const [key, a] of cards) if (!byKey(key)) { a.remove(); cards.delete(key); }
}

// ---------------------------------------------------------------- broadcast

function paintBroadcast() {
  $('#broadcast').hidden = !picked.size || !!cur;
  put($('#broadcast-count'), t('ui.broadcast_to', { labs: tn('n.labs', picked.size) }));
}
$('#broadcast-clear').addEventListener('click', () => { picked.clear(); refresh(); });
$('#broadcast').addEventListener('submit', guard(async e => {
  e.preventDefault();
  const text = $('#broadcast-text').value;
  const keys = [...picked];
  const done = await Promise.allSettled(keys.map(k => api(path(k, '/keys'), { text, enter: true })));
  const ko = done.filter(d => d.status === 'rejected').length;
  toast(ko ? t('ui.broadcast_ko', { ok: keys.length - ko, ko }) : t('ui.broadcast_ok', { labs: tn('n.labs', keys.length) }));
  $('#broadcast-text').value = '';
  picked.clear();
  refresh();
}));

$('#search').addEventListener('input', e => { filter.q = e.target.value.trim().toLowerCase(); refresh(); });

// ---------------------------------------------------------------- the open lab

let term, fit, sock;
const enc = new TextEncoder();
const SEQ = { esc: '\x1b', tab: '\t', ctrlc: '\x03', up: '\x1b[A', down: '\x1b[B', enter: '\r' };
const type = s => { if (sock?.readyState === 1) sock.send(enc.encode(s)); };

// newTerm opens an xterm.js in box, wired to whatever socket conn() returns.
function newTerm(box, conn) {
  const t = new Terminal({
    cursorBlink: true,
    fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace',
    fontSize: 13,
    scrollback: 0, // tmux owns the history (wheel = scroll back)
    theme: { background: '#000000', foreground: '#eaeaea', cursor: '#ccff00', selectionBackground: 'rgba(204,255,0,.35)' },
  });
  const f = new FitAddon.FitAddon();
  t.loadAddon(f);
  t.open(box);
  const send = d => { const s = conn(); if (s?.readyState === 1) s.send(d); };
  t.onData(d => send(enc.encode(d)));
  t.onBinary(d => send(Uint8Array.from(d, c => c.charCodeAt(0))));
  t.onResize(({ cols, rows }) => send(JSON.stringify({ cols, rows })));
  t.attachCustomKeyEventHandler(e => !shortcut(e));
  new ResizeObserver(() => { if (cur) f.fit(); }).observe(box);
  selectable(t, box);
  return [t, f];
}

// ---------------------------------------------------------------- selection

// A plain drag selects in tmux (mouse mode), which copies to its own buffer and
// hands the text to the outer terminal as OSC 52; xterm.js ignores that, so we
// take it. Shift+drag selects in xterm itself. Either way a menu pops where the
// button was released: copy, or save as .md. A click anywhere else drops it.
const selMenu = $('#selmenu');
let selText = '', lastUp = null;
window.addEventListener('pointerup', e => {
  lastUp = e.target.closest?.('.xterm') ? { x: e.clientX, y: e.clientY, at: Date.now() } : null;
}, true);

function selectable(t, box) {
  t.parser.registerOscHandler(52, data => {
    const b64 = data.slice(data.indexOf(';') + 1);
    if (b64 && b64 !== '?' && lastUp && Date.now() - lastUp.at < 3000 && box.contains(document.elementFromPoint(lastUp.x, lastUp.y))) {
      try { offerSelection(new TextDecoder().decode(Uint8Array.from(atob(b64), c => c.charCodeAt(0)))); } catch {}
    }
    return true; // never let the page's clipboard be written behind our back
  });
  box.addEventListener('mouseup', e => {
    if (e.shiftKey) setTimeout(() => { if (t.hasSelection()) offerSelection(t.getSelection()); });
  });
}

function offerSelection(text) {
  selText = text.split('\n').map(l => l.trimEnd()).join('\n').replace(/^\n+|\n+$/g, '');
  if (!selText.trim() || !lastUp) return;
  selMenu.hidden = false;
  const w = selMenu.offsetWidth, h = selMenu.offsetHeight;
  selMenu.style.left = `${Math.max(4, Math.min(lastUp.x + 6, innerWidth - w - 4))}px`;
  selMenu.style.top = `${lastUp.y + 10 + h > innerHeight ? Math.max(4, lastUp.y - h - 10) : lastUp.y + 10}px`;
  selMenu.querySelector('button').focus({ preventScroll: true });
}
const closeSel = () => { selMenu.hidden = true; selText = ''; };
document.addEventListener('pointerdown', e => { if (!selMenu.hidden && !selMenu.contains(e.target)) closeSel(); }, true);
window.addEventListener('keydown', e => { if (e.key === 'Escape' && !selMenu.hidden) { e.preventDefault(); e.stopPropagation(); closeSel(); } }, true);
selMenu.addEventListener('click', guard(async e => {
  const act = e.target.closest('button')?.dataset.act;
  if (!act) return;
  const text = selText;
  closeSel();
  if (act === 'copy') {
    await copyText(text);
    toast(t('sel.copied', { n: text.length }));
  } else {
    const d = new Date(), z = n => String(n).padStart(2, '0');
    const file = `${(cur || 'posterm').split('/').pop()}-${d.getFullYear()}${z(d.getMonth() + 1)}${z(d.getDate())}-${z(d.getHours())}${z(d.getMinutes())}${z(d.getSeconds())}.md`;
    const a = el('a');
    a.href = URL.createObjectURL(new Blob([text + '\n'], { type: 'text/markdown;charset=utf-8' }));
    a.download = file;
    a.click();
    setTimeout(() => URL.revokeObjectURL(a.href), 1000);
    toast(t('sel.saved', { file }));
  }
}));

// copyText needs the click that called it (user activation); the async
// clipboard API only exists on HTTPS or localhost, execCommand is the fallback.
async function copyText(text) {
  if (navigator.clipboard && isSecureContext) return navigator.clipboard.writeText(text);
  const ta = el('textarea');
  ta.value = text;
  ta.style.cssText = 'position:fixed;top:0;left:0;opacity:0';
  document.body.append(ta);
  ta.select();
  const ok = document.execCommand('copy');
  ta.remove();
  if (!ok) throw new Error(t('sel.copy_failed'));
}
const wsURL = (rest, t) => `${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}${path(cur, rest)}?cols=${t.cols}&rows=${t.rows}`;

function ensureTerm() {
  if (!term) [term, fit] = newTerm($('#term'), () => sock);
}

function detach() {
  const s = sock;
  sock = null;
  s?.close();
}

function attach() {
  detach();
  veil();
  term.reset();
  fit.fit();
  const s = sock = new WebSocket(wsURL('/ws', term));
  s.binaryType = 'arraybuffer';
  s.onmessage = e => term.write(new Uint8Array(e.data));
  s.onclose = () => {
    if (sock !== s) return;
    sock = null;
    const l = byKey(cur);
    if (!l) return;
    l.state === 'eteint' && !l.code ? veil(t('ui.dead'), t('ui.dead_btn'), start) : veil(t('ui.lost'), t('ui.lost_btn'), attach);
  };
  if (matchMedia('(pointer: fine)').matches) term.focus();
}

function veil(text, action, fn) {
  $('#veil').hidden = !text;
  if (!text) return;
  $('#veil-text').textContent = text;
  $('#veil-btn').textContent = action;
  $('#veil-btn').onclick = guard(fn);
}

// ---------------------------------------------------------------- the lab's bash

// Each lab has a bash of its own under its terminal (a separate tmux session, in
// the lab's directory). On/off per lab, one height for all: both kept in this browser.
let shTerm, shFit, shSock;
const shellOff = new Set(store.get('shellOff', []));
const shellOn = () => !!cur && !shellOff.has(cur);
let shellH = store.get('shellH', 100);

function paintShell() {
  const on = shellOn();
  $('#shell').hidden = !on;
  $('#shell').style.height = `${shellH}px`;
  $('#lab-shell').classList.toggle('on', on);
  $('#lab-shell').setAttribute('aria-pressed', on);
}

function detachShell() {
  const s = shSock;
  shSock = null;
  s?.close();
}

function attachShell() {
  detachShell();
  paintShell();
  if (!shellOn()) return;
  if (!shTerm) [shTerm, shFit] = newTerm($('#shell-term'), () => shSock);
  $('#shell-veil').hidden = true;
  shTerm.reset();
  shFit.fit();
  const s = shSock = new WebSocket(wsURL('/sh', shTerm));
  s.binaryType = 'arraybuffer';
  s.onmessage = e => shTerm.write(new Uint8Array(e.data));
  s.onclose = () => {
    if (shSock !== s) return;
    shSock = null;
    $('#shell-veil').hidden = false;
  };
}
$('#shell-veil button').addEventListener('click', () => { attachShell(); shTerm?.focus(); });

$('#lab-shell').addEventListener('click', () => {
  shellOff.has(cur) ? shellOff.delete(cur) : shellOff.add(cur);
  store.set('shellOff', [...shellOff]);
  attachShell();
  (shellOn() ? shTerm : term)?.focus();
});

// The grip: drag (mouse or finger), arrows, double-click = back to 100 px.
const grip = $('#shell-grip');
const setShellH = h => {
  shellH = Math.round(Math.max(40, Math.min(h, $('#stage').clientHeight - 160)));
  $('#shell').style.height = `${shellH}px`;
  store.set('shellH', shellH);
};
grip.addEventListener('pointerdown', e => {
  e.preventDefault();
  grip.setPointerCapture(e.pointerId);
  const y0 = e.clientY, h0 = $('#shell').offsetHeight;
  const move = ev => setShellH(h0 + y0 - ev.clientY);
  grip.addEventListener('pointermove', move);
  grip.addEventListener('lostpointercapture', () => grip.removeEventListener('pointermove', move), { once: true });
});
grip.addEventListener('dblclick', () => setShellH(100));
grip.addEventListener('keydown', e => {
  const step = { ArrowUp: 10, ArrowDown: -10 }[e.key];
  if (step) { e.preventDefault(); setShellH(shellH + step); }
});

// ---------------------------------------------------------------- dropping on the bash

// Files or whole folders dropped on the bash land in its current directory
// (the hub asks tmux where the bash is). Existing files are only overwritten
// after a yes.
const dropVeil = $('#shell-drop');
const hasFiles = e => [...(e.dataTransfer?.types || [])].includes('Files');
let dragDepth = 0, dropping = null; // dropping: the AbortController of the drop in progress

// ✕ or Esc cancels a drop at any stage; the server takes back what it wrote.
const cancelDrop = () => dropping?.abort();
$('.drop-close', dropVeil).addEventListener('click', cancelDrop);
window.addEventListener('keydown', e => {
  if (e.key !== 'Escape' || !dropping) return;
  e.preventDefault();
  e.stopPropagation(); // not for the bash underneath
  cancelDrop();
}, true);

// dropSay paints the veil: progress is a fraction, true for "busy, no
// measure", or nothing for no bar.
function dropSay(text, progress) {
  dropVeil.hidden = false;
  put($('p', dropVeil), text);
  const bar = $('progress', dropVeil);
  bar.hidden = progress == null;
  if (progress === true) bar.removeAttribute('value');
  else if (progress != null) bar.value = progress;
}

// dropped reads what was dropped. The entries must be taken synchronously, in
// the drop event; walking them can wait. Each top-level item comes back on its
// own: a file, or a folder with its files (paths relative to the folder) and
// its empty subfolders.
function dropped(dt) {
  const entries = [...dt.items].map(i => i.webkitGetAsEntry?.()).filter(Boolean);
  const flat = [...dt.files];
  return async () => {
    if (!entries.length) return flat.map(file => ({ name: file.name, file }));
    const readAll = reader => new Promise((ok, ko) => {
      const out = [];
      const next = () => reader.readEntries(batch => (batch.length ? (out.push(...batch), next()) : ok(out)), ko);
      next();
    });
    const asFile = entry => new Promise((ok, ko) => entry.file(ok, ko));
    const items = [];
    for (const top of entries) {
      if (top.isFile) { items.push({ name: top.name, file: await asFile(top) }); continue; }
      const files = [], dirs = [];
      const walk = async (entry, rel) => {
        if (entry.isFile) return files.push({ path: rel, file: await asFile(entry) });
        const kids = await readAll(entry.createReader());
        if (!kids.length && rel) dirs.push(rel);
        for (const k of kids) await walk(k, rel ? `${rel}/${k.name}` : k.name);
      };
      await walk(top, '');
      items.push({ name: top.name, files, dirs });
    }
    return items;
  };
}

// ---- a zip made in the browser: native deflate (CompressionStream), CRC32,
// hand-written headers. No zip64: under 4 GB and 65,535 entries.
const CRC = Uint32Array.from({ length: 256 }, (_, n) => {
  for (let k = 0; k < 8; k++) n = n & 1 ? 0xedb88320 ^ (n >>> 1) : n >>> 1;
  return n >>> 0;
});
const crc32 = (buf, crc) => {
  for (let i = 0; i < buf.length; i++) crc = CRC[(crc ^ buf[i]) & 0xff] ^ (crc >>> 8);
  return crc;
};
// Already compressed: stored as is, deflating them again only costs time.
const PACKED = /\.(zip|gz|tgz|bz2|xz|zst|7z|rar|jar|apk|png|jpe?g|gif|webp|avif|heic|mp[34]|m4[av]|mov|mkv|webm|ogg|opus|flac|woff2?|pdf|docx|xlsx|pptx|odt)$/i;

async function zipFolder(files, dirs, progress, signal) {
  const parts = [], central = [];
  const total = files.reduce((t, f) => t + f.file.size, 0) || 1;
  let offset = 0, done = 0;
  const enc8 = new TextEncoder();
  const header = (size, fill) => { const b = new Uint8Array(size); fill(new DataView(b.buffer)); return b; };
  const dosTime = d => [(d.getHours() << 11) | (d.getMinutes() << 5) | (d.getSeconds() >> 1),
    ((d.getFullYear() - 1980) << 9) | ((d.getMonth() + 1) << 5) | d.getDate()];
  const add = async (path, file) => {
    const name = enc8.encode(path);
    const [time, date] = dosTime(file ? new Date(file.lastModified) : new Date());
    const deflate = !!file && file.size > 0 && 'CompressionStream' in window && !PACKED.test(path);
    const method = deflate ? 8 : 0;
    const local = header(30 + name.length, v => {
      v.setUint32(0, 0x04034b50, true); v.setUint16(4, 20, true); v.setUint16(6, 0x0808, true); // data descriptor, UTF-8
      v.setUint16(8, method, true); v.setUint16(10, time, true); v.setUint16(12, date, true);
      v.setUint16(26, name.length, true);
    });
    local.set(name, 30);
    parts.push(local);
    let crc = 0xffffffff, csize = 0;
    if (file) {
      const reader = file.stream().getReader();
      let writer, drain;
      if (deflate) {
        const cs = new CompressionStream('deflate-raw');
        writer = cs.writable.getWriter();
        const out = cs.readable.getReader();
        drain = (async () => {
          for (;;) {
            const { done: end, value } = await out.read();
            if (end) return;
            parts.push(value);
            csize += value.length;
          }
        })();
      }
      for (;;) {
        if (signal?.aborted) { reader.cancel(); writer?.abort(); throw new Error(t('drop.cancelled')); }
        const { done: end, value } = await reader.read();
        if (end) break;
        crc = crc32(value, crc);
        if (writer) await writer.write(value);
        done += value.length;
        progress(done / total);
      }
      if (writer) { await writer.close(); await drain; } else { parts.push(file); csize = file.size; }
    }
    crc = (crc ^ 0xffffffff) >>> 0;
    const usize = file ? file.size : 0;
    if (csize >= 0xffffffff || usize >= 0xffffffff) throw new Error(t('drop.too_big_file', { path }));
    parts.push(header(16, v => {
      v.setUint32(0, 0x08074b50, true); v.setUint32(4, crc, true); v.setUint32(8, csize, true); v.setUint32(12, usize, true);
    }));
    const entry = header(46 + name.length, v => {
      v.setUint32(0, 0x02014b50, true); v.setUint16(4, 0x0314, true); v.setUint16(6, 20, true); v.setUint16(8, 0x0808, true);
      v.setUint16(10, method, true); v.setUint16(12, time, true); v.setUint16(14, date, true);
      v.setUint32(16, crc, true); v.setUint32(20, csize, true); v.setUint32(24, usize, true);
      v.setUint16(28, name.length, true);
      v.setUint32(38, (file ? 0o100644 : 0o040755) << 16, true); // unix mode
      v.setUint32(42, offset, true);
    });
    entry.set(name, 46);
    central.push(entry);
    offset += local.length + csize + 16;
    if (offset >= 0xffffffff) throw new Error(t('drop.too_big_dir'));
  };
  if (files.length + dirs.length >= 0xffff) throw new Error(t('drop.too_many'));
  for (const d of dirs) await add(`${d}/`, null);
  for (const f of files) await add(f.path, f.file);
  const size = central.reduce((t, c) => t + c.length, 0);
  const end = header(22, v => {
    v.setUint32(0, 0x06054b50, true); v.setUint16(8, central.length, true); v.setUint16(10, central.length, true);
    v.setUint32(12, size, true); v.setUint32(16, offset, true);
  });
  return new Blob([...parts, ...central, end], { type: 'application/zip' });
}

// upload goes through XHR for the progress bar (fetch cannot report uploads).
// sent fires once every byte has left: the server is then writing or unzipping.
function upload(url, form, progress, sent, signal) {
  return new Promise((ok, ko) => {
    const x = new XMLHttpRequest();
    x.open('POST', url);
    x.upload.onprogress = e => { if (e.lengthComputable) progress(e.loaded / e.total); };
    x.upload.onload = sent;
    x.onload = () => {
      let d = {};
      try { d = JSON.parse(x.responseText); } catch {}
      if (x.status === 401) askLogin();
      if (x.status === 413) return ko(new Error(t('drop.proxy_413')));
      x.status < 300 ? ok(d) : ko(new Error(d.error || t('ui.http_error', { code: x.status })));
    };
    x.onerror = () => ko(new Error(t('drop.broken')));
    x.onabort = () => ko(new Error(t('drop.cancelled')));
    signal.addEventListener('abort', () => x.abort());
    x.send(form);
  });
}

const sizeOf = b => b < 1024 ? t('size.b', { n: b }) : b < 1 << 20 ? t('size.k', { n: (b / 1024).toFixed(0) })
  : b < 1 << 30 ? t('size.m', { n: (b / (1 << 20)).toFixed(1) }) : t('size.g', { n: (b / (1 << 30)).toFixed(2) });

const shellBox = $('#shell');
shellBox.addEventListener('dragenter', e => {
  if (!hasFiles(e) || dropping) return;
  e.preventDefault();
  if (dragDepth++) return;
  dropSay(t('drop.where'));
  api(path(cur, '/sh/check'), { paths: [] })
    .then(({ dir }) => { if (dragDepth) dropSay(t('drop.into', { dir })); })
    .catch(err => { if (dragDepth) dropSay(err.message); });
});
shellBox.addEventListener('dragover', e => {
  if (!hasFiles(e)) return;
  e.preventDefault();
  e.dataTransfer.dropEffect = dropping ? 'none' : 'copy';
});
shellBox.addEventListener('dragleave', e => {
  if (!hasFiles(e) || dropping) return;
  if (--dragDepth <= 0) { dragDepth = 0; dropVeil.hidden = true; }
});
shellBox.addEventListener('drop', guard(async e => {
  if (!hasFiles(e)) return;
  e.preventDefault();
  dragDepth = 0;
  if (dropping) return;
  // Alt + drop: raw, no zip, no unpacking.
  const key = cur, raw = e.altKey, read = dropped(e.dataTransfer);
  const ctl = dropping = new AbortController(), signal = ctl.signal;
  const stop = () => { if (signal.aborted) throw new Error(t('drop.cancelled')); };
  $('.drop-close', dropVeil).hidden = false;
  let watch;
  try {
    dropSay(t('drop.reading'));
    const items = await read();
    // What will be sent: plain files, and zips to unpack (dropped .zip, or a
    // folder zipped here). In raw mode a folder goes file by file.
    const send = [], dirs = [], unzip = [], check = [];
    let total = 0, count = 0;
    for (const it of items) {
      if (it.file) {
        send.push({ path: it.name, file: it.file });
        if (!raw && /\.zip$/i.test(it.name)) { unzip.push(it.name); check.push(it.name.replace(/\.zip$/i, '')); } else check.push(it.name);
        total += it.file.size; count++;
      } else if (raw) {
        for (const f of it.files) { send.push({ path: `${it.name}/${f.path}`, file: f.file }); check.push(`${it.name}/${f.path}`); total += f.file.size; }
        count += it.files.length;
        dirs.push(...it.dirs.map(d => `${it.name}/${d}`), ...(it.files.length || it.dirs.length ? [] : [it.name]));
      } else {
        it.size = it.files.reduce((t, f) => t + f.file.size, 0);
        total += it.size; count += it.files.length;
        check.push(it.name);
      }
    }
    if (!count && !dirs.length && !items.some(it => it.files)) throw new Error(t('drop.nothing'));

    stop();
    const { dir, exists } = await api(path(key, '/sh/check'), { paths: check });
    stop();
    let overwrite = false;
    if (exists.length) {
      const list = exists.slice(0, 6).join('\n') + (exists.length > 6 ? '\n' + t('drop.more', { n: exists.length - 6 }) : '');
      if (!confirm(t('drop.confirm', { dir, list }))) throw new Error(t('drop.declined'));
      overwrite = true;
    }
    const what = `${tn('n.files', count)} · ${sizeOf(total)}`;
    if (!raw) {
      for (const it of items.filter(it => it.files)) {
        const blob = await zipFolder(it.files, it.dirs, p => dropSay(t('drop.zipping', { name: it.name, pct: Math.round(p * 100) }), p), signal);
        send.push({ path: `${it.name}.zip`, file: blob });
        unzip.push(`${it.name}.zip`);
      }
    }
    const sent = send.reduce((t, f) => t + f.file.size, 0);
    const form = new FormData();
    form.append('manifest', JSON.stringify({ files: send.map(f => f.path), dirs, unzip }));
    for (const f of send) form.append('file', f.file);
    const how = sent < total ? t('drop.packed', { size: sizeOf(sent) }) : '';
    stop();
    let label = t('drop.sending', { dir, what, how }), moved = Date.now();
    dropSay(label, 0);
    // Say so when nothing moves any more, rather than look frozen.
    watch = setInterval(() => {
      const idle = Math.round((Date.now() - moved) / 1000);
      if (idle >= 20) dropSay(t('drop.stalled', { label, s: idle }));
    }, 5000);
    const q = `?dir=${encodeURIComponent(dir)}${overwrite ? '&overwrite=1' : ''}`;
    await upload(path(key, '/sh/upload') + q, form,
      p => { moved = Date.now(); dropSay(`${label} · ${Math.round(p * 100)} %`, p); },
      () => { moved = Date.now(); label = t(unzip.length ? 'drop.received_unzip' : 'drop.received', { what, dir }); dropSay(label, true); },
      signal);
    toast(t('drop.done', { what, dir }));
  } finally {
    clearInterval(watch);
    if (dropping === ctl) dropping = null;
    $('.drop-close', dropVeil).hidden = true;
    dropVeil.hidden = true;
  }
}));
// A file dropped anywhere else must not make the browser leave the wall.
for (const t of ['dragover', 'drop']) document.addEventListener(t, e => { if (hasFiles(e)) e.preventDefault(); });

async function start() {
  await api(path(cur, '/start'), {});
  attach();
}

function paintLab() {
  const l = byKey(cur);
  if (!l) { if (labs.length || world.hub) location.hash = ''; return; }
  put($('#lab-name'), l.name);
  put($('#lab-where'), `${l.m} · ${l.dir}${l.cmd ? ` · ${l.cmd}` : ''}`);
  $('#lab-state').className = `pill ${l.state}`;
  put($('#lab-state'), `${label(l)} · ${ago(l.since)}`);

  const rail = $('#rail');
  const want = [];
  let machine = null;
  for (const o of labs) {
    if (world.machines.length > 1 && o.m !== machine) want.push({ sep: machine = o.m });
    want.push(o);
  }
  while (rail.children.length > want.length) rail.lastChild.remove();
  want.forEach((o, i) => {
    const tag = o.sep ? 'P' : 'A';
    let n = rail.children[i];
    if (!n || n.tagName !== tag) {
      const fresh = el(tag.toLowerCase());
      n ? n.replaceWith(fresh) : rail.append(fresh);
      n = fresh;
    }
    if (o.sep) { n.className = 'sep'; put(n, o.sep); return; }
    n.className = `mini ${o.state}${o.key === cur ? ' here' : ''}${o.key === dragged ? ' dragging' : ''}`;
    n.dataset.key = o.key;
    n.draggable = true;
    if (n.getAttribute('href') !== '#' + o.key) n.setAttribute('href', '#' + o.key);
    n.title = `${o.name} — ${label(o)}`;
    put(n, o.name);
  });
}

// Drag a lab up or down the rail to reorder; it moves live, within its machine.
let dragged = null;
const rail = $('#rail');
function moveLab(from, to, after) {
  const now = labs.map(l => l.key);
  const keys = now.filter(k => k !== from);
  keys.splice(keys.indexOf(to) + (after ? 1 : 0), 0, from);
  if (keys.join('\n') === now.join('\n')) return;
  order = [...keys, ...order.filter(k => !byKey(k))]; // remember labs of offline machines too
  store.set('order', order);
  refresh();
}
rail.addEventListener('dragstart', e => {
  const a = e.target.closest?.('.mini');
  if (!a) return;
  dragged = a.dataset.key;
  e.dataTransfer.effectAllowed = 'move';
  e.dataTransfer.setData('text/plain', a.textContent);
  paintLab();
});
rail.addEventListener('dragover', e => {
  const a = e.target.closest?.('.mini');
  if (!dragged || !a) return;
  e.preventDefault();
  e.dataTransfer.dropEffect = 'move';
  if (a.dataset.key === dragged || byKey(a.dataset.key)?.m !== byKey(dragged)?.m) return;
  const r = a.getBoundingClientRect();
  const across = getComputedStyle(rail).flexDirection === 'row';
  moveLab(dragged, a.dataset.key, across ? e.clientX > r.left + r.width / 2 : e.clientY > r.top + r.height / 2);
});
rail.addEventListener('drop', e => e.preventDefault());
rail.addEventListener('dragend', () => { dragged = null; paintLab(); });

$('#lab-start').addEventListener('click', guard(async () => {
  const l = byKey(cur);
  if (l.state !== 'eteint' && !confirm(t('ui.confirm_start', { name: l.name }))) return;
  await start();
}));
$('#lab-stop').addEventListener('click', guard(async () => {
  const l = byKey(cur);
  if (!confirm(t('ui.confirm_stop', { name: l.name }))) return;
  await api(path(cur, '/stop'), {});
}));
$('#lab-delete').addEventListener('click', guard(async () => {
  const l = byKey(cur);
  if (!confirm(t('ui.confirm_delete', { name: l.name }))) return;
  await api(path(cur), null, 'DELETE');
  location.hash = '';
}));

$('#keys').addEventListener('click', e => {
  const seq = e.target.dataset?.seq;
  if (seq) type(SEQ[seq]);
});
$('#keys').addEventListener('submit', e => {
  e.preventDefault();
  const input = $('#keys-text');
  if (input.value) type(input.value);
  input.value = '';
  setTimeout(() => type('\r'), 150); // let TUIs tell typing from pasting
});

// Ctrl+Tab / Ctrl+Shift+Tab (or Alt+PageDown / PageUp): next / previous lab ·
// Alt+Q: next question · Alt+M: the wall
function hop(list, step) {
  if (!list.length) return;
  const i = list.findIndex(l => l.key === cur);
  const j = i < 0 ? (step > 0 ? 0 : list.length - 1) : (i + step + list.length) % list.length;
  location.hash = list[j].key;
}
function shortcut(e) {
  if (e.type !== 'keydown' || e.metaKey) return false;
  const k = e.key.toLowerCase();
  if (e.ctrlKey && !e.altKey && k === 'tab') hop(labs, e.shiftKey ? -1 : 1);
  else if (!e.altKey || e.ctrlKey) return false;
  else if (k === 'pagedown' || k === 'pageup') hop(labs, k === 'pagedown' ? 1 : -1);
  else if (k === 'q') hop(labs.filter(l => l.state === 'question' || l.key === cur), 1);
  else if (k === 'm') location.hash = '';
  else return false;
  e.preventDefault();
  return true;
}
document.addEventListener('keydown', e => { if (!e.target.closest?.('.xterm')) shortcut(e); });

function route() {
  const key = decodeURIComponent(location.hash.slice(1));
  cur = key.includes('/') ? key : null;
  $('#wall').hidden = !!cur;
  $('#lab').hidden = !cur;
  document.body.classList.toggle('inlab', !!cur);
  if (cur) {
    ensureTerm();
    paintLab();
    attach();
    attachShell();
  } else {
    detach();
    detachShell();
  }
  paintBroadcast();
}
window.addEventListener('hashchange', route);

// A hidden tab lets go of its terminal after a minute, so that the hub knows
// nobody is watching and goes back to notifying.
let away;
document.addEventListener('visibilitychange', () => {
  clearTimeout(away);
  if (document.hidden) away = setTimeout(() => { detach(); detachShell(); }, 60000);
  else {
    if (cur && !sock && $('#veil').hidden) attach();
    if (shellOn() && !shSock && $('#shell-veil').hidden) attachShell();
    inbox();
  }
});
setInterval(() => { if (!document.hidden) refresh(); }, 15000); // keeps the ages fresh

// ---------------------------------------------------------------- new lab

const dlgNew = $('#dlg-new');
// Suggested commands, always offered; the ones typed recently come first.
const PRESETS = { crush: 'Crush', claude: 'Claude Code', codex: 'OpenAI Codex', bash: t('preset.bash') };
const commands = () => [...new Set([...store.get('cmds', []), ...Object.keys(PRESETS)])];
$('#btn-new').addEventListener('click', () => {
  const online = world.machines.filter(m => m.online);
  $('#new-machine').replaceChildren(...online.map(m => new Option(m.name, m.name, false, m.name === store.get('machine'))));
  $('#new-machine').parentElement.hidden = online.length < 2;
  $('#new-cmds').replaceChildren(...commands().map(c => {
    const o = new Option();
    o.value = c;
    if (PRESETS[c]) o.label = PRESETS[c];
    return o;
  }));
  $('#new-dir').value = store.get('dir', '');
  $('#new-cmd').value = commands()[0];
  $('#new-name').value = '';
  $('#new-error').hidden = true;
  dlgNew.showModal();
  $('#new-dir').focus();
});
let dirTimer;
$('#new-dir').addEventListener('input', e => {
  clearTimeout(dirTimer);
  dirTimer = setTimeout(async () => {
    const m = encodeURIComponent($('#new-machine').value);
    const dirs = await api(`/m/${m}/api/dirs?p=${encodeURIComponent(e.target.value)}`).catch(() => []);
    $('#new-dirs').replaceChildren(...dirs.map(d => new Option(d)));
  }, 150);
});
$('#form-new').addEventListener('submit', async e => {
  e.preventDefault();
  const m = $('#new-machine').value, dir = $('#new-dir').value.trim(), cmd = $('#new-cmd').value.trim();
  try {
    const l = await api(`/m/${encodeURIComponent(m)}/api/labs`, { name: $('#new-name').value, dir, cmd });
    store.set('machine', m);
    store.set('dir', dir.replace(/[^/]+\/?$/, ''));
    if (cmd) store.set('cmds', [cmd, ...store.get('cmds', []).filter(c => c !== cmd)].slice(0, 8));
    dlgNew.close();
    location.hash = `${m}/${l.id}`;
  } catch (err) {
    $('#new-error').textContent = err.message;
    $('#new-error').hidden = false;
  }
});
for (const b of document.querySelectorAll('[data-close]')) b.addEventListener('click', () => b.closest('dialog').close());

// ---------------------------------------------------------------- journal

const VERB = Object.fromEntries(['question', 'pose', 'eteint'].map(s => [s, t(`journal.${s}`)]));
// What the operator did (journaled by the hub): never counted as news.
const ACT = Object.fromEntries(['create', 'start', 'stop', 'delete', 'keys', 'upload'].map(a => [a, t(`journal.act_${a}`)]));

// inbox counts what happened since the journal was last opened.
async function inbox() {
  const events = (await api('/api/journal').catch(() => null)) || []; // null while the journal is empty
  const fresh = events.filter(ev => !ev.act && ev.t > store.get('seen', 0)).length;
  put($('#btn-journal'), fresh ? `${t('html.journal')} (${fresh})` : t('html.journal'));
  $('#btn-journal').classList.toggle('on', fresh > 0);
  return events;
}
function renderJournal(events, seen) {
  const mine = store.get('journal.mine', true);
  $('#journal-mine').checked = mine;
  const today = new Date().toDateString();
  const list = $('#journal');
  const shown = events.filter(ev => mine || !ev.act).reverse();
  list.replaceChildren(...shown.map(ev => {
    const li = el('li', ev.act ? 'act' : ev.to);
    const d = new Date(ev.t * 1000);
    const hm = d.toLocaleTimeString(document.documentElement.lang, { hour: '2-digit', minute: '2-digit' });
    const by = ev.by ? ' · ' + t(`journal.by_${ev.by}`) : '';
    const when = el('span', 'when', (d.toDateString() === today ? '' : d.toLocaleDateString(document.documentElement.lang, { day: 'numeric', month: 'short' }) + ' ') + hm + ' · ' + ev.m + by);
    const head = el('div');
    head.append(el('b', '', ev.name), ` ${ev.act ? (ACT[ev.act] || ev.act) : (VERB[ev.to] || ev.to)} `, when);
    if (!ev.act && ev.t > seen) head.append(el('span', 'new', t('journal.new')));
    li.append(head);
    if (ev.text) li.append(el('pre', '', ev.text));
    li.addEventListener('click', () => { $('#dlg-journal').close(); location.hash = `${ev.m}/${ev.id}`; });
    return li;
  }));
  if (!shown.length) list.append(el('li', '', t('journal.empty')));
}
let journalEvents = [], journalSeen = 0;
$('#journal-mine').addEventListener('change', e => {
  store.set('journal.mine', e.target.checked);
  renderJournal(journalEvents, journalSeen);
});
$('#btn-journal').addEventListener('click', guard(async () => {
  journalEvents = await inbox();
  journalSeen = store.get('seen', 0);
  renderJournal(journalEvents, journalSeen);
  store.set('seen', Math.floor(Date.now() / 1000));
  put($('#btn-journal'), t('html.journal'));
  $('#btn-journal').classList.remove('on');
  $('#dlg-journal').showModal();
}));

// ---------------------------------------------------------------- push

async function pushInit() {
  if (!('serviceWorker' in navigator)) return;
  const reg = await navigator.serviceWorker.register('/sw.js');
  if (!('PushManager' in window)) return;
  const btn = $('#btn-push');
  btn.hidden = false;
  const paint = on => {
    btn.textContent = t(on ? 'push.on' : 'push.off');
    btn.title = t(on ? 'push.on_hint' : 'push.off_hint');
  };
  let sub = await reg.pushManager.getSubscription();
  paint(!!sub);
  if (sub) api('/api/push/sub', sub.toJSON()).catch(() => {});
  btn.addEventListener('click', guard(async () => {
    if (!sub) {
      if (await Notification.requestPermission() !== 'granted') throw new Error(t('push.denied'));
      const { key } = await api('/api/push/key');
      const raw = atob(key.replace(/-/g, '+').replace(/_/g, '/'));
      sub = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: Uint8Array.from(raw, c => c.charCodeAt(0)) });
      await api('/api/push/sub', sub.toJSON());
      paint(true);
    }
    await api('/api/push/test', {});
    toast(t('push.test_sent'));
  }));
}

listen();
inbox();
route();
pushInit().catch(() => {});
