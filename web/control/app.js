// Fresh Breath Control Panel — admin SPA

const { useState, useEffect, useCallback, useRef, createContext, useContext } = React;

// ── Icons ──────────────────────────────────────────────────────────────

const Icon = ({ name, size = 16 }) => {
  const s = "currentColor";
  const w = 1.5;
  const c = { width: size, height: size, viewBox: "0 0 24 24", fill: "none", stroke: s, strokeWidth: w, strokeLinecap: "round", strokeLinejoin: "round" };
  const p = {
    home:    <><path d="M3 11l9-7 9 7v9a1 1 0 0 1-1 1h-5v-6h-6v6H4a1 1 0 0 1-1-1z"/></>,
    users:   <><circle cx="9" cy="8" r="3.5"/><path d="M2.5 19c.5-3.5 3.2-5 6.5-5s6 1.5 6.5 5"/><path d="M16 4.5a3.5 3.5 0 0 1 0 7"/><path d="M21.5 19c-.3-2.6-1.8-4.1-4-4.7"/></>,
    apps:    <><rect x="3.5" y="3.5" width="7" height="7" rx="1.5"/><rect x="13.5" y="3.5" width="7" height="7" rx="1.5"/><rect x="3.5" y="13.5" width="7" height="7" rx="1.5"/><rect x="13.5" y="13.5" width="7" height="7" rx="1.5"/></>,
    shield:  <><path d="M12 3l8 3v6c0 5-3.5 8-8 9-4.5-1-8-4-8-9V6z"/></>,
    log:     <><path d="M5 4h11l3 3v13a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1V5a1 1 0 0 1 1-1z"/><path d="M8 11h8M8 15h8M8 7h5"/></>,
    plus:    <><path d="M12 5v14M5 12h14"/></>,
    search:  <><circle cx="11" cy="11" r="7"/><path d="M21 21l-4.3-4.3"/></>,
    close:   <><path d="M6 6l12 12M18 6L6 18"/></>,
    edit:    <><path d="M14 4l6 6"/><path d="M4 20l5-1L20 8l-5-5L4 14z"/></>,
    trash:   <><path d="M4 7h16M9 7V4h6v3M6 7l1 13a1 1 0 0 0 1 1h8a1 1 0 0 0 1-1l1-13"/></>,
    check:   <><path d="M5 12l5 5L20 7"/></>,
    more:    <><circle cx="5" cy="12" r="1.4"/><circle cx="12" cy="12" r="1.4"/><circle cx="19" cy="12" r="1.4"/></>,
    download:<><path d="M12 4v12M7 11l5 5 5-5M5 20h14"/></>,
    filter:  <><path d="M4 5h16l-6 8v6l-4-2v-4z"/></>,
    sort:    <><path d="M7 4v16M3 8l4-4 4 4"/><path d="M17 20V4M13 16l4 4 4-4"/></>,
    copy:    <><rect x="8" y="8" width="12" height="12" rx="2"/><path d="M16 8V5a1 1 0 0 0-1-1H5a1 1 0 0 0-1 1v10a1 1 0 0 0 1 1h3"/></>,
    sparkle: <><path d="M12 4l1.8 4.5L18 10l-4.2 1.5L12 16l-1.8-4.5L6 10l4.2-1.5z"/><path d="M19 4v3M19 17v3M5 17v3M5 4v3"/></>,
    bell:    <><path d="M6 9a6 6 0 0 1 12 0c0 5 2 6 2 7H4c0-1 2-2 2-7z"/><path d="M10 19a2 2 0 0 0 4 0"/></>,
    refresh: <><path d="M3 12a9 9 0 0 1 15-6.7L21 8"/><path d="M21 3v5h-5"/><path d="M21 12a9 9 0 0 1-15 6.7L3 16"/><path d="M3 21v-5h5"/></>,
    lock:    <><rect x="5" y="11" width="14" height="9" rx="2"/><path d="M8 11V8a4 4 0 0 1 8 0v3"/></>,
    mail:    <><rect x="3" y="5" width="18" height="14" rx="2"/><path d="M3 7l9 6 9-6"/></>,
    cog:     <><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.8-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1-1.5 1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.7 1.7 0 0 0 1.5-1 1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3 1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.8 1.7 1.7 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z"/></>,
    plug:    <><path d="M12 22v-5"/><path d="M9 8V2"/><path d="M15 8V2"/><path d="M18 8v5a6 6 0 0 1-12 0V8z"/></>,
    signout: <><path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4"/><polyline points="16 17 21 12 16 7"/><line x1="21" y1="12" x2="9" y2="12"/></>,
    menu:    <><path d="M3 6h18M3 12h18M3 18h18"/></>,
    moon:    <><path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z"/></>,
    sun:     <><circle cx="12" cy="12" r="5"/><path d="M12 1v2M12 21v2M4.22 4.22l1.42 1.42M18.36 18.36l1.42 1.42M1 12h2M21 12h2M4.22 19.78l1.42-1.42M18.36 5.64l1.42-1.42"/></>,
  };
  return <svg {...c}>{p[name] || p.more}</svg>;
};

// ── UI primitives ──────────────────────────────────────────────────────

const AVATAR_HUES = [20, 60, 110, 150, 200, 240, 280, 320];
const initls = (n) => n?.split(/\s+/).map(s=>s[0]).slice(0,2).join('').toUpperCase() || '??';
const hashHue = (s='') => { let h=0; for(let i=0;i<s.length;i++)h=(h*31+s.charCodeAt(i))>>>0; return AVATAR_HUES[h%AVATAR_HUES.length]; };

const Avatar = ({ name, size = 32 }) => {
  const hue = hashHue(name);
  const bg = `linear-gradient(135deg, oklch(0.78 0.07 ${hue}), oklch(0.55 0.1 ${(hue+30)%360}))`;
  return <div className="avatar" style={{width:size,height:size,fontSize:size*0.36,background:bg}}>{initls(name)}</div>;
};

const Badge = ({ tone = "gray", dot = true, children }) => (
  <span className={`badge ${tone}`}>{dot && <span className="dot"/>}{children}</span>
);

const statusTone = (s='') => ({ Active:'green', Invited:'blue', Suspended:'red' }[s] || 'gray');
const envTone = (e='') => ({ Production:'green', Staging:'amber', Development:'blue' }[e] || 'gray');
const roleTone = (r='') => ({ Superuser:'violet', Admin:'blue', Member:'gray', 'Read-only':'gray' }[r] || 'gray');

const actionIcon = (a='') => {
  const x = a.toLowerCase();
  if (x.includes('login')||x.includes('sign in')) return {icon:'lock',tone:'blue'};
  if (x.includes('created')) return {icon:'plus',tone:'green'};
  if (x.includes('deleted')||x.includes('removed')) return {icon:'trash',tone:'red'};
  if (x.includes('updated')||x.includes('edited')) return {icon:'edit',tone:'gray'};
  if (x.includes('role')) return {icon:'shield',tone:'violet'};
  return {icon:'cog',tone:'gray'};
};

const Drawer = ({ open, title, onClose, footer, children }) => {
  useEffect(() => {
    const esc = (e) => { if(e.key==='Escape')onClose(); };
    if(open) window.addEventListener('keydown',esc);
    return () => window.removeEventListener('keydown',esc);
  }, [open,onClose]);
  return <>
    <div className={`drawer-scrim ${open?'open':''}`} onClick={onClose}/>
    <div className={`drawer ${open?'open':''}`} role="dialog" aria-modal="true">
      <div className="drawer-head"><h3>{title}</h3><button className="btn btn-icon btn-ghost" onClick={onClose}><Icon name="close" size={16}/></button></div>
      <div className="drawer-body">{children}</div>
      {footer && <div className="drawer-foot">{footer}</div>}
    </div>
  </>;
};

// ── MultiSelect ────────────────────────────────────────────────────────

const MultiSelect = ({ options, value = [], onChange, placeholder = 'Select…' }) => {
  const [open,setOpen] = useState(false);
  const ref = useRef(null);

  useEffect(()=>{
    const handler = (e) => { if(ref.current && !ref.current.contains(e.target)) setOpen(false); };
    document.addEventListener('mousedown',handler);
    return () => document.removeEventListener('mousedown',handler);
  },[]);

  const sel = new Set(value);

  const toggle = (val) => {
    const next = new Set(sel);
    if(next.has(val)) next.delete(val); else next.add(val);
    onChange([...next]);
  };

  const remove = (val,e) => {
    e.stopPropagation();
    onChange(value.filter(v=>v!==val));
  };

  return (
    <div className="multiselect" ref={ref}>
      <div className="multiselect-control" onClick={()=>setOpen(o=>!o)}>
        <div className="multiselect-tags">
          {value.length === 0
            ? <span className="multiselect-placeholder">{placeholder}</span>
            : value.map(val=>{
                const opt = options.find(o=>o.value===val);
                return (
                  <span key={val} className="multiselect-tag">
                    {opt?.label??val}
                    <button type="button" className="multiselect-tag-remove" onClick={e=>remove(val,e)}>
                      <Icon name="close" size={9}/>
                    </button>
                  </span>
                );
              })
          }
        </div>
        <span className="multiselect-chevron"><Icon name="sort" size={12}/></span>
      </div>
      {open && (
        <div className="multiselect-dropdown">
          {options.length === 0
            ? <div className="multiselect-empty">No options available</div>
            : options.map(opt=>{
                const isSelected = sel.has(opt.value);
                return (
                  <div key={opt.value} className={`multiselect-option${isSelected?' selected':''}`} onClick={()=>toggle(opt.value)}>
                    <span className="multiselect-check">{isSelected && <Icon name="check" size={12}/>}</span>
                    {opt.label}
                  </div>
                );
              })
          }
        </div>
      )}
    </div>
  );
};

