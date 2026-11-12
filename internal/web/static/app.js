// snxw board. Plain JS, no dependencies, no build step.
//
// Data flow: one fetch-streamed SSE connection to /v1/stream?view=sessions
// (a snapshot, then small per-session patches at most 4/s each), plus the
// selected session's history from /v1/events. fetch rather than EventSource
// so the token travels in the Authorization header and never in a URL or a
// cookie. Everything an agent wrote is inserted as text, never as HTML.
"use strict";

const TOKEN_KEY = "snxw.token";
const ORDER = ["waiting_for_you", "running", "idle", "failed", "done"];
const LABEL = { waiting_for_you: "needs you", running: "running", idle: "idle", failed: "failed", done: "done" };
const PAGE = 200;

const state = {
  token: "",
  sessions: new Map(),
  selected: "",
  filter: "",
  hideDone: localStorage.getItem("snxw.hideDone") === "1",
  notify: false,
  live: false,
  hist: null, // { id, events: [], last: 0, first: 0, more: false, busy: false }
};

const $ = (id) => document.getElementById(id);

// el builds an element; text children become text nodes, so agent output
// can never become markup.
function el(tag, cls, ...kids) {
  const n = document.createElement(tag);
  if (cls) n.className = cls;
  for (const k of kids) {
    if (k == null || k === false) continue;
    n.append(typeof k === "string" || typeof k === "number" ? String(k) : k);
  }
  return n;
}

// ---- token -------------------------------------------------------------

function loadToken() {
  const m = /[#&]t=([0-9a-fA-F]+)/.exec(location.hash);
  if (m) {
    localStorage.setItem(TOKEN_KEY, m[1]);
    history.replaceState(null, "", location.pathname); // drop it from the URL bar
  }
  state.token = localStorage.getItem(TOKEN_KEY) || "";
}

function showGate(msg) {
  $("gate").hidden = false;
  $("gateerr").textContent = msg || "";
  $("tokeninput").focus();
}

function api(path) {
  return fetch(path, { headers: { Authorization: "Bearer " + state.token }, cache: "no-store" });
}

// ---- stream ------------------------------------------------------------

let backoff = 1000;

async function connect() {
  if (!state.token) return showGate();
  let res;
  try {
    res = await api("/v1/stream?view=sessions");
  } catch {
    return retry();
  }
  if (res.status === 401) {
    localStorage.removeItem(TOKEN_KEY);
    state.token = "";
    setLive(false);
    return showGate("That token was refused.");
  }
  if (!res.ok || !res.body) return retry();

  $("gate").hidden = true;
  setLive(true);
  backoff = 1000;

  const reader = res.body.pipeThrough(new TextDecoderStream()).getReader();
  let buf = "";
  try {
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      buf += value;
      let i;
      while ((i = buf.indexOf("\n\n")) >= 0) {
        handleMessage(buf.slice(0, i));
        buf = buf.slice(i + 2);
      }
    }
  } catch {
    // dropped: fall through to retry
  }
  retry();
}

function retry() {
  setLive(false);
  setTimeout(connect, backoff);
  backoff = Math.min(backoff * 2, 10000);
}

function setLive(on) {
  state.live = on;
  const c = $("conn");
  c.className = "conn " + (on ? "live" : "down");
  c.title = on ? "live" : "reconnecting…";
}

function handleMessage(block) {
  let event = "message";
  const data = [];
  for (const line of block.split("\n")) {
    if (line.startsWith(":")) continue; // heartbeat
    if (line.startsWith("event: ")) event = line.slice(7);
    else if (line.startsWith("data: ")) data.push(line.slice(6));
  }
  if (!data.length) return;
  let msg;
  try {
    msg = JSON.parse(data.join("\n"));
  } catch {
    return;
  }
  if (event === "snap") applySnap(msg);
  else if (event === "s") applyPatch(msg.id, msg.p || {});
}

function applySnap(msg) {
  const next = new Map();
  for (const s of msg.sessions || []) next.set(s.id, s);
  state.sessions = next;
  if (state.selected && !next.has(state.selected)) state.selected = "";
  if (!state.selected) selectFirst();
  else refreshHistory();
  render();
}

function applyPatch(id, p) {
  const prev = state.sessions.get(id);
  const s = Object.assign(prev ? { ...prev } : { id, status: "running", toolCalls: 0, errors: 0 }, p);
  state.sessions.set(id, s);
  if (p.status === "waiting_for_you" && (!prev || prev.status !== "waiting_for_you")) notifyWaiting(s);
  if (!state.selected) select(id);
  else if (id === state.selected) refreshHistory();
  render();
}

