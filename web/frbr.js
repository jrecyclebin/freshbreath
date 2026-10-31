/**
 * Fresh Breath client SDK.
 *
 * One verb: `login(serviceURL)`. It resolves what the door asks for, spends
 * a credential the browser already holds when one fits, and prompts only
 * when nothing does. What comes back is a ServiceProxy you can call.
 *
 * What the browser holds is always a Fresh Breath token — an identity with
 * any upstream credentials sealed inside it, unreadable here and unsealed
 * by the proxy on the way out. On the server's own origin, refresh tokens
 * live in HttpOnly cookies and the OAuth client_secret never leaves the
 * server, so the worst a page can read out of localStorage is a token that
 * expires. An app that runs elsewhere can't spend that cookie (it's
 * SameSite=Strict), so it keeps its own rotating refresh token in its
 * entry instead.
 */
import { McpClient, StreamableHTTPClientTransport } from "/control/vendor/frbr-deps-1.29.0.js";

const CFG = window.__HOMESLICE_CONFIG ?? {};
const API = CFG.apiBase ?? "";
let APP_NONCE = CFG.appNonce ?? null; // let: re-derived if the server couldn't resolve from Referer
// The auth record guarding this page. 0 when the door is open — an app
// behind the Anonymous record, or an instance still in setup.
let GATE_ID = CFG.authRecordID ?? 0; // let: re-derived alongside APP_NONCE

// The origin our callback page will post from. The page posts only to
// the app's own origin (its registered one for an app that runs off the
// server, or "*" for a file:// page, which has none); the listener checks
// the sender's origin as well.
const FRBR_ORIGIN = new URL(API || window.location.href, window.location.href).origin;

// resolveFromLocation: when the server couldn't derive our app from Referer
// (a page that set referrerpolicy="no-referrer", or a same-origin script tag
// the browser chose not to refer), tell the env endpoint where we are and
// re-derive. /env.js?loc= is trusted by the server for app resolution only —
// never the admin door — so this can recover a hosted app's nonce but not
// the control panel's. No-op when the server already resolved it.
async function resolveFromLocation() {
  if (APP_NONCE) return;                       // server resolved it from Referer
  if (window.location.origin !== FRBR_ORIGIN) return; // cross-origin: env.js can't help (file:// dev uses the explicit nonce)
  try {
    const r = await fetch(`${API}/env.js?loc=${encodeURIComponent(window.location.pathname + window.location.search)}`);
    if (!r.ok) return;
    const cfg = await r.json();
    if (cfg.appNonce) APP_NONCE = cfg.appNonce;
    GATE_ID = cfg.authRecordID ?? GATE_ID;
    Object.assign(CFG, cfg);                   // keep CFG in step for anything that reads it later
  } catch { /* leave the server-rendered defaults; the page must use an explicit nonce */ }
}

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

// ── API helper ───────────────────────────────────────────────────────
//
// Extracted from the control panel. Apps may need access to the central API.
// Pass in the session that clears the app's gate — or a remote's session,
// and path is that remote's own (/api/me).
//
export async function api(session, path, opts = {}) {
  return authedFetch(session, `${session?.current.base ?? API}${path}`, jsonInit(opts));
}

// Only objects and arrays are stringified. Strings, binary bodies
// (Blob/File, ArrayBuffer, typed arrays, ReadableStream) and pre-encoded
// forms pass through untouched — JSON.stringify(file) yields "{}", and a
// string would arrive in quotes, either of which corrupts raw uploads
// (e.g. SSH file sync's hash-verified PUTs).
function jsonInit(opts) {
  const init = { ...opts };
  const body = init.body;
  const isRawBody = typeof body === "string" || body instanceof Blob ||
    body instanceof ArrayBuffer || ArrayBuffer.isView(body) ||
    body instanceof ReadableStream || body instanceof FormData ||
    body instanceof URLSearchParams;
  if (body && !isRawBody) {
    init.headers = new Headers(init.headers || {});
    if (!init.headers.has('Content-Type')) init.headers.set('Content-Type', 'application/json');
    init.body = JSON.stringify(body);
  }
  return init;
}