// Toast
const ToastCtx = createContext(()=>{});
const useToast = () => useContext(ToastCtx);
const ToastProvider = ({children}) => {
  const [toasts,setToasts] = useState([]);
  const push = useCallback((msg,err) => {
    const id = Math.random().toString(36).slice(2,8);
    setToasts(t=>[...t,{id,msg,err}]);
    setTimeout(()=>setToasts(t=>t.filter(x=>x.id!==id)), 2800);
  },[]);
  return (
    <ToastCtx.Provider value={push}>
      {children}
      <div className="toast-wrap">
        {toasts.map(t => (
          <div key={t.id} className={`toast ${t.err?'toast-error':''}`}>
            <span className="check"><Icon name={t.err?'close':'check'} size={10}/></span>
            {t.msg}
          </div>
        ))}
      </div>
    </ToastCtx.Provider>
  );
};

// ── Auth ───────────────────────────────────────────────────────────────

const AuthCtx = createContext({ user: null, token: null, authRequired: false, serviceName: '', sessionExpired: false, login: ()=>{}, logout: ()=>{}, clearExpired: ()=>{} });
const useAuth = () => useContext(AuthCtx);

function AuthProvider({ children }) {
  const [ready, setReady] = useState(false);
  const [user, setUser] = useState(null);
  const [token, setToken] = useState(null);
  const [authRequired, setAuthRequired] = useState(false);
  const [serviceName, setServiceName] = useState('');
  const [sessionExpired, setSessionExpired] = useState(false);
  const [authError, setAuthError] = useState('');

  useEffect(() => {
    _onUnauthorized = () => { setUser(null); setToken(null); setSessionExpired(true); };
    return () => { _onUnauthorized = null; };
  }, []);

  useEffect(() => {
    (async () => {
      try {
        const cfg = window.__HOMESLICE_CONFIG || {};
        window.FreshBreath.ServiceProxy.on('refresh', rtoken => {
          console.log("Access token expired, refreshing...");
          localStorage.setItem('frebre_admin', rtoken.toJSON());
          setToken(rtoken);
        });

        if (!cfg.authRequired) { setReady(true); return; }
        setAuthRequired(true);
        setServiceName(cfg.authServiceName || '');

        const stored = localStorage.getItem('frebre_admin');
        if (stored) {
          const appNonce = cfg.adminNonce;
          const result = window.FreshBreath.load(appNonce, stored);
          setToken(result);

          const d = await api(result, 'GET','/api/me');
          setUser(d.user);
        }
      } catch {}
      setReady(true);
    })();
  }, []);

  const login = async () => {
    const cfg = window.__HOMESLICE_CONFIG || {};
    const appNonce = cfg.adminNonce;
    const serviceURL = cfg.authServiceURL;
    if (!appNonce || !serviceURL) return;

    const result = await window.FreshBreath.login({ appNonce, serviceURL });
    if (result.appNonce !== appNonce) return;
    setSessionExpired(false);
    localStorage.setItem('frebre_admin', result.toJSON());
    setToken(result);

    // Verify the token and load user
    const d = await api(result, 'GET', '/api/me');
    setUser(d.user);
  };

  const logout = () => { localStorage.removeItem('frebre_admin'); setUser(null); setSessionExpired(false); };
  const clearExpired = () => setSessionExpired(false);

  if (!ready) return <div style={{display:'grid',placeItems:'center',height:'100vh',color:'var(--ink-3)'}}>Loading…</div>;

  return (
    <AuthCtx.Provider value={{ user, token, authRequired, serviceName, sessionExpired, login, logout, clearExpired, authError }}>
      {children}
    </AuthCtx.Provider>
  );
}

function LoginScreen({ serviceName, onLogin, authError }) {
  const cfg = window.__HOMESLICE_CONFIG || {};
  const isSSH = cfg.authServiceType === 'ssh';
  const errorMessages = {
    no_user: 'Your account is not registered. Please contact an administrator.',
    invalid_token: 'Authentication failed. Please try again.',
    no_email: 'Your identity provider did not share an email address.',
  };
  return (
    <div className="login-screen">
      <aside className="login-aside">
        <div className="quiet-grid"/>
        <div className="login-aside-inner">
          <div className="login-brand">
            <span className="brand-mark"/>
            Fresh Breath
          </div>
          <h1 className="login-headline">
            A quieter place<br/>
            to run <em>your services.</em>
          </h1>
          <p className="login-sub">
            Manage users, applications, and auth across every service you operate — without leaving the calm.
          </p>
        </div>
        <div className="login-foot">
          <span>admin panel</span>
          <span>{window.__HOMESLICE_CONFIG?.version || 'dev'}</span>
        </div>
      </aside>
      <main className="login-main">
        <div className="login-card">
          <div>
            <h2>Sign in to Fresh Breath</h2>
            <p className="lead">{isSSH ? 'Sign in with your SSH key passphrase.' : 'Use your work account to access the control panel.'}</p>
          </div>
          {authError && (
            <div className="login-error">
              <Icon name="bell" size={14}/>
              <span>{errorMessages[authError] || 'Authentication error.'}</span>
            </div>
          )}
          <button className="oidc-btn oidc-primary" onClick={onLogin}>
            <span className="glyph"><Icon name={isSSH ? 'lock' : 'lock'} size={16}/></span>
            {isSSH ? 'Sign in with SSH key' : `Continue with ${serviceName || 'your identity provider'}`}
            <span className="meta">{isSSH ? 'SSH' : 'OIDC'}</span>
          </button>
        </div>
      </main>
    </div>
  );
}

function SessionBanner({ onLogin, onDismiss }) {
  return (
    <div className="session-banner">
      <Icon name="lock" size={14}/>
      <span>Your session has expired.</span>
      <button className="btn btn-sm btn-primary" onClick={onLogin}>Sign in again</button>
      <button className="btn btn-icon btn-ghost" style={{marginLeft:'auto',color:'inherit'}} onClick={onDismiss}>
        <Icon name="close" size={12}/>
      </button>
    </div>
  );
}

// ── Nav ────────────────────────────────────────────────────────────────

const NAV = [
  { id:'home',     label:'Overview', icon:'home' },
  { id:'apps',     label:'Apps',     icon:'apps', countKey:'apps' },
  { id:'services', label:'Services', icon:'plug', countKey:'services' },
  { id:'users',    label:'Users',    icon:'users', countKey:'users' },
  { id:'roles',    label:'Roles',    icon:'shield' },
  { id:'audit',    label:'Audit log',icon:'log' },
  { id:'settings', label:'Settings', icon:'cog' },
];

function MobileTopBar({ onMenuOpen, pageLabel }) {
  return (
    <div className="mobile-topbar">
      <div className="mb-brand">
        <span className="brand-mark"/>
        Fresh Breath
      </div>
      <div style={{display:'flex',alignItems:'center',gap:8}}>
        {pageLabel && <span className="mb-page">{pageLabel}</span>}
        <button className="btn btn-icon btn-ghost" onClick={onMenuOpen} aria-label="Open menu">
          <Icon name="menu" size={18}/>
        </button>
      </div>
    </div>
  );
}

function Sidebar({ active, onNav, counts, user, onLogout, mobileOpen, onMobileClose }) {
  const workspace = NAV.slice(0,4);
  const security  = NAV.slice(4);
  const displayName = user?.name || 'Admin';
  const displayRole = user?.role || 'Superuser';
  const [dark, setDark] = useState(() => document.documentElement.dataset.theme === 'dark');
  useEffect(() => {
    const obs = new MutationObserver(() => setDark(document.documentElement.dataset.theme === 'dark'));
    obs.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] });
    return () => obs.disconnect();
  }, []);
  const toggleTheme = () => {
    const next = document.documentElement.dataset.theme === 'dark' ? 'light' : 'dark';
    document.documentElement.dataset.theme = next;
    localStorage.setItem('frebre_theme', next);
    setDark(next === 'dark');
  };
  const handleNav = (id) => { onNav(id); onMobileClose?.(); };
  return (
    <aside className={`sidebar${mobileOpen ? ' mobile-open' : ''}`}>
      <div className="sb-brand">
        <span style={{display:'flex',alignItems:'center',gap:10}}><span className="brand-mark"/>Fresh Breath</span>
        <button className="theme-toggle" onClick={toggleTheme} title={dark ? 'Switch to light' : 'Switch to dark'}>
          <Icon name={dark ? 'sun' : 'moon'} size={16}/>
        </button>
      </div>
      <div>
        <div className="sb-section">Workspace</div>
        <div className="sb-nav">
          {workspace.map(n=>NavLink(n,active,handleNav,counts))}
        </div>
      </div>
      <div>
        <div className="sb-section">Security</div>
        <div className="sb-nav">
          {security.map(n=>NavLink(n,active,handleNav,counts))}
        </div>
      </div>
      <div className="sb-foot">
        <div style={{display:'flex',alignItems:'center',gap:4}}>
          <div className="sb-user" style={{flex:1}} title={displayName}>
            <Avatar name={displayName} size={28}/>
            <div className="sb-user-text"><b>{displayName}</b><span>{displayRole}</span></div>
          </div>
          {onLogout && (
            <button className="btn btn-icon btn-ghost" onClick={onLogout} title="Sign out" style={{flexShrink:0}}>
              <Icon name="signout" size={14}/>
            </button>
          )}
        </div>
        <div className="sb-version" title={window.__HOMESLICE_CONFIG?.commit || 'none'}>
          {window.__HOMESLICE_CONFIG?.version || 'dev'}
        </div>
      </div>
    </aside>
  );
}