// ---- derived -----------------------------------------------------------

function base(p) {
  if (!p) return "";
  const parts = p.split(/[\\/]/).filter(Boolean);
  return parts[parts.length - 1] || p;
}

function nameOf(s) {
  return s.repo || s.title || base(s.cwd) || s.source + " " + s.id.slice(0, 8);
}

function ago(ms) {
  if (!ms) return "";
  const d = Math.max(0, (Date.now() - ms) / 1000);
  if (d < 60) return Math.floor(d) + "s";
  if (d < 3600) return Math.floor(d / 60) + "m";
  if (d < 86400) return Math.floor(d / 3600) + "h";
  return Math.floor(d / 86400) + "d";
}

function visible() {
  const f = state.filter.toLowerCase();
  const out = [];
  for (const s of state.sessions.values()) {
    if (state.hideDone && s.status === "done") continue;
    if (f) {
      const hay = [nameOf(s), s.branch, s.tool, s.status, LABEL[s.status], s.source, s.cwd, s.model]
        .filter(Boolean).join(" ").toLowerCase();
      if (!hay.includes(f)) continue;
    }
    out.push(s);
  }
  const rank = (s) => { const i = ORDER.indexOf(s.status); return i < 0 ? ORDER.length : i; };
  out.sort((a, b) => rank(a) - rank(b) || (b.updatedAt || 0) - (a.updatedAt || 0));
  return out;
}

// ---- render ------------------------------------------------------------

let frame = 0;
function render() {
  if (!frame) frame = requestAnimationFrame(() => { frame = 0; draw(); });
}

function draw() {
  drawSummary();
  drawList();
  drawDetail();
}

function drawSummary() {
  const n = {};
  for (const s of state.sessions.values()) n[s.status] = (n[s.status] || 0) + 1;
  const sum = $("summary");
  sum.replaceChildren();
  const add = (cls, k, label) => {
    if (n[k]) sum.append(el("span", cls, el("b", null, n[k]), " " + label));
  };
  add("w", "waiting_for_you", "need you");
  add("", "running", "running");
  add("", "idle", "idle");
  add("", "failed", "failed");
  const w = n.waiting_for_you || 0;
  document.title = (w ? "(" + w + ") " : "") + "snxw";
}

function drawList() {
  const list = $("list");
  const rows = visible();
  list.replaceChildren();
  if (!rows.length) {
    list.append(emptyState());
    return;
  }
  let group = "";
  for (const s of rows) {
    if (s.status !== group) {
      group = s.status;
      const count = rows.filter((r) => r.status === group).length;
      list.append(el("div", "group " + group, LABEL[group] || group, el("span", null, String(count))));
    }
    const line2 = el("div", "line2");
    if (s.tool) line2.append(el("span", "tool", "▸ " + s.tool), "  ");
    else line2.append((LABEL[s.status] || s.status) + "  ");
    if (s.branch) line2.append(" " + s.branch + " ·");
    line2.append(" " + (s.toolCalls || 0) + " calls");
    if (s.errors) line2.append(" · ", el("span", "err", s.errors + " err"));
    line2.append(" · " + s.source);

    const row = el("div", "row " + s.status + (s.id === state.selected ? " sel" : ""),
      el("span", "dot " + s.status),
      el("span", "name", nameOf(s)),
      el("span", "age", ago(s.updatedAt)),
      line2);
    row.dataset.id = s.id;
    row.setAttribute("role", "button");
    row.title = s.cwd || s.id;
    list.append(row);
  }
  const sel = list.querySelector(".row.sel");
  if (sel) sel.scrollIntoView({ block: "nearest" });
}

function emptyState() {
  if (state.sessions.size) {
    return el("div", "empty", el("h2", null, "Nothing matches"), "Clear the filter with Esc" + (state.hideDone ? ", or press h to show finished sessions." : "."));
  }
  return el("div", "empty",
    el("h2", null, "No agent sessions yet"),
    el("p", null, "Connect an agent, then start a session. It shows up here the moment it does something."),
    el("pre", null, "snxw connect claude"),
    el("p", null, "Anything else can post events directly:"),
    el("pre", null, "snxw emit tool.start '{\"tool\":\"build\"}'"));
}

