/**
 * Fresh Breath Service Proxy client SDK.
 *
 * User-provided API keys and OAuth tokens are stored in the browser's
 * localStorage. The server's OAuth client_secret is never exposed to
 * the client — token exchange and refresh run server-side.
 */
import { Client as McpClient } from "https://esm.sh/@modelcontextprotocol/sdk@1.29.0/client/index.js?deps=zod@3";
import { StreamableHTTPClientTransport } from "https://esm.sh/@modelcontextprotocol/sdk@1.29.0/client/streamableHttp.js?deps=zod@3";
import EventEmitter from 'https://esm.sh/eventemitter3';

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

export class ServiceProxy extends EventEmitter {
  #appNonce;
  #serviceURL;
  #serviceID;
  #data;
  #apiKey;
  #apiHeader;
  #proxied;
  #client = null;

  /**
   * @param {Object} opts
   * @param {string} opts.appNonce     — app identifier from /api/apps
   * @param {string} opts.serviceURL   — registered service URL
   * @param {number} [opts.serviceID]  — known service id (from prior loginPopup)
   * @param {Object} [opts.data]       — OAuth credentials from callback
   * @param {string} [opts.apiKey]     — API key for key-auth services
   * @param {string} [opts.apiHeader]  — header name for the API key (from ServiceDescriptor.Header)
   * @param {boolean} [opts.proxied]   — whether to route through freshbreath proxy
   */
  constructor({ appNonce, serviceURL, serviceID, data, apiKey, apiHeader, proxied }) {
    if (!appNonce || !serviceURL) {
      throw new Error("ServiceProxy requires appNonce and serviceURL");
    }
    super();
    this.#appNonce = appNonce;
    this.#serviceURL = serviceURL;
    this.#serviceID = serviceID ?? null;
    this.#data = data ?? null;
    this.#apiKey = apiKey ?? null;
    this.#apiHeader = apiHeader || null;
    this.#proxied = proxied ?? false;

    for (const [event, handler] of ServiceProxy._defaults) {
      this.on(event, handler);
    }
  }

  get appNonce()   { return this.#appNonce; }
  get serviceURL() { return this.#serviceURL; }
  get serviceID()  { return this.#serviceID; }
  get data()       { return this.#data; }
  get apiKey()     { return this.#apiKey; }
  get apiHeader()  { return this.#apiHeader; }
  get proxied()    { return this.#proxied; }

  static fromJSON(appNonce, jsonString) {
    let data = JSON.parse(jsonString, (key, value) => {
      if (typeof value === 'string' && /^\d{4}-\d{2}-\d{2}T/.test(value)) {
        return new Date(value);
      }
      return value;
    });
    return new ServiceProxy({ appNonce, ...data });
  }

  toJSON() {
    return JSON.stringify({
      serviceURL: this.#serviceURL,
      serviceID: this.#serviceID,
      data: this.#data,
      apiKey: this.#apiKey,
      apiHeader: this.#apiHeader,
      proxied: this.#proxied
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

  async refresh() {
    if (!this.#data?.refresh_token) {
      throw new Error("No refresh token available — re-login required");
    }

    let t;
    if (this.#proxied && this.#serviceID) {
      // Proxied refresh: server-side with stored client_secret
      const body = {
        refresh_token: this.#data.refresh_token,
        service_id: this.#serviceID,
      };
      // For DCR services, the server never persisted our client_id.
      // Pre-registered services don't need this (it's in the descriptor).
      if (this.#data.client_id) body.client_id = this.#data.client_id;
      // token_endpoint is optional — server resolves from OAuthURL or metadata.
      // Sending it lets the server validate we're not being redirected.
      if (this.#data.token_endpoint) body.token_endpoint = this.#data.token_endpoint;

      const r = await fetch(`${API}/service/refresh`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      if (!r.ok) throw new Error(`Token refresh failed (${r.status})`);
      t = await r.json();
    } else {
      // Direct refresh: full OAuth client credentials in body
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
      t = await r.json();
    }

    this.#data = {
      ...this.#data,
      access_token:  t.access_token,
      refresh_token: t.refresh_token ?? this.#data.refresh_token,
      id_token:      t.id_token ?? this.#data.id_token,
      expires_at:    t.expires_in ? new Date(Date.now() + (t.expires_in * 1000)) : null,
    };
    this.emit("refresh", this);
  }

  async connect() {
    if (this.isIdentity) {
      throw new Error("This ServiceProxy was obtained from an OIDC identity provider. Use .data.claims instead of MCP methods.");
    }
    await this.checkToken();
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
      await this.refresh();
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

    if ((this.#proxied || window.location.protocol !== 'file:') && this.#serviceID) {
      // Proxied — nonce required; server handles key injection
      headers.set("X-App-Nonce", this.#appNonce);
      if (this.#apiKey) headers.set("X-Api-Key", this.#apiKey);
      const url = `${API}/service/${this.#serviceID}/${path.replace(/^\//, "")}`;
      return fetch(url, { ...init, headers });
    }

    // Non-proxied — call the service directly; apply the configured auth header
    if (this.#apiKey) {
      if (this.#apiHeader) {
        headers.set(this.#apiHeader, this.#apiKey);
      } else {
        headers.set("Authorization", "Bearer " + this.#apiKey);
      }
    }
    const url = `${this.#serviceURL}${path}`;
    return fetch(url, { ...init, headers });
  }
}

export function load(appNonce, jsonString) {
  return ServiceProxy.fromJSON(appNonce, jsonString);
}

export default ServiceProxy;

// Expose to window for non-module consumers (e.g. the admin panel)
if (typeof window !== 'undefined') {
  window.FreshBreath = { login, load, ServiceProxy };
}