function NavLink(n,active,onNav,counts) {
  return (
    <button key={n.id} className={`sb-link ${active===n.id?'active':''}`} onClick={()=>onNav(n.id)}>
      <span className="icn"><Icon name={n.icon}/></span>
      {n.label}
      {n.countKey && counts[n.countKey]!=null && <span className="count">{counts[n.countKey]}</span>}
    </button>
  );
}

// ── Shell ──────────────────────────────────────────────────────────────

function PageHead({ crumbs, title, sub, actions }) {
  return (
    <div className="page-head">
      <div>
        {crumbs && <div className="crumbs">{crumbs.map((c,i)=><React.Fragment key={i}>{i>0&&' / '}<span>{c}</span></React.Fragment>)}</div>}
        <h1 className="page-title">{title}</h1>
        {sub && <p className="page-sub">{sub}</p>}
      </div>
      {actions && <div className="head-actions">{actions}</div>}
    </div>
  );
}

function Toolbar({ search, onSearch, placeholder, filters=[], activeFilter, onFilter, children }) {
  return (
    <div className="toolbar">
      <div className="search">
        <span className="icn"><Icon name="search" size={14}/></span>
        <input value={search} onChange={e=>onSearch(e.target.value)} placeholder={placeholder}/>
      </div>
      {filters.map(f=>
        <button key={f} className={`filter-chip ${activeFilter===f?'active':''}`} onClick={()=>onFilter(activeFilter===f?null:f)}>
          {activeFilter===f && <Icon name="check" size={11}/>}{f}
        </button>
      )}
      <div style={{flex:1}}/>{children}
    </div>
  );
}

// ── API helpers ────────────────────────────────────────────────────────

let _onUnauthorized = null;

async function api(token, method, path, body) {
  const opts = { method, headers: {} };
  if (token?.data?.access_token) opts.headers['Authorization'] = 'Bearer ' + token.data.access_token;
  if (body) { opts.headers['Content-Type']='application/json'; opts.body=JSON.stringify(body); }

  let r = await fetch(path, opts);

  // Stale token — try refresh once
  if (r.status === 401 && token?.refresh) {
    try {
      await token.refresh();
      opts.headers['Authorization'] = 'Bearer ' + token.data.access_token;
      r = await fetch(path, opts);
    } catch {
      _onUnauthorized?.();
      throw new Error('Session expired');
    }
  }

  if (r.status === 401) { _onUnauthorized?.(); throw new Error('Session expired'); }
  if (!r.ok) {
    const t = await r.text().catch(()=>'');
    throw new Error(`${r.status}: ${t||r.statusText}`);
  }
  return r.status===204 ? null : r.json();
}

const copyText = async (text, toast) => {
  try { await navigator.clipboard.writeText(text); toast('Copied to clipboard'); }
  catch { toast('Failed to copy', true); }
};

function serviceInstructions(service) {
  if (service.descriptor?.type === "ssh") {
    return `  - ${service.name} (see the SSH guide in the 'freshbreath' skill)`
  } else if (service.descriptor?.type === "tasks") {
    return `  - ${service.name} (see the tasks guide in the 'freshbreath' skill)`
  } else if (service.descriptor?.type === "virtual") {
    return `  - ${service.name} (MCP): "${service.url}"`
  }
  return `  - ${service.name} (${service.descriptor?.type?.toLocaleUpperCase()}): "${service.url}"`
}

function buildPrompt(app, appServices) {
  const fbURL = window.__HOMESLICE_CONFIG?.apiBase || window.location.origin;
  const serviceLines = appServices.length
    ? ("\nIntegrations: (be sure to use any URLs exactly)\n" + appServices.map(serviceInstructions).join('\n'))
    : '';
  return `Use the 'freshbreath' skill to add integrations to this app.\n\nSettings:\n  App nonce: ${app.nonce}\n  Fresh Breath URL: ${fbURL}\n${serviceLines}`;
}

// ── Sections ───────────────────────────────────────────────────────────

function Overview({ users, apps, services, audit }) {
  const activeUsers = users.filter(u=>u.status==='Active').length;
  const prodApps = apps.filter(a=>a.environment==='Production').length;
  return (
    <>
      <PageHead
        crumbs={['Fresh Breath']}
        title="Overview"
        sub="A snapshot of your workspace."
      />
      <div className="stats">
        <div className="stat">
          <span className="lbl">Applications</span>
          <span className="val">{apps.length}</span>
          <span className="sub">{prodApps} in production</span>
        </div>
        <div className="stat">
          <span className="lbl">Services</span>
          <span className="val">{services.length}</span>
          <span className="sub">registered providers</span>
        </div>
        <div className="stat">
          <span className="lbl">Recent events</span>
          <span className="val">{Math.min(audit.length,10)}</span>
          <span className="sub">last {Math.min(audit.length,100)} records</span>
        </div>
        <div className="stat">
          <span className="lbl">Users</span>
          <span className="val">{users.length}</span>
          <span className="sub">{activeUsers} active · {users.length-activeUsers} other</span>
        </div>
      </div>

      <div className="overview-grid">
        <div>
          <h3 style={{margin:'0 0 12px',fontSize:14,fontWeight:500}}>Recent activity</h3>
          <div className="table-wrap"><div style={{padding:'8px 16px'}}>
            {audit.length === 0 ? (
              <div className="empty" style={{padding:'24px 0'}}>
                <b>No recent activity.</b><br/>Events will appear here as users interact with services.
              </div>
            ) : (
              <div className="timeline">
                {audit.slice(0,6).map(a=>{
                  const ai = actionIcon(a.action);
                  return (
                    <div key={a.id} className="tl-row">
                      <span className="tl-when">{fmtAuditTime(a.when)}</span>
                      <span className={`tl-icn tone-${ai.tone}`}><Icon name={ai.icon} size={14}/></span>
                      <div className="tl-body">
                        <div><b>{a.actor}</b> <span className="muted">{a.action}</span></div>
                        <div className="target">{a.target}</div>
                      </div>
                    </div>
                  );
                })}
              </div>
            )}
          </div></div>
        </div>
        <div>
          <h3 style={{margin:'0 0 12px',fontSize:14,fontWeight:500}}>Apps</h3>
          <div className="table-wrap" style={{padding:4}}>
            {apps.length === 0 ? (
              <div className="empty" style={{padding:'24px 16px'}}>
                <b>No apps yet.</b><br/>Create your first app to start connecting services.
              </div>
            ) : (
              apps.slice(0,6).map((a,i)=>
                <div key={a.nonce} style={{padding:'12px 16px',display:'flex',alignItems:'center',gap:16,borderBottom:i<5?'1px solid var(--line-soft)':0}}>
                  <div style={{flex:1}}>
                    <div className="mono" style={{fontSize:13}}>{a.name}</div>
                    <div style={{fontSize:11.5,color:'var(--ink-3)'}}>{a.environment} · {a.owner_name||'No owner'}</div>
                  </div>
                  <Badge tone={envTone(a.environment)}>{a.environment||'—'}</Badge>
                </div>
              )
            )}
          </div>
        </div>
      </div>
    </>
  );
}

// ── Users ──────────────────────────────────────────────────────────────

function UsersView({ token, users, apps, onRefresh }) {
  const [q,setQ] = useState('');
  const [filter,setFilter] = useState(null);
  const [editing,setEditing] = useState(null);
  const toast = useToast();

  const filtered = users.filter(u=>{
    if(q && !(`${u.name} ${u.email}`.toLowerCase().includes(q.toLowerCase()))) return false;
    if(filter && u.status!==filter) return false;
    return true;
  });

  const remove = async (id) => {
    if (!confirm('Delete this user?')) return;
    try { await api(token, 'DELETE','/api/users/'+id); toast('User deleted'); onRefresh(); }
    catch(e) { toast(e.message,true); }
  };

  return (
    <>
      <PageHead
        crumbs={['Workspace','Users']}
        title="Users"
        sub="Say who can manage apps and services and their permissions."
        actions={<button className="btn btn-primary" onClick={()=>setEditing('new')}><Icon name="plus" size={14}/> New user</button>}
      />
      <Toolbar
        search={q} onSearch={setQ}
        placeholder="Search by name or email…"
        filters={['Active','Invited','Suspended']}
        activeFilter={filter} onFilter={setFilter}
      />
      <div className="table-wrap">
        <table className="tbl" data-mobile>
          <thead><tr><th style={{width:'28%'}}>Name</th><th>Role</th><th>Status</th><th>Apps</th><th>Last seen</th><th style={{width:80}}></th></tr></thead>
          <tbody>
            {filtered.map(u=>
              <tr key={u.id}>
                <td data-col="identity">
                  <div className="user-cell">
                    <Avatar name={u.name}/>
                    <div className="meta"><b>{u.name}</b><span>{u.email}</span></div>
                  </div>
                </td>
                <td data-col="detail"><Badge tone={roleTone(u.role)}>{u.role}</Badge></td>
                <td data-col="badge"><Badge tone={statusTone(u.status)}>{u.status}</Badge></td>
                <td data-col="detail"><UserAppTags apps={u.apps} appList={apps}/></td>
                <td data-col="detail" className="muted">{fmtAuditTime(u.last_seen)}</td>
                <td data-col="actions">
                  <div className="row-actions">
                    <button className="btn btn-icon btn-ghost" onClick={()=>setEditing(u)} title="Edit"><Icon name="edit" size={14}/></button>
                    <button className="btn btn-icon btn-ghost" onClick={()=>remove(u.id)} title="Delete"><Icon name="trash" size={14}/></button>
                  </div>
                </td>
              </tr>
            )}
          </tbody>
        </table>
        {filtered.length===0 && <div className="empty"><b>No users match.</b>Try a different search.</div>}
      </div>
      <UserDrawer user={editing} token={token} apps={apps} onClose={()=>setEditing(null)} onSaved={onRefresh}/>
    </>
  );
}