// Every authorized request out of frbr.js. An invalid token buys one
// refresh and a retry; whatever the server still turns down — or wants a
// newer login for — buys one fresh login through the door that minted the
// credential, and another retry. Still refused after that throws
// SessionExpired, so neither error reaches the caller as a response.
//
// An answer a remote gave (marked X-Frbr-Remote by the proxy) is about the
// remote's session, not this one, so it goes back to the caller untouched
// — remoteFetch deals with it.
async function authedFetch(session, url, init) {
  session = session?.current ?? null;
  if (session?.remote) return remoteFetch(session, url, init);
  const original = session;
  let renewable = false;
  const send = (s) => {
    const headers = new Headers(init.headers);
    headers.set("X-App-Nonce", APP_NONCE);
    renewable = s ? s.addAuth(headers) : false;
    return fetch(url, { ...init, headers });
  };

  const ours = (r) => !fromRemote(r);
  let r = await send(session);
  if (r.status >= 400 && r.status < 500 && renewable && ours(r)) {
    try {
      await session.refresh();
      r = await send(session);
    } catch (e) {
      if (!(e instanceof SessionExpired)) throw e;
    }
  }

  // A stream body was spent by the first send, so it can't ride a retry.
  const replayable = !(init.body instanceof ReadableStream);
  if (r.status === 401 && ours(r) && session && replayable && window.__FRBR_RELOGIN !== false) {
    const fresh = await ensureLogin(session.door);
    if (fresh) {
      // login() hands back a ServiceProxy for service doors; the
      // AuthSession it rides is what knows how to authorize.
      session = fresh.session ?? fresh;
      r = await send(session);
      if (r.status !== 401) original.supersede(session);
    }
  }

  if (r.status === 401 && ours(r)) throw new SessionExpired(session?.authID ?? GATE_ID);
  return r;
}

const fromRemote = (r) => r.headers.has("X-Frbr-Remote");

// A call to a remote carries two sign-ins. This page's own clears this
// server's door, through authedFetch like anything else; the remote's rides
// along as X-Tunnel-Token. A refused answer from the remote buys one
// refresh and a retry, then one sign-in prompt and another retry. Still
// refused throws RemoteExpired — never SessionExpired, which would read as
// this page's own sign-in ending.
async function remoteFetch(session, url, init) {
  const send = () => {
    const headers = new Headers(init.headers);
    session.addAuth(headers);
    return authedFetch(currentSession(), url, { ...init, headers });
  };
  const refused = (r) => !r || (r.status === 401 && fromRemote(r));
  // A stream body was spent by the first send, so it can't ride a retry.
  const replayable = !(init.body instanceof ReadableStream);

  let r = null; // nothing sent yet: there was no sign-in to send
  if (session.signedIn) {
    r = await send();
    if (refused(r) && replayable) {
      try {
        await session.refresh();
        r = await send();
      } catch (e) {
        if (!(e instanceof RemoteExpired)) throw e;
      }
    }
  }

  if (refused(r) && (!r || replayable) && window.__FRBR_RELOGIN !== false) {
    if (await ensurePairing(session)) r = await send();
  }

  if (refused(r)) {
    session.forget();
    throw new RemoteExpired(session.remote.id);
  }
  return r;
}

// ── The store ───────────────────────────────────────────────────────
//
// One entry per cleared auth record at localStorage["frbr:auth:<id>"].
// frbr.js is the sole writer; apps never touch these keys. An app that runs
// elsewhere also keeps its refresh token here (refresh_token), since the
// cookie stays on the server's origin. Chrome gives every file:// page one
// shared storage origin, so any local HTML file can read a disk app's. Because apps
// share an origin they also share the store — deliberate, and the reason
// two apps behind one gate prompt once between them. Spending is still
// bounded by X-App-Nonce and the app's service links, not by who can read
// a key.
//
// A sign-in on a remote is kept at localStorage["frbr:tunnel:<remote id>"]
// instead: its auth_id is the remote's own record, which could collide
// with one of ours.

const STORE_PREFIX = "frbr:auth:";
const TUNNEL_PREFIX = "frbr:tunnel:";
const STORE_V = 1;
// Refresh this far ahead of expiry, so a request never races the clock.
const REFRESH_MARGIN_MS = 5 * 60_000;

const storeKey = (authID) => STORE_PREFIX + authID;
const entryKey = (e) => e.remote ? TUNNEL_PREFIX + e.remote.id : storeKey(e.auth_id);

// Read an entry, live or lapsed — a lapsed token entry is still worth
// having, since the refresh cookie can revive it. An entry from a schema
// we don't recognize is discarded outright: re-logging in is cheap, and
// guessing at an unknown shape is not.
function readEntry(authID) {
  return authID ? readStored(storeKey(authID)) : null;
}

function writeEntry(entry) {
  return writeStored(entryKey(entry), entry);
}

function readStored(key) {
  let e = null;
  try { e = JSON.parse(localStorage.getItem(key) || "null"); } catch {}
  if (!e || typeof e !== "object") return null;
  if (e.v !== STORE_V) { evictStored(key); return null; }
  return e;
}