function drawDetail() {
  const d = $("detail");
  const s = state.sessions.get(state.selected);
  if (!s) {
    d.replaceChildren(el("div", "placeholder", state.sessions.size ? "Select a session (j / k)." : ""));
    return;
  }
  const meta = el("div", "meta");
  const kv = (k, v, cls) => { if (v) meta.append(el("span", cls, k + " ", el("b", null, String(v)))); };
  kv("branch", s.branch);
  kv("cwd", s.cwd, "copy");
  kv("model", s.model);
  kv("calls", s.toolCalls);
  if (s.errors) kv("errors", s.errors);
  kv("started", s.startedAt ? ago(s.startedAt) + " ago" : "");
  kv("agent", s.source);
  const cwdNode = meta.querySelector(".copy");
  if (cwdNode) cwdNode.title = "click or press y to copy";

  const head = el("div", "dhead",
    el("h2", "dtitle", nameOf(s), el("span", "status " + s.status, LABEL[s.status] || s.status)),
    meta);

  const tl = el("ol", "tl");
  const h = state.hist && state.hist.id === s.id ? state.hist : null;
  if (h && h.more) {
    const b = el("button", "ghost more", "load older");
    b.type = "button";
    b.dataset.action = "older";
    tl.append(b);
  }
  if (h) for (const ev of h.events) { const li = eventRow(ev); if (li) tl.append(li); }

  const stick = d.scrollHeight - d.scrollTop - d.clientHeight < 40;
  d.replaceChildren(head, tl);
  if (stick) d.scrollTop = d.scrollHeight;
}

function hhmmss(ts) {
  const t = new Date(ts);
  return [t.getHours(), t.getMinutes(), t.getSeconds()].map((n) => String(n).padStart(2, "0")).join(":");
}

function eventRow(ev) {
  const b = ev.body || {};
  let cls = "", k = ev.kind, text = "";
  switch (ev.kind) {
    case "prompt.submit": cls = "prompt"; k = "prompt"; text = b.prompt || ""; break;
    case "tool.start": k = b.tool || "tool"; text = b.arg || ""; break;
    case "tool.end":
      if (b.ok !== false) return null; // success is the default; only failures earn a line
      cls = "fail"; k = "✗ " + (b.tool || "tool"); text = b.error || "failed"; break;
    case "attention": cls = "attention"; k = "needs you"; text = [b.title, b.message, b.tool && b.tool + " " + (b.arg || "")].filter(Boolean).join(" — "); break;
    case "turn.end": cls = "turn"; k = "turn end"; text = b.last || ""; break;
    case "session.start": k = "start"; text = [b.start, b.cwd].filter(Boolean).join(" "); break;
    case "session.end": k = "ended"; text = b.reason || ""; break;
    case "error": cls = "fail"; k = "error"; text = b.error || b.message || ""; break;
    case "subagent.start": k = "subagent"; text = "▸ " + (b.agent || ""); break;
    case "subagent.end": k = "subagent"; text = "✓ " + (b.agent || ""); break;
    default: text = JSON.stringify(b);
  }
  return el("li", "ev " + cls, el("span", "t", hhmmss(ev.ts)), el("span", "k", k), el("span", "b", text));
}

// ---- history -----------------------------------------------------------
//
// One hist object per selected session. Newer events (refresh) and older
// pages (loadOlder) have separate in-flight flags because they never overlap
// in range; a refresh asked for while one is running is remembered in
// h.again and run when it finishes, so no patch is ever lost. A failed
// request just leaves the flags clear: the next patch or snapshot retries.

function newHist(id) {
  return { id, events: [], last: 0, first: 0, more: false, busy: false, again: false, olderBusy: false };
}

function loadHistory(id) {
  state.hist = newHist(id);
  refreshHistory();
}

async function getEvents(h, query) {
  try {
    const res = await api("/v1/events?session=" + encodeURIComponent(h.id) + query);
    if (!res.ok) return null;
    return (await res.json()).events || [];
  } catch {
    return null;
  }
}

async function refreshHistory() {
  const h = state.hist;
  if (!h || h.id !== state.selected) return;
  if (h.busy) { h.again = true; return; }
  h.busy = true;
  try {
    const first = !h.last;
    // First load: the newest page, newest first. Afterwards: everything newer.
    const evs = await getEvents(h, first ? "&limit=" + PAGE : "&after=" + h.last + "&limit=1000");
    if (!evs || state.hist !== h) return;
    if (first) {
      evs.reverse();
      h.more = evs.length === PAGE;
    }
    const fresh = evs.filter((e) => e.seq > h.last);
    if (fresh.length) {
      h.events.push(...fresh);
      h.last = fresh[fresh.length - 1].seq;
      if (!h.first) h.first = fresh[0].seq;
      if (first) $("detail").scrollTop = 1e9;
      render();
    }
  } finally {
    h.busy = false;
    if (h.again && state.hist === h) { h.again = false; refreshHistory(); }
  }
}