function UserDrawer({ user, token, apps, onClose, onSaved }) {
  const { user: actor, authRequired } = useAuth();
  const canManageSSH = (!authRequired) || (actor && (actor.role === 'Superuser' || actor.role === 'Admin'));
  const [form,setForm] = useState({name:'',email:'',role:'Member',status:'Active',apps:[]});
  const [loading,setLoading] = useState(false);
  const [sshKey, setSSHKey] = useState(null);
  const [sshLoading, setSSHLoading] = useState(false);
  const [showSSHGen, setShowSSHGen] = useState(false);
  const [passphrase, setPassphrase] = useState('');
  const [passConfirm, setPassConfirm] = useState('');
  const toast = useToast();
  const isNew = user==='new';
  const isEdit = user && user.id;

  useEffect(()=>{
    if(isEdit) {
      setForm({name:user.name,email:user.email,role:user.role||'Member',status:user.status||'Active',apps:[]});
      setLoading(true);
      api(token, 'GET','/api/users/'+user.id+'/apps')
        .then(d=>{
          setForm(f=>({...f,apps:d.apps||[]}));
        })
        .catch(e=>toast(e.message,true))
        .finally(()=>setLoading(false));
      // Load SSH key status for admins
      if (canManageSSH) {
        setSSHLoading(true);
        api(token, 'GET','/api/users/'+user.id+'/ssh-key')
          .then(d => setSSHKey(d.ssh_key))
          .catch(() => setSSHKey(null))
          .finally(() => setSSHLoading(false));
      }
    } else {
      setForm({name:'',email:'',role:'Member',status:'Active',apps:[]});
      setSSHKey(null);
    }
    setShowSSHGen(false);
    setSSHLoading(false);
    setPassphrase('');
    setPassConfirm('');
  },[user]); // eslint-disable-line react-hooks/exhaustive-deps

  const save = async () => {
    try {
      let uid;
      if(isEdit) {
        await api(token, 'PUT','/api/users/'+user.id,form);
        await api(token, 'PUT','/api/users/'+user.id+'/apps',{apps:form.apps||[]});
        toast('User updated');
      } else {
        const resp = await api(token, 'POST','/api/users',form);
        uid = resp.id;
        await api(token, 'PUT','/api/users/'+uid+'/apps',{apps:form.apps||[]});
        toast('User created');
      }
      onClose(); onSaved();
    } catch(e) { toast(e.message,true); }
  };

  return (
    <Drawer
      open={isNew || isEdit} title={isNew?'New user':'Edit user'}
      onClose={onClose}
      footer={<>
        <button className="btn btn-ghost" onClick={onClose}>Cancel</button>
        <button className="btn btn-primary" onClick={save}>{isNew?'Create':'Save'}</button>
      </>}
    >
      <p><strong>NOTE:</strong> You don't need to create accounts for people who are just using the apps and logging in with their own creds! This is only for users who need to log in to this admin panel and manage apps and services.</p>
      <div className="field"><label>Name</label><input className="input" value={form.name} onChange={e=>setForm(f=>({...f,name:e.target.value}))} placeholder="Ada Lovelace"/></div>
      <div className="field"><label>Email</label><input className="input" value={form.email} onChange={e=>setForm(f=>({...f,email:e.target.value}))} placeholder="ada@company.com"/></div>
      <div className="field-row">
        <div className="field"><label>Role</label>
          <select className="input" value={form.role} onChange={e=>setForm(f=>({...f,role:e.target.value}))}>
            <option>Superuser</option><option>Admin</option><option>Member</option><option>Read-only</option>
          </select>
        </div>
        <div className="field"><label>Status</label>
          <select className="input" value={form.status} onChange={e=>setForm(f=>({...f,status:e.target.value}))}>
            <option>Active</option><option>Invited</option><option>Suspended</option>
          </select>
        </div>
      </div>
      {(isNew || isEdit) && (
        <div className="field">
          <label>Assigned apps</label>
          <span className="help">Select apps this user can access.</span>
          {isEdit && loading ? <span className="muted">Loading…</span> : (
            <MultiSelect
              options={apps.map(a=>({value:a.nonce,label:a.name}))}
              value={form.apps}
              onChange={(v)=>setForm(f=>({...f,apps:v}))}
              placeholder="No apps"
            />
          )}
        </div>
      )}
      {isEdit && canManageSSH && (
        <>
          <div style={{marginTop:20,borderTop:'1px solid var(--border)',paddingTop:16}}>
            <label style={{fontSize:13,fontWeight:600,color:'var(--ink-2)',marginBottom:12,display:'block'}}>SSH Key</label>
            {sshLoading ? <span className="muted">Loading…</span> : sshKey ? (
              <>
                <div style={{marginBottom:8}}>
                  <Badge tone="green">Active</Badge>
                  <span className="muted" style={{marginLeft:8,fontSize:13}}>{sshKey.key_type?.toUpperCase()} · {sshKey.fingerprint}</span>
                </div>
                <div style={{display:'flex',gap:8,alignItems:'center'}}>
                  <input className="input mono" value={sshKey.public_key?.trim()} readOnly style={{fontSize:11}} />
                  <button className="btn btn-ghost" onClick={() => copyText(sshKey.public_key?.trim(), toast)}><Icon name="copy" size={14}/></button>
                </div>
                <button className="btn btn-ghost" style={{color:'var(--red)',marginTop:8}} onClick={async () => {
                  if (!confirm('Delete this user\'s SSH key? They\'ll need a new one to use SSH auth.')) return;
                  try { await api(token, 'DELETE','/api/users/'+user.id+'/ssh-key'); setSSHKey(null); toast('SSH key deleted'); }
                  catch(e) { toast(e.message, true); }
                }}>Delete key</button>
              </>
            ) : (
              <button className="btn btn-ghost" onClick={() => setShowSSHGen(true)}><Icon name="lock" size={14}/> Generate SSH Key</button>
            )}
          </div>
          {showSSHGen && (
            <div className="modal-overlay" onClick={() => { setShowSSHGen(false); setPassphrase(''); setPassConfirm(''); }}>
              <div className="modal" onClick={e => e.stopPropagation()} style={{maxWidth:420}}>
                <h3 style={{marginBottom:16}}>Generate SSH Key</h3>
                <p className="muted" style={{fontSize:13,marginBottom:16}}>
                  Choose a passphrase for {user.name}'s SSH key. They'll need it each time they log in via SSH.
                </p>
                <div className="field">
                  <label>Passphrase</label>
                  <input className="input" type="password" value={passphrase} onChange={e => setPassphrase(e.target.value)} placeholder="Min 8 characters" autoFocus />
                </div>
                <div className="field">
                  <label>Confirm passphrase</label>
                  <input className="input" type="password" value={passConfirm} onChange={e => setPassConfirm(e.target.value)} placeholder="Re-enter passphrase" />
                </div>
                <div style={{display:'flex',gap:8,justifyContent:'flex-end',marginTop:20}}>
                  <button className="btn btn-ghost" onClick={() => { setShowSSHGen(false); setPassphrase(''); setPassConfirm(''); }}>Cancel</button>
                  <button className="btn btn-primary" disabled={passphrase.length < 8 || passphrase !== passConfirm} onClick={async () => {
                    try {
                      const d = await api(token, 'POST','/api/users/'+user.id+'/ssh-key', { passphrase });
                      setSSHKey(d.ssh_key);
                      setShowSSHGen(false);
                      setPassphrase('');
                      setPassConfirm('');
                      toast('SSH key generated');
                    } catch(e) { toast(e.message, true); }
                  }}>Generate</button>
                </div>
              </div>
            </div>
          )}
        </>
      )}
    </Drawer>
  );
}

// ── Apps ───────────────────────────────────────────────────────────────

function AppMemberTags({ members, users }) {
  if (!members || members.length === 0) return <span className="muted">—</span>;
  const names = members.map(id => {
    const u = users.find(x => x.id === id);
    return u ? u.name : '?';
  }).slice(0, 3);
  const extra = members.length - names.length;
  return (
    <span className="tags">
      {names.map((n, i) => <span key={i} className="tag">{n}</span>)}
      {extra > 0 && <span className="tag muted">+{extra}</span>}
    </span>
  );
}

function UserAppTags({ apps, appList }) {
  if (!apps || apps.length === 0) return <span className="muted">—</span>;
  const names = apps.map(nonce => {
    const a = appList.find(x => x.nonce === nonce);
    return a ? a.name : '?';
  }).slice(0, 3);
  const extra = apps.length - names.length;
  return (
    <span className="tags">
      {names.map((n, i) => <span key={i} className="tag">{n}</span>)}
      {extra > 0 && <span className="tag muted">+{extra}</span>}
    </span>
  );
}

