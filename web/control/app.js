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
    signout: <><path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4"/><polyline points="16 17 21 12 16 7"/><line x1="21" y1="12" x2="9" y2="12"/></>,
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

const AuthCtx = createContext({ user: null, authRequired: false, serviceName: '', sessionExpired: false, login: ()=>{}, logout: ()=>{}, clearExpired: ()=>{} });
const useAuth = () => useContext(AuthCtx);

function AuthProvider({ children }) {
  const [ready, setReady] = useState(false);
  const [user, setUser] = useState(null);
  const [authRequired, setAuthRequired] = useState(false);
  const [serviceName, setServiceName] = useState('');
  const [sessionExpired, setSessionExpired] = useState(false);
  const [authError, setAuthError] = useState('');

  useEffect(() => {
    _onUnauthorized = () => { setUser(null); setSessionExpired(true); };
    return () => { _onUnauthorized = null; };
  }, []);

  useEffect(() => {
    // Show auth errors from redirect (e.g. no_user, invalid_token)
    const params = new URLSearchParams(window.location.search);
    const err = params.get('auth_error');
    if (err) { setAuthError(err); history.replaceState(null, '', window.location.pathname); }
  }, []);

  useEffect(() => {
    (async () => {
      try {
        const cfg = window.__HOMESLICE_CONFIG || {};
        if (!cfg.authRequired) { setReady(true); return; }
        setAuthRequired(true);
        setServiceName(cfg.authServiceName || '');
        const token = getStoredToken();
        if (token?.id_token) {
          const r = await fetch('/api/me', { headers: { 'Authorization': 'Bearer ' + token.id_token } });
          if (r.ok) { const d = await r.json(); setUser(d.user); }
        }
      } catch {}
      setReady(true);
    })();
  }, []);

  const login = async () => {
    try {
      const r = await fetch('/service/login', { headers: { 'X-Admin-Auth': '1' } });
      const d = await r.json();
      window.location.href = d.url;
    } catch {}
  };

  const logout = () => { localStorage.removeItem('frebre_admin'); setUser(null); setSessionExpired(false); };
  const clearExpired = () => setSessionExpired(false);

  if (!ready) return <div style={{display:'grid',placeItems:'center',height:'100vh',color:'var(--ink-3)'}}>Loading…</div>;

  return (
    <AuthCtx.Provider value={{ user, authRequired, serviceName, sessionExpired, login, logout, clearExpired, authError }}>
      {children}
    </AuthCtx.Provider>
  );
}

function LoginScreen({ serviceName, onLogin, authError }) {
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
          <span>v{window.__HOMESLICE_CONFIG?.version || 'dev'}</span>
        </div>
      </aside>
      <main className="login-main">
        <div className="login-card">
          <div>
            <h2>Sign in to Fresh Breath</h2>
            <p className="lead">Use your work account to access the control panel.</p>
          </div>
          {authError && (
            <div className="login-error">
              <Icon name="bell" size={14}/>
              <span>{errorMessages[authError] || 'Authentication error.'}</span>
            </div>
          )}
          <button className="oidc-btn oidc-primary" onClick={onLogin}>
            <span className="glyph"><Icon name="lock" size={16}/></span>
            Continue with {serviceName || 'your identity provider'}
            <span className="meta">OIDC</span>
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
  { id:'users',    label:'Users',    icon:'users', countKey:'users' },
  { id:'apps',     label:'Apps',     icon:'apps', countKey:'apps' },
  { id:'services', label:'Services', icon:'cog', countKey:'services' },
  { id:'roles',    label:'Roles',    icon:'shield' },
  { id:'audit',    label:'Audit log',icon:'log' },
  { id:'settings', label:'Settings', icon:'lock' },
];