async function loadOlder() {
  const h = state.hist;
  if (!h || !h.first || h.olderBusy) return;
  h.olderBusy = true;
  try {
    const d = $("detail");
    const evs = await getEvents(h, "&before=" + h.first + "&limit=" + PAGE);
    if (!evs || state.hist !== h) return;
    const fromBottom = d.scrollHeight - d.scrollTop;
    evs.reverse();
    h.events.unshift(...evs);
    if (evs.length) h.first = evs[0].seq;
    h.more = evs.length === PAGE;
    draw();
    d.scrollTop = d.scrollHeight - fromBottom; // keep the reader's place
  } finally {
    h.olderBusy = false;
  }
}

// ---- selection & keys --------------------------------------------------

function select(id) {
  if (!id || id === state.selected) return;
  state.selected = id;
  $("detail").scrollTop = 0;
  document.querySelector(".main").classList.add("open");
  loadHistory(id);
  render();
}

function selectFirst() {
  const v = visible();
  if (v.length) select(v[0].id);
}

function move(delta) {
  const v = visible();
  if (!v.length) return;
  let i = v.findIndex((s) => s.id === state.selected);
  i = i < 0 ? 0 : Math.max(0, Math.min(v.length - 1, i + delta));
  select(v[i].id);
}

function nextWaiting() {
  const v = visible().filter((s) => s.status === "waiting_for_you");
  if (!v.length) return;
  const i = v.findIndex((s) => s.id === state.selected);
  select(v[(i + 1) % v.length].id);
}

function copyCwd() {
  const s = state.sessions.get(state.selected);
  if (s && s.cwd && navigator.clipboard) navigator.clipboard.writeText(s.cwd).catch(() => {});
}

async function toggleNotify() {
  if (!state.notify && "Notification" in window && Notification.permission !== "granted") {
    await Notification.requestPermission();
  }
  state.notify = !state.notify && "Notification" in window && Notification.permission === "granted";
  const b = $("notify");
  b.textContent = "notify: " + (state.notify ? "on" : "off");
  b.classList.toggle("on", state.notify);
}

function notifyWaiting(s) {
  if (!state.notify || document.hasFocus()) return;
  const n = new Notification(nameOf(s) + " needs you", { body: s.branch || s.cwd || "", tag: s.id });
  n.onclick = () => { window.focus(); select(s.id); n.close(); };
}

function toggleHelp(show) {
  $("help").hidden = show === undefined ? !$("help").hidden : !show;
}

document.addEventListener("keydown", (e) => {
  if (e.metaKey || e.ctrlKey || e.altKey) return;
  const inInput = e.target instanceof HTMLInputElement;
  if (e.key === "Escape") {
    toggleHelp(false);
    if (state.filter || inInput) {
      $("filter").value = "";
      state.filter = "";
      $("filter").blur();
      render();
    } else {
      document.querySelector(".main").classList.remove("open");
    }
    return;
  }
  if (inInput) {
    if (e.key === "Enter" || e.key === "ArrowDown") { $("filter").blur(); selectFirst(); }
    return;
  }
  const keys = {
    j: () => move(1), ArrowDown: () => move(1),
    k: () => move(-1), ArrowUp: () => move(-1),
    g: () => move(-1e9), G: () => move(1e9),
    w: nextWaiting,
    "/": () => $("filter").focus(),
    h: () => { state.hideDone = !state.hideDone; localStorage.setItem("snxw.hideDone", state.hideDone ? "1" : "0"); render(); },
    y: copyCwd,
    n: toggleNotify,
    "?": () => toggleHelp(),
  };
  const f = keys[e.key];
  if (f) { e.preventDefault(); f(); }
});

$("filter").addEventListener("input", (e) => { state.filter = e.target.value.trim(); render(); });
$("list").addEventListener("click", (e) => {
  const row = e.target.closest(".row");
  if (row) select(row.dataset.id);
});
$("detail").addEventListener("click", (e) => {
  if (e.target.closest("[data-action=older]")) loadOlder();
  else if (e.target.closest(".copy")) copyCwd();
});
$("notify").addEventListener("click", toggleNotify);
$("helpbtn").addEventListener("click", () => toggleHelp());
$("help").addEventListener("click", (e) => { if (e.target === $("help")) toggleHelp(false); });
$("gateform").addEventListener("submit", (e) => {
  e.preventDefault();
  const t = $("tokeninput").value.trim();
  if (!t) return;
  localStorage.setItem(TOKEN_KEY, t);
  state.token = t;
  $("tokeninput").value = "";
  connect();
});

setInterval(render, 5000); // keep the "3s / 4m" ages honest
loadToken();
setLive(false);
render();
connect();