function AppsView({ token, apps, services, users, onRefresh }) {
  const [q,setQ] = useState('');
  const [filter,setFilter] = useState(null);
  const [editing,setEditing] = useState(null);
  const toast = useToast();

  const filtered = apps.filter(a=>{
    if(q && !(`${a.name} ${a.owner_name||''}`.toLowerCase().includes(q.toLowerCase()))) return false;
    if(filter && a.environment!==filter) return false;
    return true;
  });

  const remove = async (nonce) => {
    if(!confirm('Delete this app?')) return;
    try { await api(token, 'DELETE','/api/apps/'+nonce); toast('App deleted'); onRefresh(); }
    catch(e) { toast(e.message,true); }
  };

  const copyNonce = (nonce) => copyText(nonce, toast);

  const copyPrompt = async (a) => {
    try {
      const r = await api(token, 'GET', '/api/apps/' + a.nonce + '/services');
      const allowedIds = (r.services || []).filter(l => l.allowed).map(l => l.service_id);
      const appSvcs = services.filter(s => allowedIds.includes(s.id));
      copyText(buildPrompt(a, appSvcs), toast);
    } catch(e) { toast(e.message, true); }
  };

  return (
    <>
      <PageHead
        crumbs={['Workspace','Apps']}
        title="Applications"
        sub="Service consumers with managed access."
        actions={<button className="btn btn-primary" onClick={()=>setEditing('new')}><Icon name="plus" size={14}/> New app</button>}
      />
      <Toolbar
        search={q} onSearch={setQ}
        placeholder="Search apps…"
        filters={['Production','Staging','Development']}
        activeFilter={filter} onFilter={setFilter}
      />
      <div className="table-wrap">
        <table className="tbl" data-mobile>
          <thead><tr><th style={{width:'26%'}}>Name</th><th>Environment</th><th>Owner</th><th>Members</th><th>Services</th><th style={{width:80}}></th></tr></thead>
          <tbody>
            {filtered.map(a=>
              <tr key={a.nonce}>
                <td data-col="identity">
                  <div className="user-cell">
                    <div className="avatar" style={{
                      width:32,height:32,borderRadius:8,
                      background:`linear-gradient(135deg,oklch(0.85 0.06 ${hashHue(a.name)}),oklch(0.55 0.1 ${(hashHue(a.name)+30)%360}))`,
                      fontSize:13,color:'white',display:'grid',placeItems:'center'
                    }}>{a.name?.[0]}</div>
                    <div className="meta">
                      <div style={{display:'flex',alignItems:'center',gap:6}}>
                        <b>{a.name}</b>
                        {a.details?.last_uploaded && <span style={{fontSize:10,padding:'1px 5px',borderRadius:4,background:'oklch(from var(--tone-green) var(--tone-bg-l) calc(c*.25) h)',color:'oklch(from var(--tone-green) var(--tone-fg-l) calc(c*.67) h)',border:'1px solid oklch(from var(--tone-green) var(--tone-border-l) calc(c*.33) h)',lineHeight:1.4}}>hosted</span>}
                      </div>
                      <span className="mono" style={{cursor:'pointer'}} onClick={()=>copyNonce(a.nonce)} title={`${a.nonce} — click to copy`}>
                        {a.nonce.slice(0,8)}… <span style={{opacity:0.6,verticalAlign:'middle',marginLeft:2}}><Icon name="copy" size={12}/></span>
                      </span>
                    </div>
                  </div>
                </td>
                <td data-col="badge"><Badge tone={envTone(a.environment)}>{a.environment||'—'}</Badge></td>
                <td data-col="detail">{a.owner_name||<span className="muted">—</span>}</td>
                <td data-col="detail" className="mono">{a.member_count??0}</td>
                <td data-col="detail" className="mono">{a.service_count??0}</td>
                <td data-col="actions">
                  <div className="row-actions">
                    <button className="btn btn-icon btn-ghost" onClick={()=>copyPrompt(a)} title="Copy setup prompt"><Icon name="sparkle" size={14}/></button>
                    <button className="btn btn-icon btn-ghost" onClick={()=>setEditing(a)} title="Edit"><Icon name="edit" size={14}/></button>
                    <button className="btn btn-icon btn-ghost" onClick={()=>remove(a.nonce)} title="Delete"><Icon name="trash" size={14}/></button>
                  </div>
                </td>
              </tr>
            )}
          </tbody>
        </table>
        {filtered.length===0 && <div className="empty"><b>No apps found.</b></div>}
      </div>
      <AppDrawer token={token} app={editing} services={services} users={users} apps={apps} onClose={()=>setEditing(null)} onSaved={onRefresh}/>
    </>
  );
}

function HostUpload({ token, app, onRefresh }) {
  const isHosted = !!(app.details?.last_uploaded);
  const [hosted, setHosted] = useState(isHosted);
  const [uploadedAt, setUploadedAt] = useState(app.details?.last_uploaded || null);
  const [dragging, setDragging] = useState(false);
  const [uploading, setUploading] = useState(false);
  const inputRef = useRef(null);
  const toast = useToast();

  const route = (() => {
    if (app.url && !app.url.includes('://')) return '/' + app.url.replace(/^\//, '');
    const slug = (app.name || '').toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '');
    return '/' + slug;
  })();

  const upload = async (file) => {
    if (!file) return;
    const ext = file.name.split('.').pop().toLowerCase();
    if (ext !== 'html' && ext !== 'zip') {
      toast('Please upload an .html or .zip file', true);
      return;
    }
    setUploading(true);
    const fd = new FormData();
    fd.append('file', file);
    try {
      const res = await fetch('/api/apps/' + app.nonce + '/web', {
        method: 'POST',
        headers: { 'Authorization': 'Bearer ' + token?.data?.access_token },
        body: fd,
      });
      if (!res.ok) throw new Error(await res.text());
      const now = new Date().toISOString();
      setHosted(true);
      setUploadedAt(now);
      toast('Hosted at ' + route);
      onRefresh();
    } catch(e) {
      toast(e.message, true);
    } finally {
      setUploading(false);
    }
  };

  const remove = async () => {
    try {
      const res = await fetch('/api/apps/' + app.nonce + '/web', {
        method: 'DELETE',
        headers: { 'Authorization': 'Bearer ' + token?.data?.access_token },
      });
      if (!res.ok) throw new Error(await res.text());
      setHosted(false);
      setUploadedAt(null);
      toast('Hosting removed');
      onRefresh();
    } catch(e) {
      toast(e.message, true);
    }
  };

  const onDrop = (e) => {
    e.preventDefault();
    setDragging(false);
    upload(e.dataTransfer.files[0]);
  };

  return (
    <div className="field">
      <label>Web hosting</label>
      {hosted ? (
        <div style={{display:'flex',alignItems:'center',gap:8,flexWrap:'wrap',marginBottom:4}}>
          <Badge tone="green">Hosted</Badge>
          <span className="mono" style={{fontSize:13}}>{route}</span>
          <span className="muted" style={{fontSize:12,flex:1}}>uploaded {fmtAuditTime(uploadedAt)}</span>
          <button className="btn btn-ghost" style={{padding:'2px 8px',fontSize:12,color:'var(--tone-red)'}} onClick={remove}>Remove</button>
        </div>
      ) : (
        <span className="help">Upload an HTML file or a ZIP containing your app.</span>
      )}
      <div
        className={'drop-zone' + (dragging ? ' drop-zone-active' : '')}
        onDragOver={e=>{e.preventDefault();setDragging(true);}}
        onDragLeave={()=>setDragging(false)}
        onDrop={onDrop}
        onClick={()=>inputRef.current?.click()}
      >
        {uploading ? 'Uploading…' : (hosted ? 'Drop to replace' : 'Drop .html or .zip here, or click to browse')}
        <input ref={inputRef} type="file" accept=".html,.zip" style={{display:'none'}}
          onChange={e=>upload(e.target.files[0])}/>
      </div>
    </div>
  );
}

