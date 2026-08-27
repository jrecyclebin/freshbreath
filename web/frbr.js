/**
 * Fresh Breath Service Proxy client SDK.
 *
 * User-provided API keys and OAuth tokens are stored in the browser's
 * localStorage. The server's OAuth client_secret is never exposed to
 * the client — token exchange and refresh run server-side.
 */
import { McpClient, StreamableHTTPClientTransport, EventEmitter } from "/control/vendor/frbr-deps-1.29.0.js";

const API = window.__HOMESLICE_CONFIG?.apiBase ?? "";
const APP_NONCE = window.__HOMESLICE_CONFIG?.appNonce ?? null;

// Peek at a JWT's iss claim to check if it was issued by Freshbreath.
function isFreshbreathToken(raw) {
  if (!raw || typeof raw !== 'string') return false;
  const parts = raw.split('.');
  if (parts.length !== 3) return false;
  try {
    const payload = JSON.parse(atob(parts[1].replace(/-/g, '+').replace(/_/g, '/')));
    return payload.iss === 'freshbreath';
  } catch { return false; }
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

// ── static login flow ──
/**
  * Open an OAuth popup for the given service URL.
  * On success returns a ServiceProxy instance with token data loaded.
  */
export function login(svc) {
  const serviceURL = svc.serviceURL || svc;
  const actualState = svc.state || uuidv4();
  return new Promise(async (resolve, reject) => {
    // If we already know it's key-auth (have an apiKey), skip the popup
    const url = `${API}/service/login?url=${encodeURIComponent(serviceURL)}&state=${encodeURIComponent(actualState)}`;
    const res = await fetch(url, { headers: { "X-App-Nonce": APP_NONCE } });
    const data = await res.json();

    if (data.type !== "redirect") {
      try {
        if (data.type === "key-auth-complete") {
          resolve(new ServiceProxy({
            serviceURL: data.serviceURL,
            serviceID: data.serviceID,
            apiKey: svc.apiKey || data.apiKey,
            apiHeader: data.apiHeader,
            proxied: data.proxied,
          }));
          return;
        }
        // Not key-auth? Fall through to popup flow
      } catch (e) {
        reject(e);
        return;
      }
    }

    // OAuth / OIDC popup flow
    const popup = window.open(data.url, "serviceAuth", "width=520,height=720");

    const handler = (e) => {
      const msg = e.data;
      if (msg.state !== actualState) return;
      window.removeEventListener("message", handler);
      clearInterval(check);
      popup?.close();

      if (msg?.type !== "auth-complete") return;
      try {
        delete msg?.data?.refresh_token; // Kept in an HttpOnly cookie
        if (msg?.data?.expires_at) {
          msg.data.expires_at = new Date(msg.data.expires_at);
        }
        const proxy = new ServiceProxy({ serviceURL: msg.serviceURL, serviceID: msg.serviceID, data: msg.data, proxied: msg.data?.proxied });
        resolve(proxy);
      } catch (err) {
        reject(err);
      }
    };
    window.addEventListener("message", handler);

    const check = setInterval(() => {
      if (popup?.closed) {
        clearInterval(check);
        window.removeEventListener("message", handler);
        reject(new Error("Popup closed"));
      }
    }, 500);
  });
}

export class ServiceProxy extends EventEmitter {
  #serviceURL;
  #serviceID;
  #data;
  #apiKey;
  #apiHeader;
  #proxied;
  #authService;
  #client = null;
  #refreshPromise = null;

  /**
   * @param {Object} opts
   * @param {string} opts.serviceURL   — registered service URL
   * @param {number} [opts.serviceID]  — known service id (from prior loginPopup)
   * @param {Object} [opts.data]       — OAuth credentials from callback
   * @param {string} [opts.apiKey]     — API key for key-auth services
   * @param {string} [opts.apiHeader]  — header name for the API key (from ServiceDescriptor.Header)
   * @param {boolean} [opts.proxied]   — whether to route through freshbreath proxy
   */
  constructor({ serviceURL, serviceID, data, apiKey, apiHeader, proxied, authService }) {
    if (!serviceURL) {
      throw new Error("ServiceProxy requires serviceURL");
    }
    super();
    this.#serviceURL = serviceURL ?? null;
    this.#serviceID = serviceID ?? null;
    this.#data = data ?? null;
    this.#apiKey = apiKey ?? null;
    this.#apiHeader = apiHeader || null;
    this.#proxied = proxied ?? false;
    this.#authService = authService ?? null;

    for (const [event, handler] of ServiceProxy._defaults) {
      this.on(event, handler);
    }
  }

  get serviceURL() { return this.#serviceURL; }
  get serviceID()  { return this.#serviceID; }
  get data()       { return this.#data; }
  get apiKey()     { return this.#apiKey; }
  get apiHeader()  { return this.#apiHeader; }
  get proxied()    { return this.#proxied; }
  get authService() { return this.#authService; }

  static fromJSON(jsonString) {
    let data = JSON.parse(jsonString, (key, value) => {
      if (typeof value === 'string' && /^\d{4}-\d{2}-\d{2}T/.test(value)) {
        return new Date(value);
      }
      return value;
    });
    return new ServiceProxy(data);
  }

  toJSON() {
    return JSON.stringify({
      serviceURL: this.#serviceURL,
      serviceID: this.#serviceID,
      data: this.#data,
      apiKey: this.#apiKey,
      apiHeader: this.#apiHeader,
      proxied: this.#proxied,
    });
  }

  //
  // Default event handlers for all ServiceProxy instances (e.g. for token refresh)
  //
  static _defaults = new Map();

  static on(event, handler) {
    this._defaults.set(event, handler);
  }

  #isExpired() {
    const exp = this.#data?.expires_at;
    if (!exp) return false;
    return Date.now() > exp.getTime() - 300_000;
  }

  // Returns true if this ServiceProxy was obtained via OIDC identity login
  // (has claims + id_token from an IdP, not an MCP/API service).
  get isIdentity() {
    return !!this.#data?.claims && !!this.#data?.id_token;
  }

  async checkToken() {
    if (this.#isExpired()) {
      await this.refresh();
    }
  }

  // Returns true if we're maintaining the auth and can refresh.
  addAuth(headers) {
    if (this.#authService) {
      return this.#authService.addAuth(headers);
    }
    if (this.#apiKey) {
      if (this.#apiHeader) {
        headers.set(this.#apiHeader, this.#apiKey);
      } else {
        headers.set('Authorization', `Bearer ${this.#apiKey}`);
      }
      return false
    } else if (this.#data) {
      headers.set('Authorization', `${this.#data.token_type || "Bearer"} ${this.#data.access_token}`);
      return true
    }
    return false
  }

  async refresh() {
    if (this.#refreshPromise) return this.#refreshPromise;
    this.#refreshPromise = this.#doRefresh();
    try {
      return await this.#refreshPromise;
    } finally {
      this.#refreshPromise = null;
    }
  }

  async #doRefresh() {
    let t;
    if (this.#authService) {
      // Delegate refresh to the authService (e.g. OIDC identity)
      try {
        return await this.#authService.refresh();
      } catch (e) {
        // Auth service refresh failed — auto re-login
        const fresh = await login(this);
        this.#adoptCredentials(fresh);
        return;
      }
    } else if (isFreshbreathToken(this.#data?.access_token)) {
      // Freshbreath-issued token — refresh through /oauth/token.
      // The refresh token is in an HttpOnly cookie auto-attached to this path.
      // The path is scoped by service id so that refresh tokens for several
      // services live in separate cookie slots instead of overwriting each
      // other; the cookie's Path is set by the server to match.
      const body = new URLSearchParams({ grant_type: "refresh_token" });
      const r = await fetch(`${API}/oauth/token/${this.#serviceID}`, {
        method: "POST",
        headers: {
          "Content-Type": "application/x-www-form-urlencoded",
          "X-App-Nonce": APP_NONCE
        },
        credentials: "include",
        body,
      });
      if (!r.ok) {
        if (r.status === 400) {
          // Refresh token expired or invalid — auto re-login
          const fresh = await login(this);
          this.#adoptCredentials(fresh);
          return;
        }
        throw new Error(`Token refresh failed (${r.status})`);
      }
      t = await r.json();
      delete t?.refresh_token; // Kept in an HttpOnly cookie
    } else if (!this.#data?.refresh_token) {
      // No refresh token — auto re-login
      const fresh = await login(this);
      this.#adoptCredentials(fresh);
      return;
    } else if (this.#proxied && this.#serviceID) {
      // Proxied refresh: server-side with stored client_secret
      const body = {
        refresh_token: this.#data.refresh_token,
        service_id: this.#serviceID,
      };
      if (this.#data.client_id) body.client_id = this.#data.client_id;
      if (this.#data.token_endpoint) body.token_endpoint = this.#data.token_endpoint;

      const r = await fetch(`${API}/service/refresh`, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "X-App-Nonce": APP_NONCE
        },
        body: JSON.stringify(body),
      });
      if (!r.ok) {
        if (r.status === 400) {
          // Refresh token expired — auto re-login
          const fresh = await login(this);
          this.#adoptCredentials(fresh);
          return;
        }
        throw new Error(`Token refresh failed (${r.status})`);
      }
      t = await r.json();
    } else {
      // Direct refresh: full OAuth client credentials in body
      const body = new URLSearchParams({
        grant_type: "refresh_token",
        refresh_token: this.#data.refresh_token,
        client_id: this.#data.client_id,
      });
      if (this.#data.scopes) {
        const scopes = this.#data.scopes.split(" ");
        if (this.#data.id_token && !scopes.includes("openid")) {
          scopes.unshift("openid");
        }
        body.set("scope", scopes.join(" "));
      } else if (this.#data.id_token) {
        body.set("scope", "openid");
      }
      const r = await fetch(this.#data.token_endpoint, {
        method: "POST",
        headers: { "Content-Type": "application/x-www-form-urlencoded" },
        body,
      });
      if (!r.ok) {
        if (r.status === 400) {
          // Refresh token expired — auto re-login
          const fresh = await login(this);
          this.#adoptCredentials(fresh);
          return;
        }
        throw new Error(`Token refresh failed (${r.status})`);
      }
      t = await r.json();
    }

    this.#data = {
      ...this.#data,
      access_token: t.access_token,
      refresh_token: t.refresh_token ?? this.#data.refresh_token,
      id_token:     t.id_token ?? this.#data.id_token,
      expires_at:   t.expires_in ? new Date(Date.now() + (t.expires_in * 1000)) : null,
    };
    this.emit("refresh", this);
  }

  // Adopt credentials from a fresh ServiceProxy (after re-login)
  #adoptCredentials(fresh) {
    this.#data = fresh.#data;
    this.#apiKey = fresh.#apiKey;
    this.#apiHeader = fresh.#apiHeader;
    this.#serviceID = fresh.#serviceID;
    this.#proxied = fresh.#proxied;
    this.emit("refresh", this);
  }

  async connect() {
    if (this.isIdentity) {
      throw new Error("This ServiceProxy was obtained from an OIDC identity provider. Use .data.claims instead of MCP methods.");
    }
    if (this.#serviceURL?.startsWith('tasks://')) {
      throw new Error("Tasks services don't use MCP connect. Use listTools/callTool directly.");
    }
    await this.checkToken();
    const headers = new Headers({});
    this.addAuth(headers);
    const transport = new StreamableHTTPClientTransport(new URL(this.#serviceURL), {
      requestInit: { headers },
    });
    this.#client = new McpClient({ name: "mcp-client", version: "1.0.0" });
    await this.#client.connect(transport);
  }

  async #withReconnect(fn) {
    try {
      if (!this.#client) {
        await this.connect();
      }
      return await fn();
    } catch (e) {
      if (!String(e).includes("401")) throw e;
      await this.refresh();
      await this.connect();
      return await fn();
    }
  }

  async #fetch(url, init = {}) {
    const headers = new Headers(init.headers);
    headers.set('X-App-Nonce', APP_NONCE);
    const canRefresh = this.addAuth(headers);
    let res = await fetch(url, { ...init, headers });
    if (canRefresh && res.status === 401) {
      await this.refresh();
      this.addAuth(headers);
      res = await fetch(url, { ...init, headers });
    }
    return res;
  }

  // Returns the slug for task or virtual services, or null.
  #serviceSlug() {
    if (this.#serviceURL?.startsWith('tasks://')) {
      return this.#serviceURL.replace('tasks://', '');
    }
    if (this.#serviceURL?.startsWith('/mcp/')) {
      return this.#serviceURL.replace('/mcp/', '');
    }
    return null;
  }

  async listTools() {
    const slug = this.#serviceSlug();
    if (slug) {
      const r = await this.#fetch(`${API}/service/call/${slug}`);
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
    const result = this.#serviceSlug() ?
      await this.#callTask(name, args) :
      await this.#withReconnect(() => this.#client.callTool({ name, arguments: args }));
    const text = result.content
      .filter(c => c.type === 'text')
      .map(c => c.text)
      .join('\n');
    if (result.isError) throw new Error(`Tool error: ${text}`);
    try {
      return JSON.parse(text);
    } catch {
      return text;
    }
  }

  /**
   * Call a tasks-service tool. If any arg value is a File or Blob,
   * the request is sent as multipart/form-data with file uploads.
   * All other args are JSON-serialized into a single "args" field.
   */
  async #callTask(name, args) {
    const hasFiles = Object.values(args).some(v => v instanceof File || v instanceof Blob);
    const slug = this.#serviceSlug();
    const url = `${API}/service/call/${slug}`;

    if (hasFiles) {
      const fd = new FormData();
      fd.append('task', name);
      for (const [k, v] of Object.entries(args)) {
        if (v instanceof File || v instanceof Blob) {
          fd.append(k, v, v instanceof File ? v.name : k);
        } else {
          fd.append(k, JSON.stringify(v));
        }
      }
      const r = await this.#fetch(url, { method: 'POST', body: fd });
      if (!r.ok) {
        const text = await r.text();
        throw new Error(`Task call failed (${r.status}): ${text}`);
      }
      return r.json();
    }

    const headers = {'Content-Type': 'application/json'};
    const r = await this.#fetch(url, {
      method: 'POST',
      headers,
      body: JSON.stringify({ task: name, args }),
    });
    if (!r.ok) {
      const text = await r.text();
      throw new Error(`Task call failed (${r.status}): ${text}`);
    }
    return r.json();
  }

  /**
   * Make a proxied API call to the service.
   * For key-auth services with a server-side key, just pass the path.
   * For user-provided keys, the Authorization header is set automatically.
   * @param {string} path   — path relative to the service URL (e.g. "/v1/users")
   * @param {RequestInit} [init] — fetch options (method, body, headers, etc.)
   */
  async fetch(path, init = {}) {
    let url = `${this.#serviceURL}${path}`;

    if ((this.#proxied || window.location.protocol !== 'file:') && this.#serviceID) {
      url = `${API}/service/${this.#serviceID}/${path.replace(/^\//, "")}`;
    }

    return this.#fetch(url, init);
  }
}

export function load(jsonString) {
  return ServiceProxy.fromJSON(jsonString);
}

// ── Remote updates ──────────────────────────────────────────────
// Opt-in auto-update for hosted apps. The /check and /apply endpoints are
// anonymous and server-global (see design/remote-updates.md) — any hosted app
// can poll them. Nothing here runs unless the app calls autoUpdates().
//
// sseStream is the shared SSE-over-POST helper (fetch + ReadableStream;
// EventSource is GET-only). The control panel reuses it via window.FrBr.

export async function sseStream(path, { method = 'POST', body, token, onEvent } = {}) {
  const opts = { method, headers: { 'Accept': 'text/event-stream' } };
  if (body) { opts.headers['Content-Type'] = 'application/json'; opts.body = JSON.stringify(body); }
  if (token?.data?.access_token) opts.headers['Authorization'] = 'Bearer ' + token.data.access_token;
  const res = await fetch(path, opts);
  if (!res.ok) throw new Error(`${res.status}: ${(await res.text().catch(() => '')) || res.statusText}`);
  const reader = res.body.getReader();
  const dec = new TextDecoder();
  let buf = '';
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    buf += dec.decode(value, { stream: true });
    let idx;
    while ((idx = buf.indexOf('\n\n')) >= 0) {
      const block = buf.slice(0, idx);
      buf = buf.slice(idx + 2);
      let ev = 'message', data = '';
      for (const line of block.split('\n')) {
        if (line.startsWith('event: ')) ev = line.slice(7);
        else if (line.startsWith('data: ')) data += line.slice(6);
      }
      if (data) { try { onEvent && onEvent(ev, JSON.parse(data)); } catch {} }
    }
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
    login, load, ServiceProxy, Svc: ServiceProxy,
    sseStream, fetchUpdatesCheck, applyUpdates, autoUpdates, defaultUpdateBanner,
  };
}
