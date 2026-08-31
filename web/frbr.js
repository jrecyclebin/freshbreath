/**
 * Fresh Breath client SDK.
 *
 * One verb: `login(serviceURL)`. It resolves what the door asks for, spends
 * a credential the browser already holds when one fits, and prompts only
 * when nothing does. What comes back is a ServiceProxy you can call.
 *
 * What the browser holds is always a Fresh Breath token — an identity with
 * any upstream credentials sealed inside it, unreadable here and unsealed
 * by the proxy on the way out. Refresh tokens live in HttpOnly cookies and
 * the OAuth client_secret never leaves the server, so the worst a page can
 * read out of localStorage is a token that expires.
 */
import { McpClient, StreamableHTTPClientTransport } from "/control/vendor/frbr-deps-1.29.0.js";

const CFG = window.__HOMESLICE_CONFIG ?? {};
const API = CFG.apiBase ?? "";
const APP_NONCE = CFG.appNonce ?? null;
// The auth record guarding this page. 0 when the door is open — an app
// behind the Anonymous record, or an instance still in setup.
const GATE_ID = CFG.authRecordID ?? 0;

// The origin our callback page will post from. The page posts to "*"
// because it cannot know its opener's origin; the listener is the half
// that can be strict, so this is where strictness goes.
const FRBR_ORIGIN = new URL(API || window.location.href, window.location.href).origin;

// Peek at a JWT payload without verifying anything. Verification is the
// server's job — this is only ever used to read a claim we already trust
// the shape of.
function jwtPayload(raw) {
  if (!raw || typeof raw !== "string") return null;
  const parts = raw.split(".");
  if (parts.length !== 3) return null;
  try {
    return JSON.parse(atob(parts[1].replace(/-/g, "+").replace(/_/g, "/")));
  } catch { return null; }
}

// Whether a bearer was minted here. The twin of this check lives in
// services.go — keep them in step.
function isFreshbreathToken(raw) {
  return jwtPayload(raw)?.iss === "freshbreath";
}

function uuidv4() {
  if (crypto?.randomUUID) {
    return crypto.randomUUID();
  }
  const b = crypto.getRandomValues(new Uint8Array(16));
  b[6] = (b[6] & 0x0f) | 0x40; // version 4
  b[8] = (b[8] & 0x3f) | 0x80; // variant 10
  return [...b].map((x, i) => (i === 4 || i === 6 || i === 8 || i === 10 ? "-" : "") + x.toString(16).padStart(2, "0")).join("");
}

// ── The store ───────────────────────────────────────────────────────
//
// One entry per cleared auth record at localStorage["frbr:auth:<id>"].
// frbr.js is the sole writer; apps never touch these keys. Because apps
// share an origin they also share the store — deliberate, and the reason
// two apps behind one gate prompt once between them. Spending is still
// bounded by X-App-Nonce and the app's service links, not by who can read
// a key.

const STORE_PREFIX = "frbr:auth:";
const STORE_V = 1;
// Refresh this far ahead of expiry, so a request never races the clock.
const REFRESH_MARGIN_MS = 5 * 60_000;

const storeKey = (authID) => STORE_PREFIX + authID;

// Read an entry, live or lapsed — a lapsed token entry is still worth
// having, since the refresh cookie can revive it. An entry from a schema
// we don't recognize is discarded outright: re-logging in is cheap, and
// guessing at an unknown shape is not.
function readEntry(authID) {
  if (!authID) return null;
  let e = null;
  try { e = JSON.parse(localStorage.getItem(storeKey(authID)) || "null"); } catch {}
  if (!e || typeof e !== "object") return null;
  if (e.v !== STORE_V) { evictEntry(authID); return null; }
  return e;
}

function writeEntry(entry) {
  entry.written_at = new Date().toISOString();
  try { localStorage.setItem(storeKey(entry.auth_id), JSON.stringify(entry)); } catch {}
  return entry;
}

function evictEntry(authID) {
  try { localStorage.removeItem(storeKey(authID)); } catch {}
}

// expires_at is required, so it is always derived from something: the
// token response's expires_in, or failing that the token's own exp claim.
// A response offering neither yields an entry that is already expired —
// loud and self-correcting. The old client wrote null here instead, and
// expiry simply never fired again.
function expiryFrom(tokenResponse) {
  if (tokenResponse.expires_in) {
    return new Date(Date.now() + tokenResponse.expires_in * 1000).toISOString();
  }
  const exp = jwtPayload(tokenResponse.access_token)?.exp;
  return new Date(exp ? exp * 1000 : 0).toISOString();
}

// Thrown when a refresh is refused: the family is gone and only a fresh
// login will do. Callers catch this to decide when to offer one — frbr.js
// never opens a popup on its own, because a popup with no click behind it
// is a popup the browser blocks.
export class SessionExpired extends Error {
  constructor(authID) {
    super("Session expired — log in again");
    this.name = "SessionExpired";
    this.authID = authID;
  }
}