function AppDrawer({ token, app, services, users, apps, onClose, onSaved }) {
  const [form,setForm] = useState({name:'',environment:'Development',url:'',owner_id:'',services:[],members:[]});
  const [loading,setLoading] = useState(false);
  const toast = useToast();
  const isNew = app==='new';
  const isEdit = app && app.nonce;

  useEffect(()=>{
    if(isEdit) {
      setForm({name:app.name||'',environment:app.environment||'Development',url:app.url,owner_id:app.owner_id?String(app.owner_id):'',services:[],members:[]});
      setLoading(true);
      Promise.all([
        api(token, 'GET','/api/apps/'+app.nonce+'/services'),
        api(token, 'GET','/api/apps/'+app.nonce+'/members'),
      ])
        .then(([svcs,mems])=>{
          const allowed = (svcs.services||[]).filter(l=>l.allowed).map(l=>l.service_id);
          setForm(f=>({...f,services:allowed,members:mems.members||[]}));
        })
        .catch(e=>toast(e.message,true))
        .finally(()=>setLoading(false));
    } else {
      setForm({name:'',environment:'Development',url:'',owner_id:'',services:[],members:[]});
    }
  },[app]); // eslint-disable-line react-hooks/exhaustive-deps

  const handleServicesChange = (newServices) => {
    setForm(f=>({...f,services:newServices}));
  };

  const save = async () => {
    const payload = {name:form.name,environment:form.environment,url:form.url,owner_id:form.owner_id?Number(form.owner_id):null};
    try {
      let nonce;
      if(isEdit) {
        await api(token, 'PUT','/api/apps/'+app.nonce,payload);
        await api(token, 'PUT','/api/apps/'+app.nonce+'/members',{members:form.members||[]});
        await api(token, 'PUT','/api/apps/'+app.nonce+'/services',{services:form.services||[]});
        toast('App updated');
      } else {
        const resp = await api(token, 'POST','/api/apps',payload);
        nonce = resp.nonce;
        await api(token, 'PUT','/api/apps/'+nonce+'/members',{members:form.members||[]});
        await api(token, 'PUT','/api/apps/'+nonce+'/services',{services:form.services||[]});
        toast('App created');
      }
      onClose(); onSaved();
    } catch(e) { toast(e.message,true); }
  };

  const copyNonce = (nonce) => copyText(nonce, toast);
  const setupPrompt = isEdit && !loading
    ? buildPrompt(app, services.filter(s => form.services.includes(s.id)))
    : null;

  return (
    <Drawer
      open={isNew || isEdit} title={isNew?'New application':'Edit application'}
      onClose={onClose}
      footer={<>
        <button className="btn btn-ghost" onClick={onClose}>Cancel</button>
        <button className="btn btn-primary" onClick={save}>{isNew?'Create':'Save'}</button>
      </>}
    >
      <div className="field"><label>Name</label><input className="input" value={form.name} onChange={e=>setForm(f=>({...f,name:e.target.value}))} placeholder="My App"/></div>
      {isEdit && app.nonce && (
        <div className="field">
          <label>Nonce</label>
          <div className="input" style={{display:'flex',alignItems:'center',justifyContent:'space-between',gap:12}}>
            <span className="mono">{app.nonce}</span>
            <button className="btn btn-ghost" style={{padding:'2px 8px',fontSize:12}} onClick={()=>copyNonce(app.nonce)}>
              <Icon name="copy" size={14}/> Copy
            </button>
          </div>
        </div>
      )}
      <div className="field"><label>URL</label>
        <input className="input" value={form.url} onChange={e=>setForm(f=>({...f,url:e.target.value}))} placeholder="https://hostname.com:port"/>
      </div>
      <div className="field-row">
        <div className="field"><label>Environment</label>
          <select className="input" value={form.environment} onChange={e=>setForm(f=>({...f,environment:e.target.value}))}>
            <option>Production</option><option>Staging</option><option>Development</option>
          </select>
        </div>
        <div className="field"><label>Owner</label>
          <select className="input" value={form.owner_id} onChange={e=>setForm(f=>({...f,owner_id:e.target.value}))}>
            <option value="">No owner</option>
            {users.map(u=><option key={u.id} value={u.id}>{u.name}</option>)}
          </select>
        </div>
      </div>
      {(isNew || isEdit) && (
        <div className="field">
          <label>Members</label>
          <span className="help">Select which users are assigned to this app.</span>
          {isEdit && loading ? <span className="muted">Loading…</span> : (
            <MultiSelect
              options={users.map(u=>({value:u.id,label:u.name}))}
              value={form.members}
              onChange={(v)=>setForm(f=>({...f,members:v}))}
              placeholder="No members"
            />
          )}
        </div>
      )}
      {(isNew || isEdit) && (
        <div className="field">
          <label>Service access</label>
          <span className="help">Select which services this app can access.</span>
          {isEdit && loading ? <span className="muted">Loading…</span> : (
            <MultiSelect
              options={services.map(s=>({value:s.id,label:s.name}))}
              value={form.services}
              onChange={handleServicesChange}
              placeholder="No service access"
            />
          )}
        </div>
      )}
      {setupPrompt && (
        <div className="field">
          <label>Setup prompt</label>
          <span className="help">Paste into Claude Code to wire up this app with the freshbreath skill.</span>
          <div style={{position:'relative'}}>
            <textarea
              className="input"
              readOnly
              style={{fontFamily:'var(--font-mono)',fontSize:11,lineHeight:1.6,resize:'vertical',paddingRight:38,width:'100%',fieldSizing:'content'}}
              value={setupPrompt}
              onClick={e=>e.target.select()}
            />
            <button
              className="btn btn-ghost"
              style={{position:'absolute',top:8,right:8,padding:'4px 6px'}}
              title="Copy prompt"
              onClick={()=>copyText(setupPrompt, toast)}
            >
              <Icon name="copy" size={13}/>
            </button>
          </div>
        </div>
      )}
      {isEdit && !loading && (
        <HostUpload token={token} app={app} onRefresh={onSaved}/>
      )}
    </Drawer>
  );
}

// ── Services ───────────────────────────────────────────────────────────

function ServicesView({ token, services, onRefresh }) {
  const [q,setQ] = useState('');
  const [editing,setEditing] = useState(null);
  const toast = useToast();

  const filtered = services.filter(s=>{
    if(q && !(`${s.name} ${s.url}`.toLowerCase().includes(q.toLowerCase()))) return false;
    return true;
  });

  const remove = async (id) => {
    let apps = [];
    try { const r = await api(token, 'GET','/api/services/'+id+'/apps'); apps = r.apps||[]; }
    catch(e) { /* ignore */ }
    let msg = 'Delete this service?';
    if(apps.length>0) {
      msg += `\n\nIt's used by ${apps.length} app${apps.length>1?'s':''}:\n${apps.map(a=>a.name).join(', ')}`;
    }
    if(!confirm(msg)) return;
    try { await api(token, 'DELETE','/api/services/'+id); toast('Service deleted'); onRefresh(); }
    catch(e) { toast(e.message,true); }
  };

  return (
    <>
      <PageHead
        crumbs={['Workspace','Services']}
        title="Services"
        sub="Registered MCP, OAuth, API, and task providers."
        actions={<button className="btn btn-primary" onClick={()=>setEditing('new')}><Icon name="plus" size={14}/> New service</button>}
      />
      <Toolbar search={q} onSearch={setQ} placeholder="Search services…"/>
      <div className="table-wrap">
        <table className="tbl" data-mobile>
          <thead><tr><th style={{width:'25%'}}>Name</th><th>URL</th><th>Type</th><th>Proxied</th><th style={{width:80}}></th></tr></thead>
          <tbody>
            {filtered.map(s=>
              <tr key={s.id}>
                <td data-col="identity"><b>{s.name}</b>{s.descriptor?.type==='ssh' && <Badge tone="purple" style={{marginLeft:6}}>Built-in</Badge>}</td>
                <td data-col="url">
                  <span
                    className="mono"
                    style={{fontSize:12.5, color:'var(--ink-3)', cursor:'pointer'}}
                    onClick={()=>copyText(s.url, toast)}
                    title={`${s.url} — click to copy`}
                  >
                    {s.url.length>48?s.url.slice(0,48)+'…':s.url} <span style={{opacity:0.6,verticalAlign:'middle',marginLeft:2}}><Icon name="copy" size={12}/></span>
                  </span>
                </td>
                <td data-col="badge"><Badge dot={false} tone="gray">{s.descriptor?.type?.toLocaleUpperCase()||'—'}</Badge></td>
                <td data-col="detail">{s.descriptor?.proxied ? <Badge tone="blue">Proxied</Badge> : <span className="muted">—</span>}</td>
                <td data-col="actions">
                  <div className="row-actions">
                    <button className="btn btn-icon btn-ghost" onClick={()=>setEditing(s)} title="Edit"><Icon name="edit" size={14}/></button>
                    {s.descriptor?.type!=='ssh' && <button className="btn btn-icon btn-ghost" onClick={()=>remove(s.id)} title="Delete"><Icon name="trash" size={14}/></button>}
                  </div>
                </td>
              </tr>
            )}
          </tbody>
        </table>
        {filtered.length===0 && <div className="empty"><b>No services found.</b></div>}
      </div>
      <ServiceDrawer token={token} services={services} service={editing} onClose={()=>setEditing(null)} onSaved={onRefresh}/>
    </>
  );
}