function writeStored(key, entry) {
  entry.written_at = new Date().toISOString();
  try { localStorage.setItem(key, JSON.stringify(entry)); } catch {}
  return entry;
}

function evictStored(key) {
  try { localStorage.removeItem(key); } catch {}
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

// What a refresh posts: the entry's own refresh token, or none, in which
// case the HttpOnly cookie carries it.
function refreshBody(e) {
  const body = new URLSearchParams({ grant_type: "refresh_token" });
  if (e.refresh_token) body.set("refresh_token", e.refresh_token);
  return body;
}

// The entry a refresh answer rotates e into. Only an entry that came with
// a refresh token keeps one; a cookie session never stores it.
function rotatedEntry(e, t) {
  const next = {
    ...e,
    access_token: t.access_token,
    token_type: t.token_type || e.token_type || "Bearer",
    expires_at: expiryFrom(t),
  };
  if (e.refresh_token && t.refresh_token) next.refresh_token = t.refresh_token;
  return next;
}

// Thrown when a login is gone for good: the refresh was refused, or the
// server wants a fresh login and the user dismissed the prompt for one
// (or the page opted out of prompting with window.__FRBR_RELOGIN = false).
export class SessionExpired extends Error {
  constructor(authID) {
    super("Session expired — log in again");
    this.name = "SessionExpired";
    this.authID = authID;
  }
}

// Thrown when a remote sign-in is gone: never made, refused on refresh, or
// the sign-in prompt was dismissed. Deliberately not a SessionExpired,
// which means this page's own sign-in ended.
export class RemoteExpired extends Error {
  constructor(remoteID) {
    super("Signed out of this remote — sign in to it again");
    this.name = "RemoteExpired";
    this.remoteID = remoteID;
  }
}

// ── AuthSession ─────────────────────────────────────────────────────
//
// One per auth record, held in a registry keyed by id. Two proxies behind
// the same gate get the same session: one credential, one refresh mutex,
// one storage slot. When a refresh lands, every proxy riding that session
// recovers together.
//
// A remote's session is one too, keyed by the remote's id: a sign-in on
// another Fresh Breath this server reaches through its tunnel, driven
// through the proxy at /api/remotes/<id>/. It's the remote's own — one of
// its users, its 15-minute token, its 14-day refresh family — and its
// tokens live only here in the browser, never on this server.

const sessions = new Map();
const registryKey = (e) => e.remote ? `remote:${e.remote.id}` : e.auth_id;

export class AuthSession {
  #entry;
  #refreshing = null;
  #successor = null;

  constructor(entry) { this.#entry = entry; }

  static for(entry) {
    const existing = sessions.get(registryKey(entry));
    if (existing) { existing.#entry = entry; return existing; }
    const session = new AuthSession(entry);
    sessions.set(registryKey(entry), session);
    return session;
  }

  static get(authID) { return sessions.get(authID) ?? null; }

  // The session to spend in this one's place. A login for another record
  // can take over — the app moved behind a new gate, say — and whoever
  // still holds this session follows it there instead of prompting again.
  get current() { return this.#successor?.current ?? this; }

  supersede(next) {
    if (next === this) return;
    next.#successor = null; // keeps the chain from looping back here
    this.#successor = next;
  }

  get authID()    { return this.#entry.auth_id; }
  get kind()      { return this.#entry.kind; }
  get provider()  { return this.#entry.provider ?? null; }
  get subject()   { return this.#entry.subject ?? null; }
  get legs()      { return this.#entry.legs ?? []; }
  get token()     { return this.#entry.access_token ?? null; }
  get user_name() { return jwtPayload(this.#entry.access_token)?.user_name; }
  get email()     { return jwtPayload(this.#entry.access_token)?.user_email; }
  get expiresAt() { return this.#entry.expires_at ? new Date(this.#entry.expires_at) : null; }
  // The service URL whose login minted this credential (null for the app's
  // own gate) — the door a fresh login goes back through.
  get door()      { return this.#entry.door ?? null; }
  get signedIn()  { return !!(this.#entry.access_token || this.#entry.key); }

  // The remote this sign-in is on — {id, name, fingerprint}, the last two
  // null when unknown — or null for one of ours.
  get remote() {
    const r = this.#entry.remote;
    return r ? { ...r } : null;
  }
  // Where api() paths go: a remote's through this server's proxy.
  get base() {
    const r = this.#entry.remote;
    return r ? `${API}/api/remotes/${encodeURIComponent(r.id)}` : API;
  }

  // Whether this session already clears a given record — directly, or as
  // one of the legs sealed into a merged two-leg token.
  covers(authID) {
    return this.authID === authID || this.legs.includes(authID);
  }

  // Set whatever header this record's kind implies, and report whether the
  // credential is one a refresh could renew. A typed key is not.
  addAuth(headers) {
    const e = this.#entry;
    if (e.remote) {
      // This page's own session takes Authorization; see remoteFetch.
      if (e.access_token) headers.set("X-Tunnel-Token", e.access_token);
      return !!e.access_token;
    }
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
    if (e.remote) return this.#refreshRemote(e);
    if (!e.access_token) return; // nothing to rotate

    // On the server's origin the refresh token rides an HttpOnly cookie
    // scoped to this exact path, which is why the route carries the record
    // id: a browser holding several records' refresh tokens keeps them in
    // distinct cookie slots. An app that runs elsewhere sends the one its
    // entry holds, and gets the rotated one back in the body.
    const r = await fetch(`${API}/oauth/token/${e.auth_id}`, {
      method: "POST",
      headers: {
        "Content-Type": "application/x-www-form-urlencoded",
        "X-App-Nonce": APP_NONCE,
      },
      credentials: e.refresh_token ? "omit" : "include",
      body: refreshBody(e),
    });
    if (!r.ok) {
      if (r.status === 400 || r.status === 401) {
        // Refused: the refresh family is gone. Keeping the entry would only
        // let a dead token ride along on the next request.
        this.forget();
        throw new SessionExpired(e.auth_id);
      }
      if (r.status === 403) {
        // This app may no longer spend this record (its gate moved). The
        // login may still serve other apps, so the entry stays put.
        throw new SessionExpired(e.auth_id);
      }
      throw new Error(`Token refresh failed (${r.status})`);
    }
    this.#entry = writeEntry(rotatedEntry(e, await r.json()));
  }

  // A remote refreshes through the proxy, as this page's own session. This
  // server refusing has already thrown SessionExpired from authedFetch; only
  // the remote refusing (a marked answer) means its refresh family is gone.
  async #refreshRemote(e) {
    if (!e.refresh_token) { this.forget(); throw new RemoteExpired(e.remote.id); }
    const r = await authedFetch(currentSession(), `${this.base}/oauth/token/${e.auth_id}`, {
      method: "POST",
      headers: { "Content-Type": "application/x-www-form-urlencoded" },
      body: refreshBody(e),
    });
    if (!r.ok) {
      if (fromRemote(r) && (r.status === 400 || r.status === 401)) {
        this.forget();
        throw new RemoteExpired(e.remote.id);
      }
      throw new Error(`Remote token refresh failed (${r.status})`);
    }
    this.#entry = writeEntry(rotatedEntry(e, await r.json()));
  }

  /**
   * Sign in to a remote as one of its users. The passphrase passes through
   * this server to the remote and is kept by neither.
   */
  async pair(email, passphrase) {
    const e = this.#entry;
    if (!e.remote) throw new TypeError("pair() is for a remote's session");
    const r = await authedFetch(currentSession(), `${this.base}/service/direct`,
      jsonInit({ method: "POST", body: { email, passphrase } }));
    if (!r.ok) throw new Error((await r.text()).trim() || `Sign-in to the remote failed (${r.status})`);
    const t = await r.json();
    this.#entry = writeEntry({
      v: STORE_V,
      remote: e.remote,
      auth_id: t.auth_id,
      access_token: t.access_token,
      refresh_token: t.refresh_token,
      token_type: t.token_type || "Bearer",
      expires_at: expiryFrom(t),
    });
    return this;
  }

  // Drop this session's credential and its stored entry. The server keeps
  // no session to end — the refresh family dies with the next attempt to
  // use it (a remote's lives on there until it expires or is revoked in
  // the remote's own sessions list). The object stays registered, emptied,
  // so the next login refills it and whoever still holds it recovers too.
  forget() {
    const e = this.#entry;
    evictStored(entryKey(e));
    this.#entry = e.remote ? { remote: e.remote } : { auth_id: e.auth_id };
  }
}

/**
 * The session on a remote, signed in or not yet. Pass its id, or the
 * remote as /api/remotes lists it, so a sign-in prompt can say which
 * remote it's for. Calls on it go through api(session, path).
 */
export function remote(r) {
  const info = typeof r === "string" ? { id: r } : r;
  // What's known of the remote so far: the caller's, the stored entry's
  // (from its last sign-in), or the session's from earlier on this page.
  const stored = readStored(TUNNEL_PREFIX + info.id) ?? {};
  const known = sessions.get(`remote:${info.id}`)?.remote ?? stored.remote ?? {};
  return AuthSession.for({
    ...stored,
    remote: {
      id: info.id,
      name: info.name ?? known.name ?? null,
      fingerprint: info.fingerprint ?? known.fingerprint ?? null,
    },
  });
}

// ── login ───────────────────────────────────────────────────────────

/**
 * Ask the door what a login would cost, without starting one. A pure
 * query — no flow begins, no state is stored, no window opens — which
 * is what makes it safe to call at any moment, loaded or idle.
 *
 * @param {string} [serviceURL] — a registered service URL, or omitted
 *                                (or null) for this page's own gate
 * @returns {Promise<{type: string, legs?: Array, service?: Object}>}
 *          "anonymous" (nothing to ask) or "legs" (these records, in
 *          this order)
 */
export async function resolveDoor(serviceURL) {
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
async function beginLogin(serviceURL, state, session, returnTo) {
  const q = new URLSearchParams({ state });
  if (serviceURL) q.set("url", serviceURL);
  if (returnTo) q.set("return", returnTo);
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
// and skips the popup entirely. complete asks for an entry covering every
// leg — silent mode has no login to finish a partial one.
async function candidateSession(legIDs, complete = false) {
  let best = null, bestCount = 0;
  for (const id of legIDs) {
    const e = readEntry(id);
    if (!e) continue;
    const covered = legIDs.filter(l => l === e.auth_id || (e.legs || []).includes(l)).length;
    if (covered > bestCount) { best = e; bestCount = covered; }
  }
  if (!best || (complete && bestCount < legIDs.length)) return null;
  const session = AuthSession.for(best);
  try { await session.check(); } catch { return null; }
  return session;
}

// Thrown when the browser refuses the login window — there was no click
// behind the call, or the click's activation was already spent.
class PopupBlocked extends Error {
  constructor() {
    super("The login window was blocked");
    this.name = "PopupBlocked";
  }
}

function openLoginWindow(url) {
  const popup = window.open(url, "frbrAuth", "width=520,height=720");
  if (!popup) throw new PopupBlocked();
  return popup;
}

// Run a login in a window and resolve with the store entry it posts back.
// A window opened ahead of time (still blank) is navigated rather than
// opened again.
function popupLogin(url, state, popup = null) {
  return new Promise((resolve, reject) => {
    if (popup) popup.location.href = url;
    else popup = openLoginWindow(url);

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

// What a tool call returns to the app: the result's text, parsed as JSON
// when it is JSON. An error result throws, carrying the text.
// Tasks and virtual services answer /service/call in this shape already,
// so only MCP-transport results get unwrapped here.
function toolOutput(result) {
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
 * Bring the user back in after a login dies. Runs a fresh login straight
 * away when the click behind this request still holds user activation;
 * otherwise (or if that window fails) shows a small dialog whose "Log in"
 * button supplies the click. Requests that fail together share one prompt
 * per door, and all retry once it lands.
 *
 * @param {string} [serviceURL] — a registered service URL, or omitted for
 *                                the page's own gate
 * @returns {Promise<ServiceProxy|AuthSession|null>} the fresh login, or
 *          null if the user dismissed the dialog
 */
const relogins = new Map(); // serviceURL → the prompt in flight for that door

export function ensureLogin(serviceURL = null) {
  if (!relogins.has(serviceURL)) {
    relogins.set(serviceURL, promptLogin(serviceURL).finally(() => relogins.delete(serviceURL)));
  }
  return relogins.get(serviceURL);
}

async function promptLogin(serviceURL) {
  let note = null;
  if (navigator.userActivation?.isActive) {
    try {
      return await login(serviceURL, "fresh");
    } catch (e) {
      if (!(e instanceof PopupBlocked)) note = e.message;
    }
  }
  return loginDialog(serviceURL, note);
}

// The prompts' look. Styles go on through the CSSOM, which a CSP that
// blocks inline <style> still allows.
const DIALOG_CSS = [
  "background:#1e1e1e", "color:#f5f5f5", "border:1px solid #555",
  "border-radius:10px", "padding:20px 22px", "max-width:340px",
  "box-shadow:0 8px 32px rgba(0,0,0,.5)", "text-align:center",
  "font:14px system-ui, sans-serif",
].join(";");
const TITLE_CSS = "font-weight:600; font-size:16px; margin-bottom:8px";
const MSG_CSS = "color:#bbb; margin-bottom:16px; line-height:1.4";
const PRIMARY_CSS = [
  "padding:7px 18px", "cursor:pointer", "border:0", "border-radius:6px",
  "background:#3b82f6", "color:#fff", "font:600 14px system-ui, sans-serif",
].join(";");
const SECONDARY_CSS = [
  "padding:7px 14px", "cursor:pointer", "border:1px solid #666",
  "border-radius:6px", "background:transparent", "color:#ddd",
  "font:14px system-ui, sans-serif",
].join(";");
const INPUT_CSS = [
  "display:block", "width:100%", "box-sizing:border-box", "margin-bottom:10px",
  "padding:7px 9px", "border:1px solid #555", "border-radius:6px",
  "background:#121212", "color:#f5f5f5", "font:14px system-ui, sans-serif",
].join(";");

function styled(tag, css, text = "") {
  const el = document.createElement(tag);
  el.style.cssText = css;
  el.textContent = text;
  return el;
}

// The re-login prompt.
function loginDialog(serviceURL, note) {
  return new Promise((resolve) => {
    const dialog = styled("dialog", DIALOG_CSS);
    dialog.setAttribute("aria-label", "Session expired");
    const title = styled("div", TITLE_CSS, "Session expired");
    const msg = styled("div", MSG_CSS, note ?? "Your session has ended. Log in again to continue.");
    const loginBtn = styled("button", PRIMARY_CSS, "Log in");
    const cancelBtn = styled("button", SECONDARY_CSS, "Not now");
    const buttons = styled("div", "display:flex; gap:10px; justify-content:center");
    buttons.append(loginBtn, cancelBtn);
    dialog.append(title, msg, buttons);

    let result = null;
    loginBtn.onclick = async () => {
      loginBtn.disabled = cancelBtn.disabled = true;
      try {
        result = await login(serviceURL, "fresh");
        dialog.close();
      } catch (e) {
        // Window closed or the flow failed — keep the dialog up and say why.
        msg.textContent = e?.message ?? "Login failed — try again.";
        loginBtn.disabled = cancelBtn.disabled = false;
      }
    };
    cancelBtn.onclick = () => dialog.close();
    // close covers both buttons and Escape, which <dialog> handles itself.
    dialog.onclose = () => { dialog.remove(); resolve(result); };

    document.body.appendChild(dialog);
    dialog.showModal();
  });
}

// One sign-in prompt per remote: calls that fail together share it, and
// all retry once it lands. Resolves with the session, or null if dismissed.
const pairings = new Map(); // remote id → the prompt in flight

function ensurePairing(session) {
  const { id } = session.remote;
  if (!pairings.has(id)) {
    pairings.set(id, pairDialog(session).finally(() => pairings.delete(id)));
  }
  return pairings.get(id);
}

// The remote sign-in prompt. No OAuth flow can reach a remote, so it asks
// for the remote's own email and passphrase here. The fingerprint is the
// remote's identity, which a name isn't: an Admin can rename remotes.
function pairDialog(session) {
  return new Promise((resolve) => {
    const { id, name, fingerprint } = session.remote;
    const label = name ?? id;
    const dialog = styled("dialog", DIALOG_CSS);
    dialog.setAttribute("aria-label", `Sign in to ${label}`);
    const title = styled("div", TITLE_CSS, `Sign in to ${label}`);
    const print = styled("div", "color:#888; font:12px ui-monospace, monospace; margin-bottom:8px",
      fingerprint ? `fingerprint ${fingerprint}` : "");
    const msg = styled("div", MSG_CSS,
      "Use your account on that remote. The passphrase passes through this server, and neither keeps it.");

    const form = styled("form", "margin:0");
    const email = styled("input", INPUT_CSS);
    email.type = "email";
    email.placeholder = "Email";
    email.autocomplete = "username";
    email.required = true;
    const pass = styled("input", INPUT_CSS);
    pass.type = "password";
    pass.placeholder = "Passphrase";
    pass.autocomplete = "current-password";
    pass.required = true;
    const signInBtn = styled("button", PRIMARY_CSS, "Sign in");
    signInBtn.type = "submit";
    const cancelBtn = styled("button", SECONDARY_CSS, "Not now");
    cancelBtn.type = "button";
    const buttons = styled("div", "display:flex; gap:10px; justify-content:center; margin-top:6px");
    buttons.append(signInBtn, cancelBtn);
    form.append(email, pass, buttons);
    dialog.append(title, print, msg, form);

    let result = null;
    form.onsubmit = async (ev) => {
      ev.preventDefault();
      signInBtn.disabled = cancelBtn.disabled = true;
      try {
        result = await session.pair(email.value.trim(), pass.value);
        dialog.close();
      } catch (e) {
        if (e instanceof SessionExpired) { dialog.close(); return; } // this page's own sign-in ended
        msg.textContent = e?.message ?? "Sign-in failed — try again.";
        pass.value = "";
        signInBtn.disabled = cancelBtn.disabled = false;
      }
    };
    cancelBtn.onclick = () => dialog.close();
    dialog.onclose = () => { dialog.remove(); resolve(result); };

    document.body.appendChild(dialog);
    dialog.showModal();
    email.focus();
  });
}

/**
 * Log in to a service and get back a proxy for it.
 *
 * With no argument it clears this page's own gate and returns nothing —
 * the "sign in" verb for a gated app. Either way the app's gate is part of
 * the bill, so logging in to a service signs you in to the app too.
 *
 * The mode says how hard to try:
 *   "normal" — spend a stored credential (refreshing it if it lapsed) and
 *              open the login window only when nothing fits.
 *   "silent" — spend a stored credential or come back null; never prompts.
 *   "fresh"  — ignore stored credentials and run the whole login again,
 *              always in a window. For when the server needs proof the
 *              user is here now (the SSH agent's key, say), which no
 *              refreshed token can give.
 *
 * A window needs a click behind it, so call "normal" and "fresh" from one.
 * "fresh" opens its window before anything else, while that click counts.
 *
 * "remote://<name>" signs in to a remote instead, and returns its
 * AuthSession — pass it to api(). It prompts for the remote's
 * email and passphrase in a dialog (no window, so no click needed).
 *
 * @param {string} [serviceURL] — a registered service URL, "remote://<name>",
 *                                or omitted (or null) for the app's own gate
 * @param {"normal"|"silent"|"fresh"} [mode]
 * @returns {Promise<ServiceProxy|AuthSession|null>}
 */
export async function login(serviceURL, mode = "normal") {
  // Simple boolean mode - two most common cases.
  if (mode === true)  { mode = "normal"; }
  if (mode === false) { mode = "silent"; }
  if (!["normal", "silent", "fresh"].includes(mode)) {
    throw new TypeError(`login mode must be "normal", "silent" or "fresh", not ${JSON.stringify(mode)}`);
  }
  if (serviceURL?.startsWith(REMOTE_SCHEME)) {
    return loginRemote(decodeURIComponent(serviceURL.slice(REMOTE_SCHEME.length)), mode);
  }
  const popup = mode === "fresh" ? openLoginWindow("about:blank") : null;
  try {
    const door = await resolveDoor(serviceURL);
    const proxyFor = (service, session) =>
      serviceURL ? new ServiceProxy({ serviceURL, service, session }) : session;

    if (door.type === "anonymous") return proxyFor(door.service, null);

    // A fresh login presents nothing, so the server runs every leg.
    const legIDs = (door.legs || []).map(l => l.auth_id);
    let session = mode === "fresh" ? null : await candidateSession(legIDs, mode === "silent");
    if (mode === "silent") return session ? proxyFor(door.service, session) : null;

    const state = uuidv4();
    const d = await beginLogin(serviceURL, state, session);
    const service = d.service ?? door.service;
    if (d.type === "anonymous") return proxyFor(service, null);
    if (d.type === "ok") return proxyFor(service, session);

    const entry = await popupLogin(d.url, state, popup);
    session = AuthSession.for(writeEntry({ ...entry, door: serviceURL ?? null }));
    return proxyFor(service, session);
  } finally {
    popup?.close(); // a no-op once the login closed it; otherwise unused
  }
}

const REMOTE_SCHEME = "remote://";

// The name picks the remote once, here; the session is kept by its id from
// then on, so a later rename can't redirect it.
async function loginRemote(name, mode) {
  const session = remote(await findRemote(name));
  if (mode === "fresh") session.forget();
  if (session.signedIn) {
    try {
      await session.check();
      return session;
    } catch (e) {
      if (!(e instanceof RemoteExpired)) throw e;
    }
  }
  if (mode === "silent") return null;
  return ensurePairing(session);
}

// Remote names are unique, ignoring case, so this is the one. The exact
// name goes first: SQLite only ignores ASCII case, so "Ärger" and "ärger"
// can both exist. An id is taken too, when no remote has that name.
async function findRemote(name) {
  const r = await api(currentSession(), "/api/remotes");
  if (!r.ok) throw new Error(`Couldn't list remotes (${r.status}): ${await r.text().catch(() => "")}`);
  const { remotes } = await r.json();
  const lower = name.toLowerCase();
  const found = remotes.find(x => x.name === name) ??
    remotes.find(x => x.name.toLowerCase() === lower) ??
    remotes.find(x => x.id === name);
  if (!found) throw new Error(`No remote named "${name}"`);
  return found;
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

/**
 * Forget a cleared record — this page's gate unless told otherwise. The
 * server revokes the login behind it and drops it from the gate pass, the
 * cookie that lets hosted pages through their gate.
 */
export function signOut(authID = GATE_ID) {
  if (!authID) return;
  (AuthSession.get(authID) ?? new AuthSession({ auth_id: authID })).forget();
  fetch(`${API}/service/logout?auth_id=${authID}`, { method: "POST", credentials: "include" })
    .catch(err => console.warn("frbr sign-out:", err?.message ?? err));
}

// ── Auto-login at load ───────────────────────────────────────────────
//
// A gated hosted app walks its users into the login before the first
// frame draws. With no credential in the store the page navigates itself
// into the flow and comes back with the store filled — the page *becomes*
// the popup, because a popup with no gesture behind it is a popup the
// browser blocks. Only pages on this origin take part: the hand-back
// runs through localStorage, which a cross-origin or file:// page
// cannot reach.
//
// A lapsed entry still counts as holding a credential — the refresh
// family can revive it silently — so only a first visit redirects. The
// sessionStorage guard breaks the loop when a login comes back without
// one: a page with a dead sign-in button beats a browser pinned between
// two redirects. Set window.__FRBR_AUTO_LOGIN = false before this script
// loads to keep the decision in app code.
const AUTO_KEY = "frbr:auto-login";

function autoLoginTried() {
  try { return !!sessionStorage.getItem(AUTO_KEY); } catch { return false; }
}

function autoLoginMaybe() {
  if (
    window.__FRBR_AUTO_LOGIN !== false &&
    GATE_ID &&
    window.location.origin === FRBR_ORIGIN &&
    !readEntry(GATE_ID) &&
    !autoLoginTried()
  ) {
    try { sessionStorage.setItem(AUTO_KEY, "1"); } catch {}
    resolveDoor().then(door => {
      if (door.type === "anonymous") return; // open door; nothing to ask
      const here = window.location.pathname + window.location.search + window.location.hash;
      return beginLogin(null, uuidv4(), null, here).then(d => {
        if (d.type === "redirect") window.location.replace(d.url);
      });
    }).catch(err => console.warn("frbr auto-login skipped:", err?.message ?? err));
  } else if (GATE_ID && readEntry(GATE_ID)) {
    try { sessionStorage.removeItem(AUTO_KEY); } catch {}
  }
}

// Resolve the app from the hosting location first (re-deriving APP_NONCE /
// GATE_ID when the server couldn't see Referer), then run the auto-login
// decision on the settled values. Skipped entirely when the page opts out
// via window.__FRBR_AUTO_LOGIN = false — and resolveFromLocation is a no-op
// when the server already resolved a nonce.
resolveFromLocation().finally(autoLoginMaybe);

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
  get session()    { return this.#session?.current ?? null; }

  // The slug for task and virtual services, which answer at /service/call
  // rather than over MCP transport; null for everything else.
  #serviceSlug() {
    if (this.#serviceURL?.startsWith("tasks://")) return this.#serviceURL.slice("tasks://".length);
    if (this.#serviceURL?.startsWith("/mcp/")) return this.#serviceURL.slice("/mcp/".length);
    return null;
  }

  // The single choke point: every request out of this proxy goes through
  // authedFetch, so a dead login is retried and re-prompted the same way
  // api() does it.
  async #fetch(url, init = {}) {
    return authedFetch(this.session, url, init);
  }

  // A proxied MCP service is reached through the server, which swaps the
  // session's token for the upstream credential; anything else directly.
  async #connect() {
    const headers = new Headers();
    let url = new URL(this.#serviceURL, window.location.href);
    if (this.#proxied && this.#serviceID) {
      url = new URL(`${API}/service/${this.#serviceID}/`, window.location.href);
      headers.set("X-App-Nonce", APP_NONCE);
    }
    if (this.session) {
      await this.session.check();
      this.session.addAuth(headers);
    }
    const transport = new StreamableHTTPClientTransport(url, {
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
      await this.session?.refresh();
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
    if (this.#serviceSlug()) {
      return await this.#callTask(name, args);
    }
    return toolOutput(await this.#withReconnect(() => this.#client.callTool({ name, arguments: args })));
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
 * @param {HeadersInit}  [opts.headers]      — more headers (a remote's X-Tunnel-Token, say)
 */
export async function sseStream(path, {
  method = 'POST', body, session, headers: extraHeaders, onEvent,
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
      const headers = new Headers(extraHeaders);
      headers.set('Accept', 'text/event-stream');
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
    api, login, currentSession, signOut, AuthSession, SessionExpired, ServiceProxy,
    remote, RemoteExpired,
    sseStream, fetchUpdatesCheck, applyUpdates, autoUpdates, defaultUpdateBanner,
  };
}