// ── AuthSession ─────────────────────────────────────────────────────
//
// One per auth record, held in a registry keyed by id. Two proxies behind
// the same gate get the same session: one credential, one refresh mutex,
// one storage slot. When a refresh lands, every proxy riding that session
// recovers together.

const sessions = new Map();

export class AuthSession {
  #entry;
  #refreshing = null;

  constructor(entry) { this.#entry = entry; }

  static for(entry) {
    const existing = sessions.get(entry.auth_id);
    if (existing) { existing.#entry = entry; return existing; }
    const session = new AuthSession(entry);
    sessions.set(entry.auth_id, session);
    return session;
  }

  static get(authID) { return sessions.get(authID) ?? null; }

  get authID()    { return this.#entry.auth_id; }
  get kind()      { return this.#entry.kind; }
  get provider()  { return this.#entry.provider ?? null; }
  get subject()   { return this.#entry.subject ?? null; }
  get legs()      { return this.#entry.legs ?? []; }
  get token()     { return this.#entry.access_token ?? null; }
  get user_name() { return jwtPayload(this.#entry.access_token)?.user_name; }
  get email()     { return jwtPayload(this.#entry.access_token)?.user_email; }
  get expiresAt() { return this.#entry.expires_at ? new Date(this.#entry.expires_at) : null; }

  // Whether this session already clears a given record — directly, or as
  // one of the legs sealed into a merged two-leg token.
  covers(authID) {
    return this.authID === authID || this.legs.includes(authID);
  }

  // Set whatever header this record's kind implies, and report whether the
  // credential is one a refresh could renew. A typed key is not.
  addAuth(headers) {
    const e = this.#entry;
    if (e.access_token) {
      headers.set("Authorization", `${e.token_type || "Bearer"} ${e.access_token}`);
      return true;
    }
    if (e.key) {
      if (e.header) headers.set(e.header, e.key);
      else headers.set("Authorization", `Bearer ${e.key}`);
    }
    return false;
  }

  // Live enough to spend? A key entry always is; a token entry is until it
  // comes within the refresh margin of its stated expiry.
  get live() {
    const e = this.#entry;
    if (!e.access_token) return !!e.key;
    const t = Date.parse(e.expires_at);
    return Number.isFinite(t) && t - Date.now() > REFRESH_MARGIN_MS;
  }

  async check() {
    if (!this.live) await this.refresh();
  }

  async refresh() {
    if (this.#refreshing) return this.#refreshing;
    this.#refreshing = this.#doRefresh();
    try { return await this.#refreshing; }
    finally { this.#refreshing = null; }
  }

  async #doRefresh() {
    const e = this.#entry;
    if (!e.access_token) return; // nothing to rotate

    // The refresh token rides an HttpOnly cookie scoped to this exact path,
    // which is why the route carries the record id: a browser holding
    // several records' refresh tokens keeps them in distinct cookie slots.
    const r = await fetch(`${API}/oauth/token/${e.auth_id}`, {
      method: "POST",
      headers: {
        "Content-Type": "application/x-www-form-urlencoded",
        "X-App-Nonce": APP_NONCE,
      },
      credentials: "include",
      body: new URLSearchParams({ grant_type: "refresh_token" }),
    });
    if (!r.ok) {
      if (r.status === 400 || r.status === 401) {
        // Refused: the refresh family is gone. Keeping the entry would only
        // let a dead token ride along on the next request.
        this.forget();
        throw new SessionExpired(e.auth_id);
      }
      throw new Error(`Token refresh failed (${r.status})`);
    }
    const t = await r.json();
    delete t.refresh_token; // browser flows ride the cookie; never store one
    this.#entry = writeEntry({
      ...e,
      access_token: t.access_token,
      token_type: t.token_type || e.token_type || "Bearer",
      expires_at: expiryFrom(t),
    });
  }

  // Drop this session and its stored entry. The server keeps no session to
  // end — the refresh family dies with the next attempt to use it.
  forget() {
    evictEntry(this.authID);
    sessions.delete(this.authID);
  }
}

// ── login ───────────────────────────────────────────────────────────

// Ask the door what a login would cost. A pure query: it starts no flow
// and leaves no state, which is what makes it safe to call before we know
// whether we can pay.
async function resolveDoor(serviceURL) {
  const q = new URLSearchParams({ resolve: "1" });
  if (serviceURL) q.set("url", serviceURL);
  const r = await fetch(`${API}/service/login?${q}`, {
    headers: { "X-App-Nonce": APP_NONCE },
  });
  if (!r.ok) {
    throw new Error(`Login resolution failed (${r.status}): ${await r.text().catch(() => "")}`);
  }
  return r.json();
}

// Commit to a login, presenting whatever we found. The server re-verifies
// it: a browser saying "I'm already logged in" is a claim, not a fact.
async function beginLogin(serviceURL, state, session) {
  const q = new URLSearchParams({ state });
  if (serviceURL) q.set("url", serviceURL);
  const headers = { "X-App-Nonce": APP_NONCE };
  if (session?.token) headers["Authorization"] = `Bearer ${session.token}`;
  const r = await fetch(`${API}/service/login?${q}`, { headers });
  if (!r.ok) {
    throw new Error(`Login failed (${r.status}): ${await r.text().catch(() => "")}`);
  }
  return r.json();
}

// The stored entry worth presenting is the one covering the most of what
// this door asks for. A two-leg login is stored under its outbound record
// with the inbound listed in `legs`, so that entry beats the gate-only one
// and skips the popup entirely.
async function candidateSession(legIDs) {
  let best = null, bestCount = 0;
  for (const id of legIDs) {
    const e = readEntry(id);
    if (!e) continue;
    const covered = legIDs.filter(l => l === e.auth_id || (e.legs || []).includes(l)).length;
    if (covered > bestCount) { best = e; bestCount = covered; }
  }
  if (!best) return null;
  const session = AuthSession.for(best);
  try { await session.check(); } catch { return null; }
  return session;
}

function popupLogin(url, state) {
  return new Promise((resolve, reject) => {
    const popup = window.open(url, "frbrAuth", "width=520,height=720");
    if (!popup) { reject(new Error("The login window was blocked")); return; }

    const finish = (fn, arg) => {
      window.removeEventListener("message", onMessage);
      clearInterval(watch);
      popup.close();
      fn(arg);
    };
    const onMessage = (e) => {
      // Origin first, then correlation. Correlating on state alone let any
      // frame that guessed it hand us a credential.
      if (e.origin !== FRBR_ORIGIN) return;
      const msg = e.data;
      if (!msg || msg.type !== "auth-complete" || msg.state !== state) return;
      if (!msg.entry?.auth_id) {
        finish(reject, new Error("Login finished without a credential"));
        return;
      }
      finish(resolve, msg.entry);
    };
    window.addEventListener("message", onMessage);
    const watch = setInterval(() => {
      if (popup.closed) finish(reject, new Error("Login window closed"));
    }, 500);
  });
}

/**
 * Log in to a service and get back a proxy for it.
 *
 * With no argument it clears this page's own gate and returns nothing —
 * the "sign in" verb for a gated app. Either way the app's gate is part of
 * the bill, so logging in to a service signs you in to the app too.
 *
 * A door that asks for nothing resolves instantly. A door already covered
 * by the store resolves with no popup. Only a genuinely missing credential
 * opens a window, so call this from a click.
 *
 * @param {string} [serviceURL] — a registered service URL, or omitted for
 *                                the app's own gate
 * @returns {Promise<ServiceProxy|AuthSession|null>}
 */
export async function login(serviceURL) {
  const door = await resolveDoor(serviceURL);
  const proxyFor = (service, session) =>
    serviceURL ? new ServiceProxy({ serviceURL, service, session }) : session;

  if (door.type === "anonymous") return proxyFor(door.service, null);

  const legIDs = (door.legs || []).map(l => l.auth_id);
  let session = await candidateSession(legIDs);

  const state = uuidv4();
  const d = await beginLogin(serviceURL, state, session);
  const service = d.service ?? door.service;

  if (d.type === "anonymous") return proxyFor(service, null);
  if (d.type === "ok") return proxyFor(service, session);

  session = AuthSession.for(writeEntry(await popupLogin(d.url, state)));
  return proxyFor(service, session);
}

/**
 * The session for this page's own gate, if the store already holds one.
 * Never prompts and never touches the network — this is the "am I signed
 * in?" question, asked at boot, not the verb that signs you in.
 */
export function currentSession() {
  if (!GATE_ID) return null;
  const e = readEntry(GATE_ID);
  return e ? AuthSession.for(e) : null;
}

/** Forget a cleared record — this page's gate unless told otherwise. */
export function signOut(authID = GATE_ID) {
  if (!authID) return;
  (AuthSession.get(authID) ?? new AuthSession({ auth_id: authID })).forget();
}

// ── ServiceProxy ────────────────────────────────────────────────────

export class ServiceProxy {
  #serviceURL;
  #serviceID;
  #proxied;
  #session;
  #client = null;

  /**
   * @param {Object} opts
   * @param {string} opts.serviceURL — the URL login was asked for
   * @param {Object} [opts.service]  — {id, url, proxied} from the server
   * @param {AuthSession} [opts.session] — null for an anonymous door
   */
  constructor({ serviceURL, service, session }) {
    this.#serviceURL = service?.url || serviceURL;
    if (!this.#serviceURL) throw new Error("ServiceProxy requires a service URL");
    this.#serviceID = service?.id ?? null;
    this.#proxied = !!service?.proxied;
    this.#session = session ?? null;
  }

  get serviceURL() { return this.#serviceURL; }
  get serviceID()  { return this.#serviceID; }
  get session()    { return this.#session; }

  // The slug for task and virtual services, which answer at /service/call
  // rather than over MCP transport; null for everything else.
  #serviceSlug() {
    if (this.#serviceURL?.startsWith("tasks://")) return this.#serviceURL.slice("tasks://".length);
    if (this.#serviceURL?.startsWith("/mcp/")) return this.#serviceURL.slice("/mcp/".length);
    return null;
  }

  // The single choke point: every request out of this proxy is stamped
  // with the app nonce and carries the session's credential. One 401 buys
  // one refresh and one retry — after that the caller hears about it.
  async #fetch(url, init = {}) {
    const headers = new Headers(init.headers);
    headers.set("X-App-Nonce", APP_NONCE);
    const renewable = this.#session ? this.#session.addAuth(headers) : false;
    let res = await fetch(url, { ...init, headers });
    if (res.status === 401 && renewable) {
      await this.#session.refresh();
      this.#session.addAuth(headers);
      res = await fetch(url, { ...init, headers });
    }
    return res;
  }

  async #connect() {
    const headers = new Headers();
    if (this.#session) {
      await this.#session.check();
      this.#session.addAuth(headers);
    }
    const transport = new StreamableHTTPClientTransport(new URL(this.#serviceURL, window.location.href), {
      requestInit: { headers },
    });
    this.#client = new McpClient({ name: "mcp-client", version: "1.0.0" });
    await this.#client.connect(transport);
  }

  async #withReconnect(fn) {
    try {
      if (!this.#client) await this.#connect();
      return await fn();
    } catch (e) {
      if (!String(e).includes("401")) throw e;
      await this.#session?.refresh();
      await this.#connect();
      return await fn();
    }
  }

  async listTools() {
    if (this.#serviceSlug()) {
      const r = await this.#fetch(`${API}/service/call/${this.#serviceSlug()}`);
      if (!r.ok) throw new Error(`listTools failed (${r.status})`);
      const { tools } = await r.json();
      return tools;
    }
    return this.#withReconnect(async () => {
      const { tools } = await this.#client.listTools();
      return tools;
    });
  }

  async callTool(name, args = {}) {
    const result = this.#serviceSlug()
      ? await this.#callTask(name, args)
      : await this.#withReconnect(() => this.#client.callTool({ name, arguments: args }));
    const text = result.content
      .filter(c => c.type === "text")
      .map(c => c.text)
      .join("\n");
    if (result.isError) throw new Error(`Tool error: ${text}`);
    try {
      return JSON.parse(text);
    } catch {
      return text;
    }
  }

  /**
   * Call a task or virtual tool. If any argument is a File or Blob the
   * request goes out as multipart, with everything else JSON-encoded into
   * its own field.
   */
  async #callTask(name, args) {
    const url = `${API}/service/call/${this.#serviceSlug()}`;
    const hasFiles = Object.values(args).some(v => v instanceof File || v instanceof Blob);

    let init;
    if (hasFiles) {
      const fd = new FormData();
      fd.append("task", name);
      for (const [k, v] of Object.entries(args)) {
        if (v instanceof File || v instanceof Blob) fd.append(k, v, v instanceof File ? v.name : k);
        else fd.append(k, JSON.stringify(v));
      }
      init = { method: "POST", body: fd }; // the browser sets the boundary
    } else {
      init = {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ task: name, args }),
      };
    }

    const r = await this.#fetch(url, init);
    if (!r.ok) throw new Error(`Task call failed (${r.status}): ${await r.text()}`);
    return r.json();
  }

  /**
   * A proxied HTTP call to the service. The session attaches whatever auth
   * the gate wants; the server decides what actually goes upstream.
   * @param {string} path — relative to the service URL, e.g. "/v1/users"
   * @param {RequestInit} [init]
   */
  async fetch(path, init = {}) {
    let url = `${this.#serviceURL}${path}`;
    if (this.#serviceID && (this.#proxied || window.location.protocol !== "file:")) {
      url = `${API}/service/${this.#serviceID}/${path.replace(/^\//, "")}`;
    }
    return this.#fetch(url, init);
  }
}

// ── Remote updates ──────────────────────────────────────────────
// Opt-in auto-update for hosted apps. The /check and /apply endpoints are
// anonymous and server-global (see design/remote-updates.md) — any hosted app
// can poll them. Nothing here runs unless the app calls autoUpdates().
//
// sseStream is the shared SSE-over-POST helper (fetch + ReadableStream;
// EventSource is GET-only). The control panel reuses it via window.FrBr.

const SSE_BACKOFF_MIN = 1000;
const SSE_BACKOFF_MAX = 30000;
// A connection that survives this long counts as "real" — see sseStream's
// attempt-reset rule below.
const SSE_SETTLED_MS = 5000;

// Exponential backoff with 50–100% jitter. A server-sent `retry:` overrides
// our schedule (that's what the field is for), jitter still applies so a herd
// of tabs doesn't reconnect in lockstep.
function sseBackoff(attempt, retryHint) {
  const base = retryHint ?? Math.min(SSE_BACKOFF_MAX, SSE_BACKOFF_MIN * 2 ** attempt);
  return Math.round(base * (0.5 + Math.random() * 0.5));
}

function sseSleep(ms, signal) {
  return new Promise((resolve) => {
    const done = () => { clearTimeout(timer); signal?.removeEventListener('abort', done); resolve(); };
    const timer = setTimeout(done, ms);
    signal?.addEventListener('abort', done, { once: true });
  });
}

// Parse one SSE block into its fields. Handles the parts the old inline
// parser skipped: `field:value` with no space, `:heartbeat` comment lines,
// multi-line `data:` (joined with newlines, per spec — concatenating them
// bare produced invalid JSON), and the `id:`/`retry:` fields that make
// resumption possible.
function parseSSEBlock(block) {
  let event = 'message', data = null, id, retry;
  for (const raw of block.split('\n')) {
    if (!raw || raw.startsWith(':')) continue;
    const c = raw.indexOf(':');
    const field = c < 0 ? raw : raw.slice(0, c);
    let value = c < 0 ? '' : raw.slice(c + 1);
    if (value.startsWith(' ')) value = value.slice(1);
    switch (field) {
      case 'event': event = value; break;
      case 'data': data = data === null ? value : data + '\n' + value; break;
      case 'id': id = value; break;
      case 'retry': { const n = parseInt(value, 10); if (!Number.isNaN(n)) retry = n; break; }
    }
  }
  return { event, data, id, retry };
}

/**
 * SSE over fetch + ReadableStream (EventSource is GET-only and can't set
 * headers, which rules it out for anything behind bearer auth).
 *
 * Two modes, and the default matters:
 *
 *  - `reconnect: false` (default) — one connection. Resolves when the server
 *    closes the stream, throws on any error. This is what a *finite* stream
 *    wants: /updates/apply and /updates/{id}/build send their events, close,
 *    and completion is the meaningful signal. Reconnecting those would loop
 *    forever over a job that already succeeded.
 *
 *  - `reconnect: true` — an *infinite* stream, like a change subscription.
 *    A closed connection is a failure to recover from, not a result. Retries
 *    with jittered backoff, resumes via Last-Event-ID, and resolves only on
 *    abort or a fatal (non-retryable) status.
 *
 * @param {Object}       [opts]
 * @param {AbortSignal}  [opts.signal]       — abort to close; resolves, doesn't throw
 * @param {boolean}      [opts.reconnect]    — retry on drop (default false)
 * @param {string}       [opts.lastEventId]  — resume point for the first connect
 * @param {Function}     [opts.onOpen]       — called on each successful connect
 * @param {Function}     [opts.onError]      — called per retryable failure while reconnecting
 * @param {AuthSession}  [opts.session]      — attaches this page's credential
 */
export async function sseStream(path, {
  method = 'POST', body, session, onEvent,
  reconnect = false, signal, onOpen, onError, lastEventId = null,
} = {}) {
  let attempt = 0;
  let retryHint = null;
  let lastId = lastEventId;

  for (;;) {
    if (signal?.aborted) return;

    let fatal = false;
    let gotEvent = false;
    const openedAt = Date.now();

    try {
      const headers = new Headers({ 'Accept': 'text/event-stream' });
      const opts = { method, headers, signal };
      if (body) { headers.set('Content-Type', 'application/json'); opts.body = JSON.stringify(body); }
      session?.addAuth(headers);
      if (lastId != null) headers.set('Last-Event-ID', lastId);

      const res = await fetch(path, opts);
      if (!res.ok) {
        // 4xx won't fix itself by asking again — bad request, revoked token,
        // deleted resource. 408/429 are the exceptions that mean "later".
        fatal = res.status >= 400 && res.status < 500 && res.status !== 408 && res.status !== 429;
        const err = new Error(`${res.status}: ${(await res.text().catch(() => '')) || res.statusText}`);
        err.status = res.status;
        throw err;
      }

      onOpen && onOpen();
      const reader = res.body.getReader();
      const dec = new TextDecoder();
      let buf = '';
      for (;;) {
        const { value, done } = await reader.read();
        if (done) break;
        // Normalize CRLF up front so block/line splitting stays simple.
        buf = (buf + dec.decode(value, { stream: true })).replace(/\r\n/g, '\n');
        let idx;
        while ((idx = buf.indexOf('\n\n')) >= 0) {
          const block = buf.slice(0, idx);
          buf = buf.slice(idx + 2);
          const { event, data, id, retry } = parseSSEBlock(block);
          if (id !== undefined) lastId = id;
          if (retry !== undefined) retryHint = retry;
          if (data === null || data === '') continue;
          let payload;
          try {
            payload = JSON.parse(data);
          } catch {
            continue;
          }
          gotEvent = true;
          // A throwing handler shouldn't kill the stream, but it shouldn't
          // vanish either — silent catch blocks around stream plumbing are
          // where bugs go to hide.
          try {
            onEvent && onEvent(event, payload);
          } catch (e) {
            console.error('sseStream: onEvent handler threw', e);
          }
        }
      }

      if (!reconnect) return;
    } catch (err) {
      if (signal?.aborted) return;
      if (!reconnect || fatal) throw err;
      onError && onError(err);
    }

    if (signal?.aborted) return;
    // Only credit a connection that actually did something. Otherwise a
    // server that accepts and immediately closes would reset the backoff on
    // every cycle and get hammered at the floor delay forever.
    if (gotEvent || Date.now() - openedAt >= SSE_SETTLED_MS) attempt = 0;
    await sseSleep(sseBackoff(attempt++, retryHint), signal);
  }
}

class _RateLimited extends Error {}

// GET /api/updates/check — anonymous. Returns feeds with an update available.
export async function fetchUpdatesCheck() {
  const r = await fetch(`${API}/api/updates/check`);
  if (r.status === 429) throw new _RateLimited('rate limited');
  if (!r.ok) throw new Error(`check failed (${r.status})`);
  const { updates } = await r.json();
  return updates || [];
}

// POST /api/updates/apply — anonymous, SSE progress. Body {ids?}: named feeds
// or all receive feeds. Resolves to the collected event log.
export async function applyUpdates(ids, { onEvent } = {}) {
  const events = [];
  await sseStream(`${API}/api/updates/apply`, {
    body: { ids },
    onEvent: (ev, data) => { events.push({ event: ev, data }); onEvent?.(ev, data); },
  });
  return events;
}

const DISMISS_KEY = 'frbr:dismissed-updates';

function dismissedVersions() {
  try { return new Set(JSON.parse(localStorage.getItem(DISMISS_KEY) || '[]')); }
  catch { return new Set(); }
}

function rememberDismissed(versions) {
  if (!versions.length) return;
  const set = dismissedVersions();
  for (const v of versions) set.add(v);
  // Keep the list bounded — only remember the last 32.
  const arr = [...set].slice(-32);
  try { localStorage.setItem(DISMISS_KEY, JSON.stringify(arr)); } catch {}
}

// applyUpdates resolves whenever the stream completes — failures arrive
// INSIDE it as feed_error events (or summary.failed > 0), e.g. validation
// refusing an unknown target. Both the banner and the auto-apply path need
// this check, so it lives here: returns the first failure message or null.
function applyFailures(events) {
  const bad = events.filter(e => e.event === 'feed_error');
  if (bad.length) return (bad[0] && bad[0].data && bad[0].data.message) || 'apply failed';
  const summary = events.find(e => e.event === 'summary');
  if (summary && summary.data.failed > 0) return 'apply failed';
  return null;
}

// Session-scoped memory of versions auto-apply already attempted. Without
// it, a feed whose apply persistently fails would be retried and re-bannered
// on every tick — and a reload-loop guard for anything unexpected. Success
// reloads the page anyway, so stale entries are harmless.
const AUTOATTEMPT_KEY = 'frbr:autoapply-attempted';
const autoAttempted = new Set();

function loadAutoAttempted() {
  try {
    for (const v of JSON.parse(sessionStorage.getItem(AUTOATTEMPT_KEY) || '[]')) autoAttempted.add(v);
  } catch {}
  return autoAttempted;
}

function rememberAutoAttempted(versions) {
  for (const v of versions) autoAttempted.add(v);
  try { sessionStorage.setItem(AUTOATTEMPT_KEY, JSON.stringify([...autoAttempted].slice(-32))); } catch {}
}

// autoApplyNow is the prompt-free handler: apply, then reload. On failure it
// marks the versions attempted and falls back to the banner — a human should
// see why the update didn't land, and can dismiss or retry from there.
async function autoApplyNow(ups, apply) {
  const attempted = loadAutoAttempted();
  const todo = ups.filter(u => u.version && !attempted.has(u.version));
  if (!todo.length) return defaultUpdateBanner(ups, apply);
  rememberAutoAttempted(todo.map(u => u.version));
  try {
    const events = await apply(todo.map(u => u.id));
    const fail = applyFailures(events);
    if (fail) throw new Error(fail);
    location.reload();
  } catch (e) {
    console.error('[frbr] auto-apply failed, falling back to the banner:', e);
    defaultUpdateBanner(todo, apply, e.message);
  }
}

/**
 * Start an opt-in auto-update poller. Checks once immediately, then again
 * every intervalMs. Returns a stop() function.
 *
 * Bare `autoUpdates()` just works: with no options it shows the built-in
 * banner whenever an update appears.
 *
 * @param {Object} opts
 * @param {number} [opts.intervalMs=900000]   — poll cadence (default 15 min);
 *                                      first check runs immediately on start
 * @param {Function} [opts.onAvailable]       — (ups[], apply) => void | Promise.
 *                                      Defaults to defaultUpdateBanner.
 * @param {boolean} [opts.autoApply=false]   — skip the prompt: apply and
 *                                      reload as soon as an update appears.
 *                                      Supersedes the default banner; an
 *                                      explicit onAvailable still fires
 *                                      (notification only). On a failed
 *                                      apply it falls back to the banner
 *                                      rather than reload-looping.
 * @param {Function} [opts.onProgress]        — (event, data) => void  (during apply)
 * @param {Function} [opts.onApplied]          — (events[]) => void   (after apply)
 */
export function autoUpdates({
  intervalMs = 15 * 60_000,
  onAvailable,
  autoApply = false,
  onProgress,
  onApplied,
} = {}) {
  let stopped = false, timer = null, backoff = 1;
  const applyFn = (ids) => doApply(ids);
  const notify = onAvailable || (typeof document !== 'undefined' ? defaultUpdateBanner : null);

  const arm = () => {
    if (stopped) return;
    // Don't burn the per-IP rate budget on a backgrounded tab.
    if (typeof document !== 'undefined' && document.hidden) {
      document.addEventListener('visibilitychange', tick, { once: true });
      return;
    }
    timer = setTimeout(tick, intervalMs * backoff);
  };

  const tick = async () => {
    timer = null;
    if (stopped || (typeof document !== 'undefined' && document.hidden)) return arm();
    try {
      const ups = await fetchUpdatesCheck();
      backoff = 1;
      if (!ups.length) return arm();
      // Don't re-nag for versions the user already dismissed.
      const dismissed = dismissedVersions();
      const fresh = ups.filter(u => !dismissed.has(u.version));
      if (!fresh.length) return arm();
      if (autoApply) {
        // Notification only — the apply happens in autoApplyNow.
        if (onAvailable) await onAvailable(fresh, applyFn);
        await autoApplyNow(fresh, applyFn);
      } else {
        await notify?.(fresh, applyFn);
      }
    } catch (e) {
      if (e instanceof _RateLimited) backoff = Math.min(backoff * 2, 8); // cap ~2h
      // other errors: stay quiet, retry next tick
    }
    arm();
  };

  const doApply = async (ids) => {
    const events = await applyUpdates(ids, { onEvent: onProgress });
    onApplied?.(events);
    return events;
  };

  // Check once right away, then keep the interval cadence via arm(). If the
  // page loads hidden, tick's own guard defers until it becomes visible.
  tick();
  return () => { stopped = true; if (timer) clearTimeout(timer); };
}

// Banner styles are injected once. Base looks live inline on the elements
// so a CSP that blocks this <style> only costs the animation and hover
// polish, not the banner itself.
function ensureBannerStyles() {
  if (document.getElementById('frbr-banner-styles')) return;
  const st = document.createElement('style');
  st.id = 'frbr-banner-styles';
  st.textContent =
    '@keyframes frbr-slide-down{from{transform:translateY(-100%)}to{transform:none}}' +
    '@keyframes frbr-spin{to{transform:rotate(360deg)}}' +
    '.frbr-update-banner{animation:frbr-slide-down .35s cubic-bezier(.2,.85,.3,1.08)}' +
    '.frbr-update-banner b{color:#bcc7ff;font-weight:600}' +
    '.frbr-update-banner .frbr-apply:not(:disabled):hover{filter:brightness(1.15)}' +
    '.frbr-update-banner .frbr-apply:not(:disabled):active{transform:translateY(1px)}' +
    '.frbr-update-banner .frbr-spin{display:inline-block;width:11px;height:11px;margin-right:7px;' +
      'border:2px solid rgba(255,255,255,.35);border-top-color:#fff;border-radius:50%;' +
      'animation:frbr-spin .8s linear infinite;vertical-align:-1px}';
  document.head.appendChild(st);
}

/**
 * Turnkey DOM banner for apps that don't want to build their own UI.
 * Prepends a small banner; returns a remove() function. Clicking "Apply &
 * reload" runs apply then reloads the page (the running app may be one of
 * the things that just changed). An existing banner is replaced, so repeated
 * calls (poller ticks) never stack. `note` (optional) pre-populates the
 * error line — used by the auto-apply fallback to say why it gave up.
 */
export function defaultUpdateBanner(ups, apply, note) {
  ensureBannerStyles();
  document.querySelectorAll('.frbr-update-banner').forEach(el => el.remove());

  const el = document.createElement('div');
  el.className = 'frbr-update-banner';
  el.style.cssText = 'position:fixed;top:0;left:0;right:0;z-index:99999;' +
    'background:linear-gradient(180deg,#1f2347 0%,#16182f 100%);' +
    'border-bottom:1px solid rgba(126,143,255,.25);color:#e8eaf6;' +
    'font:13px/1.5 system-ui,-apple-system,"Segoe UI",sans-serif;' +
    'padding:10px 16px;display:flex;gap:10px;align-items:center;' +
    'box-shadow:0 4px 18px rgba(0,0,0,.45)';

  const txt = document.createElement('span');
  txt.innerHTML = '<b>Update available</b>';

  const label = ups.map(u => u.name || u.version || (u.id || '').slice(0, 8)).join(', ');
  const chip = document.createElement('span');
  chip.textContent = label;
  chip.style.cssText = 'background:rgba(126,143,255,.14);border:1px solid rgba(126,143,255,.3);' +
    'border-radius:6px;padding:1px 8px;font-weight:600;color:#c6d0ff;' +
    'white-space:nowrap;overflow:hidden;text-overflow:ellipsis;max-width:32em';

  // Hidden until an apply fails — then the message lives here in red
  // instead of replacing the button label, so retry stays one click away.
  const err = document.createElement('span');
  err.style.cssText = 'display:none;color:#ff9b9b;font-size:12px;' +
    'white-space:nowrap;overflow:hidden;text-overflow:ellipsis;max-width:36em';
  if (note) {
    err.textContent = note;
    err.title = note;
    err.style.display = 'inline';
  }

  const btn = document.createElement('button');
  btn.className = 'frbr-apply';
  btn.textContent = 'Apply & reload';
  btn.style.cssText = 'margin-left:auto;cursor:pointer;border:none;border-radius:999px;' +
    'padding:7px 18px;font:600 12.5px/1 system-ui,sans-serif;letter-spacing:.02em;color:#fff;' +
    'background:linear-gradient(180deg,#5c7fff 0%,#3a54d6 100%);' +
    'box-shadow:0 2px 10px rgba(70,95,225,.5),inset 0 1px 0 rgba(255,255,255,.28);' +
    'transition:filter .15s ease,transform .06s ease,opacity .15s ease';
  btn.onclick = async () => {
    btn.disabled = true;
    btn.style.opacity = '.8';
    btn.innerHTML = '<span class="frbr-spin"></span>Applying…';
    try {
      const events = await apply(ups.map(u => u.id));
      // applyUpdates resolves whenever the stream completes — failures
      // arrive INSIDE it as feed_error events (or summary.failed > 0),
      // e.g. validation refusing an unknown target. Don't reload over
      // them: the version never stamped, so /check would just re-offer
      // the same update after reload. Surface it instead.
      const fail = applyFailures(events);
      if (fail) throw new Error(fail);
      location.reload();
    } catch (e) {
      err.textContent = e.message;
      err.title = e.message;
      err.style.display = 'inline';
      btn.disabled = false;
      btn.style.opacity = '';
      btn.textContent = 'Apply & reload';
    }
  };

  const dismiss = document.createElement('button');
  dismiss.textContent = '×';
  dismiss.title = 'Dismiss (won\'t nag for these versions again)';
  dismiss.style.cssText = 'cursor:pointer;padding:2px 8px;background:none;border:none;' +
    'color:#e8eaf6;opacity:.6;font-size:17px;line-height:1;' +
    'transition:opacity .15s,color .15s';
  dismiss.onmouseenter = () => { dismiss.style.opacity = '1'; dismiss.style.color = '#ff9b9b'; };
  dismiss.onmouseleave = () => { dismiss.style.opacity = ''; dismiss.style.color = ''; };
  dismiss.onclick = () => {
    rememberDismissed(ups.map(u => u.version).filter(Boolean));
    el.remove();
  };

  el.append(txt, chip, err, btn, dismiss);
  document.body.prepend(el);
  return () => el.remove();
}

export default ServiceProxy;

// Expose to window for non-module consumers (e.g. the admin panel)
if (typeof window !== 'undefined') {
  window.FreshBreath = window.FrBr = {
    login, currentSession, signOut, AuthSession, SessionExpired, ServiceProxy,
    sseStream, fetchUpdatesCheck, applyUpdates, autoUpdates, defaultUpdateBanner,
  };
}