function ServiceDrawer({ token, services, service, onClose, onSaved }) {
  const [form,setForm] = useState({name:'',url:'',descriptor:{type:'mcp',proxied:false}});
  const toast = useToast();
  const isNew = service==='new';
  const isEdit = service && service.id;

  useEffect(()=>{
    if(isEdit) setForm({name:service.name,url:service.url,descriptor:{...service.descriptor}});
    else setForm({name:'',url:'',descriptor:{type:'mcp',proxied:false}});
  },[service]);

  const updDesc = (k,v) => setForm(f=>({...f,descriptor:{...f.descriptor,[k]:v}}));

  // When switching type, clear fields that don't apply
  const setType = (t) => {
    const d = {...form.descriptor, type: t};
    if (t === 'tasks') {
      delete d.auth; delete d.api_key; delete d.header; delete d.proxied;
      delete d.client_id; delete d.client_secret; delete d.oauth_url;
      delete d.scopes; delete d.userinfo_url; delete d.userinfo_emails_url;
    } else if (t === 'virtual') {
      delete d.proxied;
      delete d.auth_service_id; delete d.userinfo_url; delete d.userinfo_emails_url;
    } else {
      delete d.auth_service_id;
    }
    setForm(f=>({...f, descriptor: d}));
  };

  const save = async () => {
    try {
      // Strip auth_service_id if not tasks type
      const payload = {...form};
      if (payload.descriptor.type !== 'tasks' && payload.descriptor.auth_service_id) {
        const d = {...payload.descriptor}; delete d.auth_service_id; payload.descriptor = d;
      }
      // Virtual services don't need a URL — server generates /mcp/{slug}
      if (payload.descriptor.type === 'virtual') {
        payload.url = '';
      }
      if(isEdit) { await api(token, 'PUT','/api/services/'+service.id,payload); toast('Service updated'); }
      else { await api(token, 'POST','/api/services',payload); toast('Service created'); }
      onClose(); onSaved();
    } catch(e) { toast(e.message,true); }
  };

  const isSSH = isEdit && service.descriptor?.type === 'ssh';
  const isTasks = form.descriptor.type === 'tasks';
  const isVirtual = form.descriptor.type === 'virtual';

  // Auth service options for tasks: OIDC services + built-in SSH
  const oidcSvc = services.filter(s => s.descriptor?.type === 'oidc');
  const sshSvc = services.find(s => s.descriptor?.type === 'ssh');
  const authSvcOptions = [
    ...oidcSvc.map(s => ({ id: String(s.id), label: s.name, type: 'OIDC' })),
    ...(sshSvc ? [{ id: String(sshSvc.id), label: 'SSH Key', type: 'SSH' }] : []),
  ];

  return (
    <Drawer
      open={isNew || isEdit} title={isNew?'New service':'Edit service'}
      onClose={onClose}
      footer={<>
        <button className="btn btn-ghost" onClick={onClose}>Cancel</button>
        <button className="btn btn-primary" onClick={save}>{isNew?'Create':'Save'}</button>
      </>}
    >
      <div className="field"><label>Name</label><input className="input" value={form.name} onChange={e=>setForm(f=>({...f,name:e.target.value}))} disabled={isSSH}/></div>
      {!isTasks && !isVirtual && <div className="field"><label>URL</label><input className="input mono" value={form.url} onChange={e=>setForm(f=>({...f,url:e.target.value}))} disabled={isSSH}/></div>}
      {isSSH ? (
        <div className="field"><label>Type</label><Badge tone="purple">SSH</Badge></div>
      ) : (
      <div className="field-row">
        <div className="field"><label>Type</label>
          <select className="input" value={form.descriptor.type} onChange={e=>setType(e.target.value)}>
            <option value="mcp">MCP</option><option value="api">API</option><option value="oidc">OIDC</option><option value="tasks">Tasks</option><option value="virtual">Virtual</option>
          </select>
        </div>
        {!isTasks && !isVirtual && <div className="field"><label>Proxied</label>
          <select className="input" value={form.descriptor.proxied?'true':'false'} onChange={e=>updDesc('proxied',e.target.value==='true')}>
            <option value="false">No</option><option value="true">Yes</option>
          </select>
        </div>}
      </div>
      )}
      {isTasks && (
        <div className="field" style={{maxWidth:380}}>
          <label>Auth service</label>
          <select className="input" value={form.descriptor.auth_service_id||''} onChange={e=>updDesc('auth_service_id',e.target.value)}>
            <option value="">— None (app nonce only) —</option>
            {authSvcOptions.map(s => <option key={s.id} value={s.id}>{s.label} ({s.type})</option>)}
          </select>
          {authSvcOptions.length === 0 && (
            <span className="help">No auth services available. Add an OIDC service or use the built-in SSH service.</span>
          )}
        </div>
      )}
      {(form.descriptor.type==='api' || form.descriptor.type==='virtual') && (
        <>
          <div className="field-row">
            <div className="field"><label>Auth</label>
              <select className="input" value={form.descriptor.auth||''} onChange={e=>updDesc('auth',e.target.value)}>
                <option value="">OAuth (default)</option><option value="key">API key</option>
              </select>
            </div>
            {form.descriptor.auth==='key' &&
              <div className="field"><label>API Key</label><input className="input mono" type="password" value={form.descriptor.api_key||''} onChange={e=>updDesc('api_key',e.target.value)}/></div>
            }
          </div>
          {form.descriptor.auth==='key' &&
            <div className="field"><label>API Key Header</label><input className="input mono" value={form.descriptor.header||''} onChange={e=>updDesc('header',e.target.value)} placeholder="X-API-Key (or empty for Bearer)"/></div>
          }
        </>
      )}
      {(form.descriptor.type==='oidc' || ((form.descriptor.type==='api' || form.descriptor.type==='virtual') && !form.descriptor.auth)) && (
        <>
          <div className="field"><label>OAuth URL</label><input className="input mono" value={form.descriptor.oauth_url||''} onChange={e=>updDesc('oauth_url',e.target.value)} placeholder="https://provider.com/oauth"/></div>
          <div className="field-row">
            <div className="field"><label>Client ID</label><input className="input mono" value={form.descriptor.client_id||''} onChange={e=>updDesc('client_id',e.target.value)}/></div>
            <div className="field"><label>Scopes</label><input className="input" value={form.descriptor.scopes||''} onChange={e=>updDesc('scopes',e.target.value)} placeholder="openid profile email"/></div>
          </div>
          <div className="field"><label>Client Secret</label><input className="input mono" type="password" value={form.descriptor.client_secret||''} onChange={e=>updDesc('client_secret',e.target.value)}/></div>
        </>
      )}
      {form.descriptor.type==='oidc' && (
        <>
          <div className="field"><label>Userinfo URL</label><input className="input mono" value={form.descriptor.userinfo_url||''} onChange={e=>updDesc('userinfo_url',e.target.value)}/></div>
          <div className="field"><label>User Email URL</label><input className="input mono" value={form.descriptor.userinfo_emails_url||''} onChange={e=>updDesc('userinfo_emails_url',e.target.value)}/></div>
        </>
      )}
    </Drawer>
  );
}

// ── Roles ──────────────────────────────────────────────────────────────

