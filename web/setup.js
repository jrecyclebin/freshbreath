/**
 * Fresh Breath Service Proxy client SDK.
 *
 * Load /env.js first (injected by server), then /setup.js.
 *
 * User-provided API keys and OAuth tokens are stored in the browser's
 * localStorage. The server's OAuth client_secret is never exposed to
 * the client — token exchange and refresh run server-side.
 */
import { Client as McpClient } from "https://esm.sh/@modelcontextprotocol/sdk@1.29.0/client/index.js?deps=zod@3";
import { StreamableHTTPClientTransport } from "https://esm.sh/@modelcontextprotocol/sdk@1.29.0/client/streamableHttp.js?deps=zod@3";

const API = window.__HOMESLICE_CONFIG?.apiBase ?? "";

// ── static login flow ──
/**
  * Open an OAuth popup for the given service URL.
  * On success returns a ServiceProxy instance with token data loaded.
  */
export function login({ appNonce, serviceURL, state, apiKey }) {
  const actualState = state || crypto.randomUUID();
  return new Promise(async (resolve, reject) => {
    // If we already know it's key-auth (have an apiKey), skip the popup
    const url = `${API}/service/login?url=${encodeURIComponent(serviceURL)}&state=${encodeURIComponent(actualState)}`;
    const res = await fetch(url, { headers: { "X-App-Nonce": appNonce } });
    const data = await res.json();

    if (data.type !== "redirect") {
      try {
        if (data.type === "key-auth-complete") {
          resolve(new ServiceProxy({
            appNonce: data.appNonce,
            serviceURL: data.serviceURL,
            serviceID: data.serviceID,
            apiKey: apiKey || data.apiKey,
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
        const proxy = new ServiceProxy({ appNonce: msg.appNonce, serviceURL: msg.serviceURL, serviceID: msg.serviceID, data: msg.data, proxied: msg.data?.proxied });
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

export class ServiceProxy {
  #appNonce;
  #serviceURL;
  #serviceID;
  #data;
  #apiKey;
  #proxied;
  #client = null;

  /**
   * @param {Object} opts
   * @param {string} opts.appNonce    — app identifier from /api/apps
   * @param {string} opts.serviceURL  — registered service URL
   * @param {number} [opts.serviceID] — known service id (from prior loginPopup)
   * @param {Object} [opts.data]      — OAuth credentials from callback
   * @param {string} [opts.apiKey]    — API key for key-auth services
   * @param {boolean} [opts.proxied]  — whether to route through freshbreath proxy
   */
  constructor({ appNonce, serviceURL, serviceID, data, apiKey, proxied }) {
    if (!appNonce || !serviceURL) {
      throw new Error("ServiceProxy requires appNonce and serviceURL");
    }
    this.#appNonce = appNonce;
    this.#serviceURL = serviceURL;
    this.#serviceID = serviceID ?? null;
    this.#data = data ?? null;
    this.#apiKey = apiKey ?? null;
    this.#proxied = proxied ?? false;
  }

  get appNonce()   { return this.#appNonce; }
  get serviceURL() { return this.#serviceURL; }
  get serviceID()  { return this.#serviceID; }
  get data()       { return this.#data; }
  get apiKey()     { return this.#apiKey; }
  get proxied()    { return this.#proxied; }
  toJSON() {
    return JSON.stringify({
      serviceURL: this.#serviceURL,
      serviceID: this.#serviceID,
      data: this.#data,
      apiKey: this.#apiKey,
      proxied: this.#proxied
    });
  }

  #isExpired() {
    const exp = this.#data?.expires_in;
    if (!exp) return false;
    const expiresAt = this.#data._expires_at;
    return expiresAt ? (Date.now() / 1000) > (expiresAt - 300) : false;
  }

  // Returns true if this ServiceProxy was obtained via OIDC identity login
  // (has claims + id_token from an IdP, not an MCP/API service).
  get isIdentity() {
    return !!this.#data?.claims && !!this.#data?.id_token;
  }

  async #refresh() {
    if (!this.#data?.refresh_token) {
      throw new Error("No refresh token available — re-login required");
    }
    const body = new URLSearchParams({
      grant_type: "refresh_token",
      refresh_token: this.#data.refresh_token,
      client_id: this.#data.client_id,
    });
    // Preserve original scopes; if this was an OIDC flow, ensure openid is included
    // so the provider continues to return an id_token.
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
    if (!r.ok) throw new Error(`Token refresh failed (${r.status})`);
    const t = await r.json();
    this.#data = {
      ...this.#data,
      access_token:  t.access_token,
      refresh_token: t.refresh_token ?? this.#data.refresh_token,
      token_type:    t.token_type ?? this.#data.token_type,
      id_token:      t.id_token ?? this.#data.id_token,
      expires_in:    t.expires_in,
      _expires_at:   t.expires_in ? (Date.now() / 1000) + t.expires_in : undefined,
    };
  }

  async connect() {
    if (this.isIdentity) {
      throw new Error("This ServiceProxy was obtained from an OIDC identity provider. Use .data.claims instead of MCP methods.");
    }
    if (this.#isExpired()) await this.#refresh();
    const transport = new StreamableHTTPClientTransport(new URL(this.#serviceURL), {
      requestInit: { headers: { Authorization: `${this.#data.token_type || "Bearer"} ${this.#data.access_token}` } },
    });
    this.#client = new McpClient({ name: "mcp-client", version: "1.0.0" });
    await this.#client.connect(transport);
  }

  async #withRetry(fn) {
    try {
      if (!this.#client) {
        await this.connect();
      }
      return await fn();
    } catch (e) {
      if (!String(e).includes("401")) throw e;
      await this.#refresh();
      await this.connect();
      return await fn();
    }
  }

  async listTools() {
    return this.#withRetry(async () => {
      const { tools } = await this.#client.listTools();
      return tools;
    });
  }

  async callTool(name, args = {}) {
    return this.#withRetry(() => this.#client.callTool({ name, arguments: args }));
  }

  /**
   * Make a proxied API call to the service.
   * For key-auth services with a server-side key, just pass the path.
   * For user-provided keys, the Authorization header is set automatically.
   * @param {string} path   — path relative to the service URL (e.g. "/v1/users")
   * @param {RequestInit} [init] — fetch options (method, body, headers, etc.)
   */
  async fetch(path, init = {}) {
    const headers = new Headers(init.headers || {});
    headers.set("X-App-Nonce", this.#appNonce);
    if (this.#apiKey) {
      headers.set("X-Api-Key", this.#apiKey);
    }

    if ((this.#proxied || window.location.protocol !== 'file:') && this.#serviceID) {
      const url = `${API}/service/${this.#serviceID}/${path.replace(/^\//, "")}`;
      return fetch(url, { ...init, headers });
    }

    // Non-proxied — call the service directly
    const url = `${this.#serviceURL}${path}`;
    return fetch(url, { ...init, headers });
  }
}

export default ServiceProxy;