function Sidebar({ active, onNav, counts, user, onLogout }) {
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
    localStorage.setItem('frebre-theme', next);
    setDark(next === 'dark');
  };
  return (
    <aside className="sidebar">
      <div className="sb-brand">
        <span style={{display:'flex',alignItems:'center',gap:10}}><span className="brand-mark"/>Fresh Breath</span>
        <button className="theme-toggle" onClick={toggleTheme} title={dark ? 'Switch to light' : 'Switch to dark'}>
          <Icon name={dark ? 'sun' : 'moon'} size={16}/>
        </button>
      </div>
      <div>
        <div className="sb-section">Workspace</div>
        <div className="sb-nav">
          {workspace.map(n=>NavLink(n,active,onNav,counts))}
        </div>
      </div>
      <div>
        <div className="sb-section">Security</div>
        <div className="sb-nav">
          {security.map(n=>NavLink(n,active,onNav,counts))}
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
          v{window.__HOMESLICE_CONFIG?.version || 'dev'}
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

function getStoredToken() {
  try { return JSON.parse(localStorage.getItem('frebre_admin')); } catch { return null; }
}

async function api(method, path, body) {
  const opts = { method, headers: {} };
  const token = getStoredToken();
  if (token?.id_token) opts.headers['Authorization'] = 'Bearer ' + token.id_token;
  if (body) { opts.headers['Content-Type']='application/json'; opts.body=JSON.stringify(body); }
  const r = await fetch(path, opts);
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

function buildPrompt(app, appServices) {
  const fbURL = window.__HOMESLICE_CONFIG?.apiBase || window.location.origin;
  const serviceLines = appServices.length
    ? ("\nIntegrations: (be sure to use these exact URLs)\n" + appServices.map(s => `  - ${s.name} (${s.descriptor?.type?.toLocaleUpperCase()}): "${s.url}"`).join('\n'))
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
          <span className="lbl">Users</span>
          <span className="val">{users.length}</span>
          <span className="sub">{activeUsers} active · {users.length-activeUsers} other</span>
        </div>
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
      </div>

      <div style={{display:'grid',gridTemplateColumns:'1.4fr 1fr',gap:24}}>
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
                      <span className="tl-when">{a.when}</span>
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

function UsersView({ users, apps, onRefresh }) {
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
    try { await api('DELETE','/api/users/'+id); toast('User deleted'); onRefresh(); }
    catch(e) { toast(e.message,true); }
  };

  return (
    <>
      <PageHead
        crumbs={['Workspace','Users']}
        title="Users"
        sub="People with access."
        actions={<button className="btn btn-primary" onClick={()=>setEditing('new')}><Icon name="plus" size={14}/> New user</button>}
      />
      <Toolbar
        search={q} onSearch={setQ}
        placeholder="Search by name or email…"
        filters={['Active','Invited','Suspended']}
        activeFilter={filter} onFilter={setFilter}
      />
      <div className="table-wrap">
        <table className="tbl">
          <thead><tr><th style={{width:'28%'}}>Name</th><th>Role</th><th>Status</th><th>Apps</th><th>Last seen</th><th style={{width:80}}></th></tr></thead>
          <tbody>
            {filtered.map(u=>
              <tr key={u.id}>
                <td>
                  <div className="user-cell">
                    <Avatar name={u.name}/>
                    <div className="meta"><b>{u.name}</b><span>{u.email}</span></div>
                  </div>
                </td>
                <td><Badge tone={roleTone(u.role)}>{u.role}</Badge></td>
                <td><Badge tone={statusTone(u.status)}>{u.status}</Badge></td>
                <td><UserAppTags apps={u.apps} appList={apps}/></td>
                <td className="muted">{u.last_seen||'—'}</td>
                <td>
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
      <UserDrawer user={editing} apps={apps} onClose={()=>setEditing(null)} onSaved={onRefresh}/>
    </>
  );
}

function UserDrawer({ user, apps, onClose, onSaved }) {
  const [form,setForm] = useState({name:'',email:'',role:'Member',status:'Active',apps:[]});
  const [loading,setLoading] = useState(false);
  const toast = useToast();
  const isNew = user==='new';
  const isEdit = user && user.id;

  useEffect(()=>{
    if(isEdit) {
      setForm({name:user.name,email:user.email,role:user.role||'Member',status:user.status||'Active',apps:[]});
      setLoading(true);
      api('GET','/api/users/'+user.id+'/apps')
        .then(d=>{
          setForm(f=>({...f,apps:d.apps||[]}));
        })
        .catch(e=>toast(e.message,true))
        .finally(()=>setLoading(false));
    } else {
      setForm({name:'',email:'',role:'Member',status:'Active',apps:[]});
    }
  },[user]); // eslint-disable-line react-hooks/exhaustive-deps

  const save = async () => {
    try {
      let uid;
      if(isEdit) {
        await api('PUT','/api/users/'+user.id,form);
        await api('PUT','/api/users/'+user.id+'/apps',{apps:form.apps||[]});
        toast('User updated');
      } else {
        const resp = await api('POST','/api/users',form);
        uid = resp.id;
        await api('PUT','/api/users/'+uid+'/apps',{apps:form.apps||[]});
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

function AppsView({ apps, services, users, onRefresh }) {
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
    try { await api('DELETE','/api/apps/'+nonce); toast('App deleted'); onRefresh(); }
    catch(e) { toast(e.message,true); }
  };

  const copyNonce = (nonce) => copyText(nonce, toast);

  const copyPrompt = async (a) => {
    try {
      const r = await api('GET', '/api/apps/' + a.nonce + '/services');
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
        <table className="tbl">
          <thead><tr><th style={{width:'26%'}}>Name</th><th>Environment</th><th>Owner</th><th>Members</th><th>Services</th><th style={{width:80}}></th></tr></thead>
          <tbody>
            {filtered.map(a=>
              <tr key={a.nonce}>
                <td>
                  <div className="user-cell">
                    <div className="avatar" style={{
                      width:32,height:32,borderRadius:8,
                      background:`linear-gradient(135deg,oklch(0.85 0.06 ${hashHue(a.name)}),oklch(0.55 0.1 ${(hashHue(a.name)+30)%360}))`,
                      fontSize:13,color:'white',display:'grid',placeItems:'center'
                    }}>{a.name?.[0]}</div>
                    <div className="meta">
                      <b>{a.name}</b>
                      <span className="mono" style={{cursor:'pointer'}} onClick={()=>copyNonce(a.nonce)} title={`${a.nonce} — click to copy`}>
                        {a.nonce.slice(0,8)}… <span style={{opacity:0.6,verticalAlign:'middle',marginLeft:2}}><Icon name="copy" size={12}/></span>
                      </span>
                    </div>
                  </div>
                </td>
                <td><Badge tone={envTone(a.environment)}>{a.environment||'—'}</Badge></td>
                <td>{a.owner_name||<span className="muted">—</span>}</td>
                <td className="mono">{a.member_count??0}</td>
                <td className="mono">{a.service_count??0}</td>
                <td>
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
      <AppDrawer app={editing} services={services} users={users} apps={apps} onClose={()=>setEditing(null)} onSaved={onRefresh}/>
    </>
  );
}

function AppDrawer({ app, services, users, apps, onClose, onSaved }) {
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
        api('GET','/api/apps/'+app.nonce+'/services'),
        api('GET','/api/apps/'+app.nonce+'/members'),
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
        await api('PUT','/api/apps/'+app.nonce,payload);
        await api('PUT','/api/apps/'+app.nonce+'/members',{members:form.members||[]});
        await api('PUT','/api/apps/'+app.nonce+'/services',{services:form.services||[]});
        toast('App updated');
      } else {
        const resp = await api('POST','/api/apps',payload);
        nonce = resp.nonce;
        await api('PUT','/api/apps/'+nonce+'/members',{members:form.members||[]});
        await api('PUT','/api/apps/'+nonce+'/services',{services:form.services||[]});
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
    </Drawer>
  );
}

// ── Services ───────────────────────────────────────────────────────────

function ServicesView({ services, onRefresh }) {
  const [q,setQ] = useState('');
  const [editing,setEditing] = useState(null);
  const toast = useToast();

  const filtered = services.filter(s=>{
    if(q && !(`${s.name} ${s.url}`.toLowerCase().includes(q.toLowerCase()))) return false;
    return true;
  });

  const remove = async (id) => {
    let apps = [];
    try { const r = await api('GET','/api/services/'+id+'/apps'); apps = r.apps||[]; }
    catch(e) { /* ignore */ }
    let msg = 'Delete this service?';
    if(apps.length>0) {
      msg += `\n\nIt's used by ${apps.length} app${apps.length>1?'s':''}:\n${apps.map(a=>a.name).join(', ')}`;
    }
    if(!confirm(msg)) return;
    try { await api('DELETE','/api/services/'+id); toast('Service deleted'); onRefresh(); }
    catch(e) { toast(e.message,true); }
  };

  return (
    <>
      <PageHead
        crumbs={['Workspace','Services']}
        title="Services"
        sub="Registered MCP, OAuth, and API providers."
        actions={<button className="btn btn-primary" onClick={()=>setEditing('new')}><Icon name="plus" size={14}/> New service</button>}
      />
      <Toolbar search={q} onSearch={setQ} placeholder="Search services…"/>
      <div className="table-wrap">
        <table className="tbl">
          <thead><tr><th style={{width:'25%'}}>Name</th><th>URL</th><th>Type</th><th>Proxied</th><th style={{width:80}}></th></tr></thead>
          <tbody>
            {filtered.map(s=>
              <tr key={s.id}>
                <td><b>{s.name}</b></td>
                <td>
                  <span
                    className="mono"
                    style={{fontSize:12.5, color:'var(--ink-3)', cursor:'pointer'}}
                    onClick={()=>copyText(s.url, toast)}
                    title={`${s.url} — click to copy`}
                  >
                    {s.url.length>48?s.url.slice(0,48)+'…':s.url} <span style={{opacity:0.6,verticalAlign:'middle',marginLeft:2}}><Icon name="copy" size={12}/></span>
                  </span>
                </td>
                <td><Badge dot={false} tone="gray">{s.descriptor?.type?.toLocaleUpperCase()||'—'}</Badge></td>
                <td>{s.descriptor?.proxied ? <Badge tone="blue">Proxied</Badge> : <span className="muted">—</span>}</td>
                <td>
                  <div className="row-actions">
                    <button className="btn btn-icon btn-ghost" onClick={()=>setEditing(s)} title="Edit"><Icon name="edit" size={14}/></button>
                    <button className="btn btn-icon btn-ghost" onClick={()=>remove(s.id)} title="Delete"><Icon name="trash" size={14}/></button>
                  </div>
                </td>
              </tr>
            )}
          </tbody>
        </table>
        {filtered.length===0 && <div className="empty"><b>No services found.</b></div>}
      </div>
      <ServiceDrawer service={editing} onClose={()=>setEditing(null)} onSaved={onRefresh}/>
    </>
  );
}

function ServiceDrawer({ service, onClose, onSaved }) {
  const [form,setForm] = useState({name:'',url:'',descriptor:{type:'mcp',proxied:false}});
  const toast = useToast();
  const isNew = service==='new';
  const isEdit = service && service.id;

  useEffect(()=>{
    if(isEdit) setForm({name:service.name,url:service.url,descriptor:{...service.descriptor}});
    else setForm({name:'',url:'',descriptor:{type:'mcp',proxied:false}});
  },[service]);

  const updDesc = (k,v) => setForm(f=>({...f,descriptor:{...f.descriptor,[k]:v}}));

  const save = async () => {
    try {
      if(isEdit) { await api('PUT','/api/services/'+service.id,form); toast('Service updated'); }
      else { await api('POST','/api/services',form); toast('Service created'); }
      onClose(); onSaved();
    } catch(e) { toast(e.message,true); }
  };

  return (
    <Drawer
      open={isNew || isEdit} title={isNew?'New service':'Edit service'}
      onClose={onClose}
      footer={<>
        <button className="btn btn-ghost" onClick={onClose}>Cancel</button>
        <button className="btn btn-primary" onClick={save}>{isNew?'Create':'Save'}</button>
      </>}
    >
      <div className="field"><label>Name</label><input className="input" value={form.name} onChange={e=>setForm(f=>({...f,name:e.target.value}))}/></div>
      <div className="field"><label>URL</label><input className="input mono" value={form.url} onChange={e=>setForm(f=>({...f,url:e.target.value}))}/></div>
      <div className="field-row">
        <div className="field"><label>Type</label>
          <select className="input" value={form.descriptor.type} onChange={e=>updDesc('type',e.target.value)}>
            <option value="mcp">MCP</option><option value="api">API</option><option value="oidc">OIDC</option>
          </select>
        </div>
        <div className="field"><label>Proxied</label>
          <select className="input" value={form.descriptor.proxied?'true':'false'} onChange={e=>updDesc('proxied',e.target.value==='true')}>
            <option value="false">No</option><option value="true">Yes</option>
          </select>
        </div>
      </div>
      {form.descriptor.type==='api' && (
        <>
          <div className="field-row">
            <div className="field"><label>Auth</label>
              <select className="input" value={form.descriptor.auth||''} onChange={e=>updDesc('auth',e.target.value)}>
                <option value="">OAuth (default)</option><option value="key">API key</option>
              </select>
            </div>
            {form.descriptor.auth==='key' ?
              <div className="field"><label>API Key</label><input className="input mono" type="password" value={form.descriptor.api_key||''} onChange={e=>updDesc('api_key',e.target.value)}/></div> :
              <div className="field"><label>Client ID</label><input className="input mono" value={form.descriptor.client_id||''} onChange={e=>updDesc('client_id',e.target.value)}/></div>}
          </div>
          {form.descriptor.auth!=='key' ? (
            <>
              <div className="field"><label>OAuth URL</label><input className="input mono" value={form.descriptor.oauth_url||''} onChange={e=>updDesc('oauth_url',e.target.value)} placeholder="https://provider.com/oauth"/></div>
              <div className="field"><label>Client Secret</label><input className="input mono" type="password" value={form.descriptor.client_secret||''} onChange={e=>updDesc('client_secret',e.target.value)}/></div>
            </>) :
            <div className="field"><label>API Key Header</label><input className="input mono" value={form.descriptor.header||''} onChange={e=>updDesc('header',e.target.value)} placeholder="X-API-Key (or empty for Bearer)"/></div>
          }
        </>
      )}
      {form.descriptor.type==='oidc' && (
        <>
          <div className="field"><label>OAuth URL</label><input className="input mono" value={form.descriptor.oauth_url||''} onChange={e=>updDesc('oauth_url',e.target.value)} placeholder="https://provider.com/oauth"/></div>
          <div className="field-row">
            <div className="field"><label>Client ID</label><input className="input mono" value={form.descriptor.client_id||''} onChange={e=>updDesc('client_id',e.target.value)}/></div>
            <div className="field"><label>Scopes</label><input className="input" value={form.descriptor.scopes||''} onChange={e=>updDesc('scopes',e.target.value)} placeholder="openid profile email"/></div>
          </div>
          <div className="field"><label>Client Secret</label><input className="input mono" type="password" value={form.descriptor.client_secret||''} onChange={e=>updDesc('client_secret',e.target.value)}/></div>
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
    { group:'Users',   items:['Read','Invite','Suspend','Delete'] },
    { group:'Apps',    items:['Read','Create','Edit','Delete'] },
    { group:'Services',items:['Read','Create','Edit','Delete'] },
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
                <span className="tl-when">{a.when||a.created_at}</span>
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

function SettingsView({ services }) {
  const [selectedSvc, setSelectedSvc] = useState('');
  const [currentSvc, setCurrentSvc] = useState(null);
  const [loading, setLoading] = useState(true);
  const toast = useToast();

  useEffect(() => {
    api('GET', '/api/settings')
      .then(d => {
        const id = d.admin_auth_service || '';
        setSelectedSvc(id);
        setCurrentSvc(id ? (services.find(s => String(s.id) === id) || null) : null);
      })
      .catch(e => toast(e.message, true))
      .finally(() => setLoading(false));
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  const oidcServices = services.filter(s => s.descriptor?.type === 'oidc');

  const save = async () => {
    try {
      await api('PUT', '/api/settings', { admin_auth_service: selectedSvc });
      setCurrentSvc(oidcServices.find(s => String(s.id) === selectedSvc) || null);
      toast('Settings saved');
    } catch(e) { toast(e.message, true); }
  };

  const unlink = async () => {
    if (!confirm('Remove admin auth? The control panel will be open until auth is reconfigured.')) return;
    try {
      await api('PUT', '/api/settings', { admin_auth_service: '' });
      setSelectedSvc(''); setCurrentSvc(null);
      toast('Admin auth removed');
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
                {oidcServices.map(s => <option key={s.id} value={String(s.id)}>{s.name}</option>)}
              </select>
              {oidcServices.length === 0 && (
                <span className="help">No OIDC services registered. Add one in Services first.</span>
              )}
            </div>
            <div style={{display:'flex',gap:8,marginTop:16}}>
              <button className="btn btn-primary" onClick={save}>Save</button>
              {currentSvc && <button className="btn btn-ghost" onClick={unlink}>Unlink</button>}
            </div>
          </>
        )}
      </div>
    </>
  );
}

// ── App ────────────────────────────────────────────────────────────────

function AppShell() {
  const { user, authRequired, serviceName, login, logout, sessionExpired, clearExpired, authError } = useAuth();
  const [active,setActive] = useState('home');
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
        api('GET','/api/users'),
        api('GET','/api/apps'),
        api('GET','/api/services'),
        api('GET','/api/roles'),
        api('GET','/api/audit'),
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

  return (
    <div className="app-shell">
      <Sidebar active={active} onNav={setActive} counts={counts} user={user} onLogout={user ? logout : null}/>
      <main className="main" data-screen-label={NAV.find(n=>n.id===active)?.label}>
        {sessionExpired && <SessionBanner onLogin={login} onDismiss={clearExpired}/>}
        {active==='home'     && <Overview users={users} apps={apps} services={services} audit={audit}/>}
        {active==='users'    && <UsersView users={users} apps={apps} onRefresh={load}/>}
        {active==='apps'     && <AppsView apps={apps} services={services} users={users} onRefresh={load}/>}
        {active==='services' && <ServicesView services={services} onRefresh={load}/>}
        {active==='roles'    && <RolesView roles={roles}/>}
        {active==='audit'    && <AuditView audit={audit}/>}
        {active==='settings' && <SettingsView services={services}/>}
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