function RolesView({ roles }) {
  const PERMS = [
    { group:'Apps',    items:['Read','Create','Edit','Delete'] },
    { group:'Services',items:['Read','Create','Edit','Delete'] },
    { group:'Users',   items:['Read','Invite','Suspend','Delete'] },
  ];
  const checkedFor = (role,group,item) => {
    if(role.name==='Superuser') return true;
    if(role.name==='Admin')     return true;
    if(role.name==='Member')    return item==='Read' || (group==='Apps'&&item==='Edit');
    if(role.name==='Read-only') return item==='Read';
    return false;
  };
  return (
    <>
      <PageHead
        crumbs={['Security','Roles']}
        title="Roles & permissions"
        sub="Built-in roles. Custom roles coming later."
      />
      <div className="table-wrap" style={{marginBottom:28}}>
        <table className="tbl">
          <thead><tr><th style={{width:'20%'}}>Role</th><th>Description</th><th>Members</th></tr></thead>
          <tbody>
            {roles.map(r=>
              <tr key={r.id}>
                <td><Badge tone={roleTone(r.name)}>{r.name}</Badge></td>
                <td>{r.description}</td>
                <td className="mono">{r.members}</td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
      <h3 style={{margin:'0 0 12px',fontSize:14,fontWeight:500}}>Permission matrix</h3>
      <div className="table-wrap">
        <table className="tbl">
          <thead><tr><th>Capability</th>{roles.map(r=><th key={r.id} style={{textAlign:'center'}}>{r.name}</th>)}</tr></thead>
          <tbody>
            {PERMS.flatMap(p=>p.items.map(item=>
              <tr key={p.group+item}>
                <td><span className="muted mono" style={{fontSize:11}}>{p.group}</span> &nbsp;{item}</td>
                {roles.map(r=><td key={r.id} style={{textAlign:'center'}}>{checkedFor(r,p.group,item)?<span className="mono"><Icon name="check" size={14}/></span>:<span className="muted">—</span>}</td>)}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </>
  );
}

// ── Audit ──────────────────────────────────────────────────────────────

function AuditView({ audit }) {
  const [q,setQ] = useState('');
  const filtered = audit.filter(a=>!q || `${a.actor} ${a.action} ${a.target}`.toLowerCase().includes(q.toLowerCase()));
  return (
    <>
      <PageHead
        crumbs={['Security','Audit log']}
        title="Audit log"
        sub="Recent changes across the system."
      />
      <Toolbar search={q} onSearch={setQ} placeholder="Search events…"/>
      <div className="table-wrap" style={{padding:'8px 24px'}}>
        <div className="timeline">
          {filtered.map(a=>{
            const ai = actionIcon(a.action);
            return (
              <div key={a.id} className="tl-row">
                <span className="tl-when">{fmtAuditTime(a.when)}</span>
                <span className={`tl-icn tone-${ai.tone}`}><Icon name={ai.icon} size={14}/></span>
                <div className="tl-body">
                  <div><b>{a.actor}</b> <span className="muted">{a.action}</span></div>
                  <div className="target">{a.target}</div>
                </div>
              </div>
            );
          })}
        </div>
      </div>
    </>
  );
}

// ── Settings ───────────────────────────────────────────────────────────

function SettingsView({ token, services, apps, user }) {
  const [selectedSvc, setSelectedSvc] = useState('');
  const [currentSvc, setCurrentSvc] = useState(null);
  const [defaultApp, setDefaultApp] = useState('');
  const [loading, setLoading] = useState(true);
  const [sshKey, setSSHKey] = useState(null);
  const [sshLoading, setSSHLoading] = useState(false);
  const [showGenModal, setShowGenModal] = useState(false);
  const [passphrase, setPassphrase] = useState('');
  const [passConfirm, setPassConfirm] = useState('');
  const toast = useToast();

  useEffect(() => {
    api(token, 'GET', '/api/settings')
      .then(d => {
        const id = d.admin_auth_service || '';
        setSelectedSvc(id);
        setCurrentSvc(id ? (services.find(s => String(s.id) === id) || null) : null);
        setDefaultApp(d.default_app || '');
      })
      .catch(e => toast(e.message, true))
      .finally(() => setLoading(false));
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    if (!user || user.id < 0) return;
    setSSHLoading(true);
    api(token, 'GET', '/api/me/ssh-key')
      .then(d => setSSHKey(d.ssh_key))
      .catch(() => setSSHKey(null))
      .finally(() => setSSHLoading(false));
  }, [user]); // eslint-disable-line react-hooks/exhaustive-deps

  const oidcServices = services.filter(s => s.descriptor?.type === 'oidc');
  const sshService = services.find(s => s.descriptor?.type === 'ssh');

  const authServices = [
    ...oidcServices.map(s => ({ id: String(s.id), label: s.name, type: 'OIDC' })),
    ...(sshService ? [{ id: String(sshService.id), label: 'SSH Key', type: 'SSH' }] : []),
  ];

  const saveAuth = async () => {
    try {
      await api(token, 'PUT', '/api/settings', { admin_auth_service: selectedSvc });
      setCurrentSvc(oidcServices.find(s => String(s.id) === selectedSvc) || null);
      toast('Settings saved');
    } catch(e) { toast(e.message, true); }
  };

  const unlink = async () => {
    if (!confirm('Remove admin auth? The control panel will be open until auth is reconfigured.')) return;
    try {
      await api(token, 'PUT', '/api/settings', { admin_auth_service: '' });
      setSelectedSvc(''); setCurrentSvc(null);
      toast('Admin auth removed');
    } catch(e) { toast(e.message, true); }
  };

  const saveLanding = async () => {
    try {
      await api(token, 'PUT', '/api/settings', { default_app: defaultApp });
      toast('Landing page saved');
    } catch(e) { toast(e.message, true); }
  };

  return (
    <>
      <PageHead crumbs={['Security','Settings']} title="Settings" sub="Control panel configuration."/>
      <div className="setting-section">
        <h3 className="setting-heading">Admin authentication</h3>
        <p className="muted" style={{marginBottom:20,fontSize:13}}>
          Gate this control panel with an OIDC service. Once set, all API calls require a valid identity token.
        </p>
        {loading ? <span className="muted">Loading…</span> : (
          <>
            <div className="field" style={{maxWidth:380}}>
              <label>Auth service</label>
              <select className="input" value={selectedSvc} onChange={e => setSelectedSvc(e.target.value)}>
                <option value="">— None (open access) —</option>
                {authServices.map(s => <option key={s.id} value={s.id}>{s.label} ({s.type})</option>)}
              </select>
              {oidcServices.length === 0 && !sshService && (
                <span className="help">No auth services available. Add an OIDC service or use the built-in SSH service.</span>
              )}
            </div>
            <div style={{display:'flex',gap:8,marginTop:16}}>
              <button className="btn btn-primary" onClick={saveAuth}>Save</button>
              {currentSvc && <button className="btn btn-ghost" onClick={unlink}>Unlink</button>}
            </div>
          </>
        )}
      </div>

      <div className="setting-section" style={{marginTop:32}}>
        <h3 className="setting-heading">Default landing page</h3>
        <p className="muted" style={{marginBottom:20,fontSize:13}}>
          Choose where visitors land when they hit the root URL. Only hosted apps are available as targets.
        </p>
        {loading ? <span className="muted">Loading…</span> : (
          <div className="field" style={{maxWidth:380}}>
            <label>Landing page</label>
            <select className="input" value={defaultApp} onChange={e => setDefaultApp(e.target.value)}>
              <option value="">Control Panel</option>
              {apps.filter(a => a.details?.last_uploaded).map(a => (
                <option key={a.nonce} value={a.nonce}>{a.name}</option>
              ))}
            </select>
            {apps.filter(a => a.details?.last_uploaded).length === 0 && (
              <span className="help">No hosted apps yet. Upload web content to an app to make it available as a landing page.</span>
            )}
            <div style={{display:'flex',gap:8,marginTop:16}}>
              <button className="btn btn-primary" onClick={saveLanding}>Save</button>
            </div>
          </div>
        )}
      </div>

      {user && user.id > 0 && (
      <div className="setting-section" style={{marginTop:32}}>
        <h3 className="setting-heading">SSH Key</h3>
        <p className="muted" style={{marginBottom:20,fontSize:13}}>
          Generate an SSH key pair for authentication and agent forwarding. Only the public key is shown after creation.
        </p>
        {sshLoading ? <span className="muted">Loading…</span> : sshKey ? (
          <>
            <div style={{marginBottom:12}}>
              <Badge tone="green">Active</Badge>
              <span className="muted" style={{marginLeft:8,fontSize:13}}>{sshKey.key_type?.toUpperCase()} · {sshKey.fingerprint}</span>
            </div>
            <div className="field" style={{maxWidth:560}}>
              <label>Public key</label>
              <div style={{display:'flex',gap:8}}>
                <input className="input mono" value={sshKey.public_key?.trim()} readOnly style={{fontSize:12}} />
                <button className="btn btn-ghost" onClick={() => copyText(sshKey.public_key?.trim(), toast)}><Icon name="copy" size={14}/></button>
              </div>
            </div>
            <div style={{marginTop:12}}>
              <button className="btn btn-ghost" style={{color:'var(--red)'}} onClick={async () => {
                if (!confirm('Delete your SSH key? You\'ll need to generate a new one to use SSH auth.')) return;
                try { await api(token, 'DELETE', '/api/me/ssh-key'); setSSHKey(null); toast('SSH key deleted'); }
                catch(e) { toast(e.message, true); }
              }}>Delete key</button>
            </div>
          </>
        ) : (
          <button className="btn btn-primary" onClick={() => setShowGenModal(true)}><Icon name="key" size={14}/> Generate SSH Key</button>
        )}
      </div>
      )}

      {showGenModal && (
        <div className="modal-overlay" onClick={() => setShowGenModal(false)}>
          <div className="modal" onClick={e => e.stopPropagation()} style={{maxWidth:420}}>
            <h3 style={{marginBottom:16}}>Generate SSH Key</h3>
            <p className="muted" style={{fontSize:13,marginBottom:16}}>
              Choose a strong passphrase. You'll need it each time you log in via SSH. It cannot be recovered if forgotten.
            </p>
            <div className="field">
              <label>Passphrase</label>
              <input className="input" type="password" value={passphrase} onChange={e => setPassphrase(e.target.value)} placeholder="Min 8 characters" autoFocus />
            </div>
            <div className="field">
              <label>Confirm passphrase</label>
              <input className="input" type="password" value={passConfirm} onChange={e => setPassConfirm(e.target.value)} placeholder="Re-enter passphrase" />
            </div>
            <div style={{display:'flex',gap:8,justifyContent:'flex-end',marginTop:20}}>
              <button className="btn btn-ghost" onClick={() => { setShowGenModal(false); setPassphrase(''); setPassConfirm(''); }}>Cancel</button>
              <button className="btn btn-primary" disabled={passphrase.length < 8 || passphrase !== passConfirm} onClick={async () => {
                try {
                  const d = await api(token, 'POST', '/api/me/ssh-key', { passphrase });
                  setSSHKey(d.ssh_key);
                  setShowGenModal(false);
                  setPassphrase('');
                  setPassConfirm('');
                  toast('SSH key generated');
                } catch(e) { toast(e.message, true); }
              }}>Generate</button>
            </div>
          </div>
        </div>
      )}
    </>
  );
}

// ── Helpers ────────────────────────────────────────────────────────────

const fmtAuditTime = (iso) => {
  if (!iso) return '—';
  const d = new Date(iso);
  const sameYear = d.getFullYear() === new Date().getFullYear();
  return d.toLocaleString(undefined, {
    month: 'short', day: 'numeric',
    hour: '2-digit', minute: '2-digit',
    ...(sameYear ? {} : { year: 'numeric' }),
  });
};

// ── Routing ────────────────────────────────────────────────────────────

const getPageFromPath = () => {
  const seg = window.location.pathname.replace(/^\/control\/?/, '');
  return (seg && NAV.some(n => n.id === seg)) ? seg : 'home';
};

// ── App ────────────────────────────────────────────────────────────────

function AppShell() {
  const { user, token, authRequired, serviceName, login, logout, sessionExpired, clearExpired, authError } = useAuth();
  const [active,setActive] = useState(getPageFromPath);
  const [sidebarOpen,setSidebarOpen] = useState(false);

  useEffect(() => {
    const onPop = () => setActive(getPageFromPath());
    window.addEventListener('popstate', onPop);
    return () => window.removeEventListener('popstate', onPop);
  }, []);

  const navigate = (id) => {
    const path = id === 'home' ? '/control' : `/control/${id}`;
    history.pushState(null, '', path);
    setActive(id);
  };
  const [users,setUsers] = useState([]);
  const [apps,setApps] = useState([]);
  const [services,setServices] = useState([]);
  const [roles,setRoles] = useState([]);
  const [audit,setAudit] = useState([]);
  const [loading,setLoading] = useState(true);
  const toast = useToast();

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const [u,a,s,r,au] = await Promise.all([
        api(token, 'GET','/api/users'),
        api(token, 'GET','/api/apps'),
        api(token, 'GET','/api/services'),
        api(token, 'GET','/api/roles'),
        api(token, 'GET','/api/audit'),
      ]);
      setUsers(u.users||[]); setApps(a.apps||[]); setServices(s.services||[]); setRoles(r.roles||[]); setAudit(au.audit||[]);
    } catch(e) { if (!authRequired || user) toast('Failed to load: '+e.message, true); }
    setLoading(false);
  },[authRequired, user]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(()=>{
    if (authRequired && !user) return;
    load();
  },[authRequired, user]); // eslint-disable-line react-hooks/exhaustive-deps

  if (authRequired && !user) return <LoginScreen serviceName={serviceName} onLogin={login} authError={authError}/>;

  const counts = { users:users.length, apps:apps.length, services:services.length };

  if(loading) return <div style={{display:'grid',placeItems:'center',height:'100vh',color:'var(--ink-3)'}}>Loading…</div>;

  const activeLabel = NAV.find(n=>n.id===active)?.label;
  return (
    <div className="app-shell">
      <div className={`sidebar-scrim${sidebarOpen?' open':''}`} onClick={()=>setSidebarOpen(false)}/>
      <Sidebar active={active} onNav={navigate} counts={counts} user={user} onLogout={user ? logout : null} mobileOpen={sidebarOpen} onMobileClose={()=>setSidebarOpen(false)}/>
      <main className="main" data-screen-label={activeLabel}>
        <MobileTopBar onMenuOpen={()=>setSidebarOpen(true)} pageLabel={activeLabel}/>
        {sessionExpired && <SessionBanner onLogin={login} onDismiss={clearExpired}/>}
        {active==='home'     && <Overview users={users} apps={apps} services={services} audit={audit}/>}
        {active==='apps'     && <AppsView token={token} apps={apps} services={services} users={users} onRefresh={load}/>}
        {active==='services' && <ServicesView token={token} services={services} onRefresh={load}/>}
        {active==='users'    && <UsersView token={token} users={users} apps={apps} onRefresh={load}/>}
        {active==='roles'    && <RolesView roles={roles}/>}
        {active==='audit'    && <AuditView audit={audit}/>}
        {active==='settings' && <SettingsView token={token} services={services} apps={apps} user={user}/>}
      </main>
    </div>
  );
}

function App() {
  return (
    <ToastProvider>
      <AuthProvider>
        <AppShell/>
      </AuthProvider>
    </ToastProvider>
  );
}

ReactDOM.createRoot(document.getElementById('root')).render(<App/>);
