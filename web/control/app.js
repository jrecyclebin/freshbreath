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
    sparkle: <><path d="M12 2l2.2 7.8L22 12l-7.8 2.2L12 22l-2.2-7.8L2 12l7.8-2.2z"/></>,
    bell:    <><path d="M6 9a6 6 0 0 1 12 0c0 5 2 6 2 7H4c0-1 2-2 2-7z"/><path d="M10 19a2 2 0 0 0 4 0"/></>,
    refresh: <><path d="M3 12a9 9 0 0 1 15-6.7L21 8"/><path d="M21 3v5h-5"/><path d="M21 12a9 9 0 0 1-15 6.7L3 16"/><path d="M3 21v-5h5"/></>,
    lock:    <><rect x="5" y="11" width="14" height="9" rx="2"/><path d="M8 11V8a4 4 0 0 1 8 0v3"/></>,
    key:     <><circle cx="8" cy="15" r="4"/><path d="M10.9 12.1L20 3"/><path d="M17 6l2.5 2.5"/><path d="M14.5 8.5L17 11"/></>,
    mail:    <><rect x="3" y="5" width="18" height="14" rx="2"/><path d="M3 7l9 6 9-6"/></>,
    cog:     <><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.8-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1-1.5 1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.7 1.7 0 0 0 1.5-1 1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3 1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.8 1.7 1.7 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z"/></>,
    plug:    <><path d="M12 22v-5"/><path d="M9 8V2"/><path d="M15 8V2"/><path d="M18 8v5a6 6 0 0 1-12 0V8z"/></>,
    signout: <><path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4"/><polyline points="16 17 21 12 16 7"/><line x1="21" y1="12" x2="9" y2="12"/></>,
    menu:    <><path d="M3 6h18M3 12h18M3 18h18"/></>,
    moon:    <><path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z"/></>,
    sun:     <><circle cx="12" cy="12" r="5"/><path d="M12 1v2M12 21v2M4.22 4.22l1.42 1.42M18.36 18.36l1.42 1.42M1 12h2M21 12h2M4.22 19.78l1.42-1.42M18.36 5.64l1.42-1.42"/></>,
    clock:   <><circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 3"/></>,
    upload:  <><path d="M12 16V4"/><path d="M7 9l5-5 5 5"/><path d="M5 20h14"/></>,
    back:    <><path d="M19 12H5"/><path d="M12 19l-7-7 7-7"/></>,
    tag:     <><path d="M20.6 13.4L13.4 20.6a2 2 0 0 1-2.8 0l-7.2-7.2A2 2 0 0 1 2.8 12V5a2 2 0 0 1 2-2h7a2 2 0 0 1 1.4.6l7.4 7.4a2 2 0 0 1 0 2.4z"/><circle cx="7.5" cy="7.5" r="1.2"/></>,
    chevron: <><path d="M6 9l6 6 6-6"/></>,
  };
  return <svg {...c}>{p[name] || p.more}</svg>;
};

// ── UI primitives ──────────────────────────────────────────────────────

// A hand-drawn "window with a view", deterministic from the user's email
// (falling back to their name). The drawing lives in window-avatar.js, a
// dependency-free ES module that also works in Node for SSR / .svg
// caching; control.html bridges it onto window.windowAvatar (same pattern
// as frbr.js → window.FrBr) so this script-mode app can reach it.
const Avatar = ({ name, email, size = 32 }) => {
  const seed = (email || name || '').trim();
  if (!window.windowAvatar) {
    // Bootstrap hasn't run — shouldn't happen (control.html's module
    // ordering guarantees it). Don't paint a blank box.
    const initial = ((name || '?').trim()[0] || '?').toUpperCase();
    return <div className="avatar" style={{width:size,height:size,fontSize:size*0.42,fontWeight:600}}>{initial}</div>;
  }
  return <div className="avatar" style={{width:size,height:size}}
    dangerouslySetInnerHTML={{__html: window.windowAvatar(seed, {size, color: 'currentColor'})}}/>;
};

// A hosted app's favicon, if it serves one at /favicon.ico; otherwise the
// app's first initial on a coloured tile. Tried per-card so a missing
// icon never breaks the grid.
const Favicon = ({ route, name }) => {
  const [err, setErr] = useState(false);
  if (err || !route) {
    return <Icon name="apps" size={14}/>;
  }
  return <img className="hosted-fav" src={route + '/favicon.ico'} alt="" onError={()=>setErr(true)}/>;
};

const Badge = ({ tone = "gray", dot = true, children }) => (
  <span className={`badge ${tone}`}>{dot && <span className="dot"/>}{children}</span>
);

const statusTone = (s='') => ({ Active:'green', Invited:'blue', Suspended:'red' }[s] || 'gray');
// Only mcp/api services have the proxied switch; tasks, virtuals and the
// built-in SSH service always run server-side.
const unproxied = (s) => (s.descriptor?.type === 'mcp' || s.descriptor?.type === 'api') && !s.descriptor?.proxied;
const envTone = (e='') => ({ Production:'green', Staging:'amber', Development:'blue' }[e] || 'gray');
const envShort = (e='') => ({ Production:'Prod', Staging:'Staging', Development:'Dev' }[e] || e);
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

// Drawers nest: a service drawer opens an auth drawer over itself so you
// can make a record without losing the form that needed one. Both listen
// for Escape on window, so without a stack one keypress closes both and
// takes the half-filled form with it. Only the topmost answers.
const drawerStack = [];

const Drawer = ({ open, title, onClose, footer, children }) => {
  const closeRef = useRef(onClose);
  closeRef.current = onClose;
  const me = useRef({});
  useEffect(() => {
    if (!open) return;
    const self = me.current;
    drawerStack.push(self);
    const esc = (e) => {
      if (e.key === 'Escape' && drawerStack[drawerStack.length - 1] === self) closeRef.current();
    };
    window.addEventListener('keydown', esc);
    return () => {
      window.removeEventListener('keydown', esc);
      const i = drawerStack.indexOf(self);
      if (i >= 0) drawerStack.splice(i, 1);
    };
    // Deliberately keyed on `open` alone: onClose is usually a fresh inline
    // closure each render, and re-running this would shuffle the stack
    // order out from under a nested drawer.
  }, [open]); // eslint-disable-line react-hooks/exhaustive-deps
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

const AuthCtx = createContext({ user: null, session: null, authRequired: false, gateName: '', sessionExpired: false, login: ()=>{}, logout: ()=>{}, clearExpired: ()=>{}, setUser: ()=>{} });
const useAuth = () => useContext(AuthCtx);

// What the signed-in user may do in the panel. With auth off the panel runs
// as the setup account, a Superuser. The server enforces all of this; these
// only keep the panel from offering what would answer 403.
const isAdminRole = (user, authRequired) => !authRequired || user?.role === 'Superuser' || user?.role === 'Admin';
const isSuperuserRole = (user, authRequired) => !authRequired || user?.role === 'Superuser';
const useRoles = () => {
  const { user, authRequired } = useAuth();
  return { isAdmin: isAdminRole(user, authRequired), isSuperuser: isSuperuserRole(user, authRequired) };
};

function AuthProvider({ children }) {
  const [ready, setReady] = useState(false);
  const [user, setUser] = useState(null);
  const [session, setSession] = useState(null);
  const [authRequired, setAuthRequired] = useState(false);
  const [gateName, setGateName] = useState('');
  const [sessionExpired, setSessionExpired] = useState(false);
  const [authError, setAuthError] = useState('');

  useEffect(() => {
    _onUnauthorized = () => { setUser(null); setSession(null); setSessionExpired(true); };
    return () => { _onUnauthorized = null; };
  }, []);

  useEffect(() => {
    (async () => {
      try {
        if (!window.FrBr || !window.__HOMESLICE_CONFIG) {
          // Script-ordering bug: frbr.js (which defines window.FrBr and
          // __HOMESLICE_CONFIG) must execute before this app. Without it we
          // can't read authRequired and would silently skip a stored session.
          console.error('Fresh Breath: frbr.js/config not loaded before app init — script ordering bug');
        }
        const cfg = window.__HOMESLICE_CONFIG || {};
        if (!cfg.authRequired) { setReady(true); return; }
        setAuthRequired(true);
        setGateName(cfg.authRecordName || '');

        // The store is the session. Restoring one is a synchronous read of
        // frbr:auth:<gate id> — no round trip, and nothing to persist here,
        // because frbr.js owns persistence now including refresh rotation.
        const restored = window.FrBr.currentSession();
        if (restored) {
          setSession(restored);
          const d = await frbr(restored, 'GET', '/api/me');
          setUser(d.user);
        }
      } catch (e) {
        console.error('Fresh Breath: auth restore failed', e);
      }
      setReady(true);
    })();
  }, []);

  // One verb. The control panel logs in to its own gate, so login() takes
  // no service URL — whatever that gate is (OIDC, a passphrase, a key),
  // frbr.js runs the right flow.
  const login = async () => {
    const fresh = await window.FrBr.login();
    setSessionExpired(false);
    setSession(fresh);
    const d = await frbr(fresh, 'GET', '/api/me');
    setUser(d.user);
  };

  const logout = () => { window.FrBr.signOut(); setSession(null); setUser(null); setSessionExpired(false); };
  const clearExpired = () => setSessionExpired(false);

  if (!ready) return <div style={{display:'grid',placeItems:'center',height:'100vh',color:'var(--ink-3)'}}>Loading…</div>;

  return (
    <AuthCtx.Provider value={{ user, session, authRequired, gateName, sessionExpired, login, logout, clearExpired, authError, setUser }}>
      {children}
    </AuthCtx.Provider>
  );
}

// How each kind of gate introduces itself on the sign-in screen. The kind
// comes from env.js, so the button says what will actually happen when it
// is clicked rather than assuming everyone signs in the same way.
const GATE_PROMPTS = {
  ssh_key:   { lead: 'Sign in with your SSH key passphrase.', cta: () => 'Sign in with your passphrase', tag: 'SSH' },
  api_key:   { lead: 'This panel is behind an API key.',      cta: () => 'Enter the API key',            tag: 'KEY' },
  oidc:      { lead: 'Use your work account to access the control panel.', cta: (n) => `Continue with ${n || 'your identity provider'}`, tag: 'OIDC' },
  oauth2:    { lead: 'Use your work account to access the control panel.', cta: (n) => `Continue with ${n || 'your provider'}`,          tag: 'OAUTH2' },
};

function LoginScreen({ gateName, onLogin, authError }) {
  const cfg = window.__HOMESLICE_CONFIG || {};
  const gate = GATE_PROMPTS[cfg.authKind] || GATE_PROMPTS.oidc;
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState('');
  const errorMessages = {
    no_user: 'Your account is not registered. Please contact an administrator.',
    invalid_token: 'Authentication failed. Please try again.',
    no_email: 'Your identity provider did not share an email address.',
  };
  // A login can fail for reasons the server never hears about — a blocked
  // popup, a closed window — so the click, not the server, reports them.
  const attempt = async () => {
    setBusy(true); setFailure('');
    try { await onLogin(); }
    catch (e) { setFailure(e.message || 'Login failed'); }
    finally { setBusy(false); }
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
            <p className="lead">{gate.lead}</p>
          </div>
          {(authError || failure) && (
            <div className="login-error">
              <Icon name="bell" size={14}/>
              <span>{failure || errorMessages[authError] || 'Authentication error.'}</span>
            </div>
          )}
          <button className="oidc-btn oidc-primary" onClick={attempt} disabled={busy}>
            <span className="glyph"><Icon name="lock" size={16}/></span>
            {busy ? 'Signing in…' : gate.cta(gateName)}
            <span className="meta">{gate.tag}</span>
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

// The user area: three pages — people, their roles, what they did —
// reachable from one icon in the top bar and cross-linked by tabs
// once you're inside one of them.
const USER_AREA = [
  { id: 'users',  label: 'Users', adminOnly: true },
  { id: 'roles',  label: 'Roles' },
  { id: 'audit',  label: 'Audit log' },
];
const userAreaFor = (isAdmin) => USER_AREA.filter(p => isAdmin || !p.adminOnly);

// Menu is the one piece of shared chrome in the top bar: a button that
// opens a small dropdown pinned under itself. Outside clicks and Escape
// close it; choosing an item closes it too.
function Menu({ label, icon, children, avatar }) {
  const [open, setOpen] = useState(false);
  const ref = useRef(null);
  useEffect(() => {
    if (!open) return;
    const away = (e) => { if (!ref.current?.contains(e.target)) setOpen(false); };
    const esc = (e) => { if (e.key === 'Escape') setOpen(false); };
    document.addEventListener('mousedown', away);
    window.addEventListener('keydown', esc);
    return () => {
      document.removeEventListener('mousedown', away);
      window.removeEventListener('keydown', esc);
    };
  }, [open]);
  return (
    <div className="menu" ref={ref}>
      <button className={'menu-btn' + (open ? ' open' : '')} title={label}
              onClick={() => setOpen(o => !o)}>
        {avatar || <Icon name={icon} size={18}/>}
      </button>
      {open && (
        <div className="menu-pop" onClick={() => setOpen(false)}>
          {label && <div className="menu-heading">{label}</div>}
          {children}
        </div>
      )}
    </div>
  );
}

function MenuItem({ icon, children, onClick, tone }) {
  return (
    <button className={'menu-item' + (tone ? ' tone-' + tone : '')} onClick={onClick}>
      {icon && <span className="icn"><Icon name={icon} size={15}/></span>}
      {children}
    </button>
  );
}

// A "+ New" dropdown above the entity table — one entry per type, so any
// kind of record can be added from any view without leaving the table.
function NewEntityMenu({ navigate }) {
  const [open, setOpen] = useState(false);
  const ref = useRef(null);
  useEffect(() => {
    if (!open) return;
    const away = (e) => { if (!ref.current?.contains(e.target)) setOpen(false); };
    const esc = (e) => { if (e.key === 'Escape') setOpen(false); };
    document.addEventListener('mousedown', away);
    window.addEventListener('keydown', esc);
    return () => {
      document.removeEventListener('mousedown', away);
      window.removeEventListener('keydown', esc);
    };
  }, [open]);
  const items = [
    { id: 'app', label: 'New app', icon: 'apps', page: 'app', params: { isNew: true } },
    { id: 'service', label: 'New service', icon: 'plug', page: 'service', params: { isNew: true } },
    { id: 'auth', label: 'New auth record', icon: 'key', page: 'authrecord', params: { isNew: true } },
  ];
  return (
    <div className="menu new-menu" ref={ref}>
      <button className={'btn btn-primary btn-sm new-btn' + (open ? ' open' : '')} onClick={() => setOpen(o => !o)}>
        <Icon name="plus" size={14}/>
        <span className="new-label">New <Icon name="chevron" size={13}/></span>
      </button>
      {open && (
        <div className="menu-pop" onClick={() => setOpen(false)}>
          {items.map(it => (
            <MenuItem key={it.id} icon={it.icon} onClick={() => navigate(it.page, it.params)}>{it.label}</MenuItem>
          ))}
        </div>
      )}
    </div>
  );
}

function TopBar({ user, onNav, onLogout }) {
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
  const { isAdmin, isSuperuser } = useRoles();
  const displayName = user?.name || 'Admin';
  const displayRole = user?.role || 'Superuser';
  // Only a real account has a profile; the auth-off setup account has none.
  const hasProfile = !!(user && user.id > 0);
  return (
    <header className="topbar">
      <div className="topbar-inner">
        <button className="tb-brand" onClick={() => onNav('home')} title="Fresh Breath">
          <img src="/control/images/frbr-sm.png" alt="Fresh Breath" className="brand-logo"/>
        </button>
        <div className="tb-right">
          <Menu icon="users">
            {userAreaFor(isAdmin).map(p =>
              <MenuItem key={p.id} onClick={() => onNav(p.id)}>{p.label}</MenuItem>
            )}
          </Menu>
          <Menu avatar={<Avatar name={displayName} email={user?.email} size={30}/>}>
            {hasProfile ? (
              <button className="menu-heading user menu-heading-link" title="Your profile" onClick={() => onNav('profile')}>
                <b>{displayName}</b>
                <span>{displayRole}</span>
              </button>
            ) : (
              <div className="menu-heading user">
                <b>{displayName}</b>
                <span>{displayRole}</span>
              </div>
            )}
            <MenuItem icon={dark ? 'sun' : 'moon'} onClick={toggleTheme}>
              {dark ? 'Light mode' : 'Dark mode'}
            </MenuItem>
            {isSuperuser && <MenuItem icon="cog" onClick={() => onNav('settings')}>Settings</MenuItem>}
            {user && user.id && <MenuItem icon="signout" tone="red" onClick={onLogout}>Sign out</MenuItem>}
            <div className="menu-foot" title={window.__HOMESLICE_CONFIG?.commit || 'none'}>
              {window.__HOMESLICE_CONFIG?.version || 'dev'}
            </div>
          </Menu>
        </div>
      </div>
    </header>
  );
}

// Tabs linking the three user-area pages together, shown above them.
function UserAreaTabs({ active, onNav }) {
  const { isAdmin } = useRoles();
  return (
    <div className="area-tabs">
      {userAreaFor(isAdmin).map(p =>
        <button key={p.id} className={'area-tab' + (active === p.id ? ' active' : '')}
                onClick={() => onNav(p.id)}>{p.label}</button>
      )}
    </div>
  );
}

// ── Shell ──────────────────────────────────────────────────────────────

function PageHead({ title, sub, back, backLabel, actions }) {
  return (
    <div className="page-head">
      <div>
        {back && (
          <button className="back-link" onClick={back}>
            <Icon name="back" size={14}/> <span>{backLabel || 'Back'}</span>
          </button>
        )}
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
      {filters.map(f=>{
        const value = typeof f === 'string' ? f : f.value;
        const label = typeof f === 'string' ? f : f.label;
        const mobile = typeof f === 'string' ? f : (f.mobile || f.label);
        return (
          <button key={value} className={`filter-chip ${activeFilter===value?'active':''}`} onClick={()=>onFilter(activeFilter===value?null:value)}>
            {activeFilter===value && <Icon name="check" size={11}/>}
            <span className="env-full">{label}</span>
            <span className="env-short">{mobile}</span>
          </button>
        );
      })}
      <div style={{flex:1}}/>{children}
    </div>
  );
}

// ── API helpers ────────────────────────────────────────────────────────

let _onUnauthorized = null;

// Headers carrying the admin session's credential. The session decides
// which header its kind implies — a bearer, or a key under its own name —
// so nothing here has to know.
const authHeaders = (session, init) => {
  const h = new Headers(init);
  session?.addAuth(h);
  return h;
};

async function frbr(session, method, path, body, { rawText = false } = {}) {
  const opts = { method, body };
  let r = null;
  try {
    r = await window.FrBr.api(session, path, opts);
  } catch (e) {
    _onUnauthorized?.();
    throw e;
  }

  if (!r.ok) {
    const t = await r.text().catch(()=>'');
    throw new Error(`${r.status}: ${t||r.statusText}`);
  }
  if (r.status===204) return null;
  return rawText ? r.text() : r.json();
}

// canEditServiceFile reports whether user is one of a service's members,
// the non-admins trusted with its definition file.
const canEditServiceFile = (service, user) => !!user && (service.members || []).includes(user.id);

// sameIDs reports whether two id lists hold the same ids, in any order.
const sameIDs = (a, b) => a.length === b.length && a.every(id => b.includes(id));

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

function hostRoute(app) {
  if (app.url && !app.url.includes('://')) return '/' + app.url.replace(/^\//, '');
  const slug = (app.name || '').toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '');
  return '/' + slug;
}

// An app is hosted when any deployment slot has content: the dev upload or a
// staging/production deploy. Slots live at <route>@dev|@staging|@prod; the
// bare route serves the app's default environment.
const isHosted = (a) => !!(a.details?.last_uploaded || a.details?.last_deployed_staging || a.details?.last_deployed_production);

function buildPrompt(app, appServices) {
  const fbURL = window.__HOMESLICE_CONFIG?.apiBase || window.location.origin;
  const serviceLines = appServices.length
    ? ("\nIntegrations: (be sure to use any URLs exactly)\n" + appServices.map(serviceInstructions).join('\n'))
    : '';
  return `Use the 'freshbreath' skill to add integrations to this app.\n\nSettings:\n  App nonce: ${app.nonce}\n  Fresh Breath URL: ${fbURL}\n${serviceLines}`;
}

// ── Sections ───────────────────────────────────────────────────────────

// What points at an auth record, so deleting one is an informed choice
// rather than a 409 from the server. Shared by the home table and the
// auth record page.
const authUsedBy = (id, services, apps) => [
  ...services.filter(s => s.protected_by === id).map(s => `${s.name} (protects)`),
  ...services.filter(s => s.acts_as === id).map(s => `${s.name} (acts as)`),
  ...apps.filter(a => a.protected_by === id).map(a => `${a.name} (protects)`),
];

// A copyable identifier shown under an entity's name in the table — the
// app nonce or the service URL. Auth records aren't referred to
// externally, so they get no subtitle.
const IDSub = ({ value, toast }) => {
  if (!value) return null;
  return (
    <button className="id-sub" title="Copy to clipboard"
            onClick={ev => { ev.stopPropagation(); copyText(value, toast); }}>
      <span className="mono">{value}</span>
      <Icon name="copy" size={11}/>
    </button>
  );
};

// The drop box on the home page: one place to publish anything — a new
// app or an app update (.html/.zip) or a tasks/virtual definition (.txt).
// It only picks the file up; the modal does the asking.
// updateOnly is for users who can't create apps or services: the drop can
// only replace something they already have.
function DropZone({ onFile, updateOnly }) {
  const [dragging, setDragging] = useState(false);
  const inputRef = useRef(null);
  return (
    <div
      className={'drop-zone home-dropzone' + (dragging ? ' drop-zone-active' : '')}
      onDragOver={e => { e.preventDefault(); setDragging(true); }}
      onDragLeave={() => setDragging(false)}
      onDrop={e => {
        e.preventDefault();
        setDragging(false);
        const f = e.dataTransfer.files[0];
        if (f) onFile(f);
      }}
      onClick={() => inputRef.current?.click()}
    >
      <span className="dz-icon"><Icon name="upload" size={22}/></span>
      <b>{updateOnly ? 'Drop an update' : 'Drop a new app or update'}</b>
      <span>.html or .zip for apps · .txt for tasks & virtual services · or click to browse</span>
      <input ref={inputRef} type="file" accept=".html,.zip,.txt" style={{display:'none'}}
        onChange={e => { const f = e.target.files[0]; if (f) onFile(f); e.target.value = ''; }}/>
    </div>
  );
}

// The table's left-hand rail: one button per view — everything recently
// edited together, or the alphabetized list of a single type.
const ENTITY_VIEWS = [
  { id: 'recent',   label: 'Recently edited', singular: null,           icon: 'clock' },
  { id: 'app',      label: 'Apps',            singular: 'app',          icon: 'apps'  },
  { id: 'service',  label: 'Services',        singular: 'service',     icon: 'plug'  },
  { id: 'auth',     label: 'Auth',            singular: 'auth record', icon: 'key'   },
];

function HomePage({ session, navigate, apps, services, auth, users, adminAuthID, onRefresh }) {
  const { user } = useAuth();
  const { isAdmin } = useRoles();
  const [view, setView] = useState('recent');
  const [q, setQ] = useState('');
  const [dropFile, setDropFile] = useState(null);
  const toast = useToast();

  const hosted = apps.filter(isHosted);

  const appRows    = apps.map(a => ({ kind: 'app', id: a.nonce, name: a.name, when: a.updated_at, entity: a }));
  const serviceRows = services.map(s => ({ kind: 'service', id: s.id, name: s.name, when: s.updated_at, entity: s }));
  const authRows   = auth.map(r => ({ kind: 'auth', id: r.id, name: r.name, when: r.updated_at, entity: r }));

  const recent = [...appRows, ...serviceRows, ...authRows]
    .filter(e => e.when)
    .sort((a, b) => String(b.when).localeCompare(String(a.when)))
    .slice(0, 15);

  const matches = (name) => !q || name.toLowerCase().includes(q.toLowerCase());
  const rows =
    view === 'recent'   ? recent.filter(e => matches(e.name)) :
    view === 'app'      ? appRows.filter(e => matches(e.name)).sort((a, b) => a.name.localeCompare(b.name)) :
    view === 'service' ? serviceRows.filter(e => matches(e.name)).sort((a, b) => a.name.localeCompare(b.name)) :
                          authRows.filter(e => matches(e.name)).sort((a, b) => a.name.localeCompare(b.name));

  const open = (row) => navigate(
    row.kind === 'app' ? 'app' : row.kind === 'service' ? 'service' : 'authrecord',
    row.kind === 'app' ? { nonce: row.id }
      : row.kind === 'service' ? { serviceId: String(row.id) }
      : { authId: String(row.id) });

  const remove = async (row) => {
    const e = row.entity;
    if (row.kind === 'app') {
      if (!confirm('Delete this app?')) return;
      try { await frbr(session, 'DELETE', '/api/apps/' + row.id); toast('App deleted'); onRefresh(); }
      catch (err) { toast(err.message, true); }
    } else if (row.kind === 'service') {
      let usedBy = [];
      try { const r = await frbr(session, 'GET', '/api/services/' + row.id + '/apps'); usedBy = r.apps || []; }
      catch { /* ignore */ }
      let msg = 'Delete this service?';
      if (usedBy.length > 0) msg += `\n\nIt's used by ${usedBy.length} app${usedBy.length > 1 ? 's' : ''}:\n${usedBy.map(a => a.name).join(', ')}`;
      if (!confirm(msg)) return;
      try { await frbr(session, 'DELETE', '/api/services/' + row.id); toast('Service deleted'); onRefresh(); }
      catch (err) { toast(err.message, true); }
    } else {
      const uses = authUsedBy(e.id, services, apps);
      if (uses.length) { toast(`In use by ${uses.join(', ')} — unassign it first`, true); return; }
      if (!confirm(`Delete "${e.name}"? Anyone holding a credential from it will have to log in again.`)) return;
      try { await frbr(session, 'DELETE', '/api/auth/' + e.id); toast('Auth record deleted'); onRefresh(); }
      catch (err) { toast(err.message, true); }
    }
  };

  const activeView = ENTITY_VIEWS.find(v => v.id === view);
  const railIcn = { app: 'apps', service: 'plug', auth: 'key' };
  const railTone = { app: 'green', service: 'blue', auth: 'violet' };

  const kindBadge = (row) =>
    row.kind === 'app' ? <Badge tone={envTone(row.entity.environment)}>{envShort(row.entity.environment)}</Badge>
    : row.kind === 'service' ? <Badge dot={false} tone="gray">{row.entity.descriptor?.type?.toLocaleUpperCase() || '—'}</Badge>
    : <Badge dot={false} tone={authKindTone(row.entity.kind)}>{authKindLabel(row.entity.kind)}</Badge>;

  const detailCell = (row) => {
    const e = row.entity;
    if (row.kind !== 'auth') return <GateCell slot={e.protected_by} auth={auth} adminAuthID={adminAuthID}/>;
    return e.descriptor?.provider
      ? <span className="mono" style={{fontSize:12.5}}>{e.descriptor.provider}</span>
      : <span className="muted">—</span>;
  };

  return (
    <>
      <p>Connect to the MCP at <strong>{window.__HOMESLICE_CONFIG.apiBase}/mcp</strong>. <a href="https://freshbreath.dev/" target="_blank">Read more.</a></p>
      <div className="home-section">
        <div className="home-hosted">
          {hosted.length === 0 ? (
            <div className="empty hosted-empty">
              <b>No hosted apps yet.</b><br/>
              Drop an .html or .zip file to publish your first one.
            </div>
          ) : (
            <div className="hosted-grid">
              {hosted.map(a => (
                <div key={a.nonce} className="hosted-card" onClick={() => window.open(hostRoute(a), '_blank', 'noopener')} title="Open app">
                  <Favicon route={hostRoute(a)} name={a.name}/>
                  <a className="hosted-name" href={hostRoute(a)} target="_blank" rel="noopener noreferrer"
                     onClick={ev => ev.stopPropagation()}>{a.name}</a>
                  <Badge tone={envTone(a.environment)}>
                    <span className="env-full">{a.environment || '—'}</span>
                    <span className="env-short">{envShort(a.environment)}</span>
                  </Badge>
                  <button className="btn btn-icon btn-ghost hosted-edit" onClick={ev => { ev.stopPropagation(); navigate('app', { nonce: a.nonce }); }} title="Edit app settings"><Icon name="edit" size={14}/></button>
                </div>
              ))}
            </div>
          )}
          {(isAdmin || apps.length > 0 || services.some(s => canEditServiceFile(s, user))) && (
            <div className="home-dropzone-wrap">
              <DropZone onFile={setDropFile} updateOnly={!isAdmin}/>
            </div>
          )}
        </div>
      </div>

      <div className="home-section">
        <div className="home-section-head">
          <h2>{activeView.label}</h2>
          {isAdmin && <NewEntityMenu navigate={navigate}/>}
        </div>
        <div className="entity-wrap">
          <div className="entity-rail">
            {ENTITY_VIEWS.map(v => (
              <button key={v.id} className={'rail-btn tone-' + (railTone[v.id] || 'white') + (view === v.id ? ' active' : '')}
                      title={v.id === 'recent' ? 'Recently edited — apps, services and auth together' : `${v.label}, alphabetical`}
                      aria-label={v.label}
                      onClick={() => { setView(v.id); setQ(''); }}>
                <Icon name={v.icon} size={17}/>
              </button>
            ))}
          </div>
          <div className="table-wrap entity-table">
            <div className="entity-toolbar">
              <div className="search">
                <span className="icn"><Icon name="search" size={14}/></span>
                <input value={q} onChange={e => setQ(e.target.value)} placeholder={`Search ${activeView.label.toLowerCase()}…`}/>
              </div>
            </div>
            <table className="tbl" data-mobile>
              <thead><tr>
                <th style={{width:'30%'}}>Name</th>
                {view !== 'recent' && <th>{view === 'app' ? 'Environment' : view === 'auth' ? 'Kind' : 'Type'}</th>}
                <th>Access</th>
                <th>Edited</th>
                <th style={{width:80}}></th>
              </tr></thead>
              <tbody>
                {rows.map(row => {
                  const e = row.entity;
                  const deletable = isAdmin && (
                    (row.kind === 'auth' && !e.builtin) ||
                    (row.kind === 'service' && e.descriptor?.type !== 'ssh') ||
                    row.kind === 'app');
                  return (
                    <tr key={row.kind + ':' + row.id} className="entity-row" onClick={() => open(row)}>
                      <td data-col="identity">
                        <div className="user-cell">
                          <span className={'entity-icn tone-' + railTone[row.kind]}>
                            <Icon name={railIcn[row.kind]} size={14}/>
                          </span>
                          <div className="meta">
                            <b>{row.name}</b>
                            {row.kind === 'app' && <IDSub value={e.nonce} toast={toast}/>}
                            {row.kind === 'service' && <IDSub value={e.url} toast={toast}/>}
                            {row.kind === 'auth' && e.builtin && <Badge tone="purple" dot={false}>Built-in</Badge>}
                            {row.kind === 'service' && unproxied(e) && (
                              <span className="unproxied-mark" title="Unproxied services are allowed to pass their creds to the user.">(!)</span>
                            )}
                          </div>
                        </div>
                      </td>
                      {view !== 'recent' && (
                        <td data-col="badge">{kindBadge(row)}</td>
                      )}
                      <td data-col="detail">{detailCell(row)}</td>
                      <td data-col="detail" className="muted">{fmtAuditTime(row.when)}</td>
                      <td data-col="actions" onClick={ev => ev.stopPropagation()}>
                        <div className="row-actions">
                          <button className="btn btn-icon btn-ghost" onClick={() => open(row)} title="Open"><Icon name="edit" size={14}/></button>
                          {deletable && (
                            <button className="btn btn-icon btn-ghost" onClick={() => remove(row)} title="Delete"><Icon name="trash" size={14}/></button>
                          )}
                        </div>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
            {rows.length === 0 && (
              <div className="empty">
                <b>{view === 'recent' ? 'Nothing edited yet.' : `No ${activeView.label.toLowerCase()} found.`}</b>
              </div>
            )}
          </div>
        </div>
      </div>

      {dropFile && (
        <UploadModal session={session} file={dropFile} apps={apps} services={services} auth={auth}
                     users={users} adminAuthID={adminAuthID} onClose={() => setDropFile(null)}
                     onSaved={onRefresh} navigate={navigate}/>
      )}
    </>
  );
}

// ── Users ──────────────────────────────────────────────────────────────

function UsersView({ session, users, apps, onRefresh }) {
  const { isSuperuser } = useRoles();
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
    try { await frbr(session, 'DELETE','/api/users/'+id); toast('User deleted'); onRefresh(); }
    catch(e) { toast(e.message,true); }
  };

  return (
    <>
      <PageHead
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
                    <Avatar name={u.name} email={u.email}/>
                    <div className="meta"><b>{u.name}</b><span>{u.email}</span></div>
                  </div>
                </td>
                <td data-col="detail"><Badge tone={roleTone(u.role)}>{u.role}</Badge></td>
                <td data-col="badge"><Badge tone={statusTone(u.status)}>{u.status}</Badge></td>
                <td data-col="detail"><UserAppTags apps={u.apps} appList={apps}/></td>
                <td data-col="detail" className="muted">{fmtAuditTime(u.last_seen)}</td>
                <td data-col="actions">
                  {/* Only a Superuser may change a Superuser's account. */}
                  {(isSuperuser || u.role !== 'Superuser') && (
                    <div className="row-actions">
                      <button className="btn btn-icon btn-ghost" onClick={()=>setEditing(u)} title="Edit"><Icon name="edit" size={14}/></button>
                      <button className="btn btn-icon btn-ghost" onClick={()=>remove(u.id)} title="Delete"><Icon name="trash" size={14}/></button>
                    </div>
                  )}
                </td>
              </tr>
            )}
          </tbody>
        </table>
        {filtered.length===0 && <div className="empty"><b>No users match.</b>Try a different search.</div>}
      </div>
      <UserDrawer user={editing} session={session} apps={apps} onClose={()=>setEditing(null)} onSaved={onRefresh}/>
    </>
  );
}

function UserDrawer({ user, session, apps, onClose, onSaved }) {
  const { isSuperuser } = useRoles();
  const [form,setForm] = useState({name:'',email:'',role:'Member',status:'Active',apps:[]});
  const [passphrase, setPassphrase] = useState('');
  const [passConfirm, setPassConfirm] = useState('');
  const [loading,setLoading] = useState(false);
  const [busy,setBusy] = useState(false);
  // The invite or reset link just minted, shown with an email to send.
  const [link, setLink] = useState(null);
  const toast = useToast();
  const isNew = user==='new';
  const isEdit = user && user.id;

  useEffect(()=>{
    if(isEdit) {
      setForm({name:user.name,email:user.email,role:user.role||'Member',status:user.status||'Active',apps:[]});
      setLoading(true);
      frbr(session, 'GET','/api/users/'+user.id+'/apps')
        .then(d=>{
          setForm(f=>({...f,apps:d.apps||[]}));
        })
        .catch(e=>toast(e.message,true))
        .finally(()=>setLoading(false));
    } else {
      setForm({name:'',email:'',role:'Member',status:'Active',apps:[]});
    }
    setPassphrase('');
    setPassConfirm('');
  },[user]); // eslint-disable-line react-hooks/exhaustive-deps

  // A passphrase is optional on create, but if one is typed it has to be
  // usable before anything is saved.
  const passphraseProblem =
    !passphrase && !passConfirm ? '' :
    passphrase.length < 8 ? 'At least 8 characters.' :
    passphrase !== passConfirm ? "The passphrases don't match." : '';

  const save = async () => {
    setBusy(true);
    try {
      if(isEdit) {
        await frbr(session, 'PUT','/api/users/'+user.id,form);
        await frbr(session, 'PUT','/api/users/'+user.id+'/apps',{apps:form.apps||[]});
        toast('User updated');
      } else {
        const created = await frbr(session, 'POST','/api/users',form);
        await frbr(session, 'PUT','/api/users/'+created.id+'/apps',{apps:form.apps||[]});
        if (passphrase) {
          try { await frbr(session, 'POST','/api/users/'+created.id+'/ssh-key',{passphrase}); }
          catch(e) { toast('User created, but the SSH key failed: '+e.message, true); onClose(); onSaved(); return; }
        }
        toast(passphrase ? 'User created with an SSH key' : 'User created');
      }
      onClose(); onSaved();
    } catch(e) { toast(e.message,true); }
    finally { setBusy(false); }
  };

  // Invite: create the user as Invited and mint them a link to choose
  // their own passphrase. The drawer gives way to the email to send.
  const invite = async () => {
    setBusy(true);
    try {
      const created = await frbr(session, 'POST','/api/users',{...form,status:'Invited'});
      await frbr(session, 'PUT','/api/users/'+created.id+'/apps',{apps:form.apps||[]});
      const minted = await frbr(session, 'POST','/api/users/'+created.id+'/passphrase-link');
      setLink({kind:'invite', user:created, ...minted});
    } catch(e) { toast(e.message,true); }
    finally { setBusy(false); }
  };

  const resetLink = async () => {
    try {
      const minted = await frbr(session, 'POST','/api/users/'+user.id+'/passphrase-link');
      setLink({kind:'reset', user, ...minted});
    } catch(e) { toast(e.message,true); }
  };

  // An invite's refresh waits for the modal: reloading the panel remounts
  // this drawer, which would take the link with it.
  const closeLink = () => {
    const wasInvite = link?.kind === 'invite';
    setLink(null);
    if (wasInvite) { onClose(); onSaved(); }
  };

  return (
    <>
    <Drawer
      open={(isNew || isEdit) && !(link && link.kind === 'invite')} title={isNew?'New user':'Edit user'}
      onClose={onClose}
      footer={<>
        <button className="btn btn-ghost" onClick={onClose}>Cancel</button>
        {isNew && (
          <button className="btn btn-ghost" onClick={invite} disabled={busy || !form.name || !form.email || !!passphrase}
                  title={passphrase ? 'Invited users choose their own passphrase — clear the one above to invite' : 'Create the user and get an email to send them'}>
            <Icon name="mail" size={14}/> Invite
          </button>
        )}
        <button className="btn btn-primary" onClick={save} disabled={busy || !!passphraseProblem}>{isNew?'Create':'Save'}</button>
      </>}
    >
      <p><strong>NOTE:</strong> You don't need to create accounts for people who are just using the apps and logging in with their own creds! This is only for users who need to log in to this admin panel and manage apps and services.</p>
      <div className="field"><label>Name</label><input className="input" value={form.name} onChange={e=>setForm(f=>({...f,name:e.target.value}))} placeholder="Ada Lovelace"/></div>
      <div className="field"><label>Email</label><input className="input" value={form.email} onChange={e=>setForm(f=>({...f,email:e.target.value}))} placeholder="ada@company.com"/></div>
      <div className="field-row">
        <div className="field"><label>Role</label>
          <select className="input" value={form.role} onChange={e=>setForm(f=>({...f,role:e.target.value}))}>
            {isSuperuser && <option>Superuser</option>}<option>Admin</option><option>Member</option><option>Read-only</option>
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
      {isNew && (
        <div style={{marginTop:20,borderTop:'1px solid var(--line-soft)',paddingTop:16}}>
          <label style={{fontSize:13,fontWeight:600,color:'var(--ink-2)',marginBottom:4,display:'block'}}>SSH key passphrase</label>
          <span className="help" style={{display:'block',marginBottom:12}}>
            Optional. Set one now and an SSH key is generated with the account — you'll have to pass the passphrase on yourself.
            Or leave it blank and <b>Invite</b> them to choose their own.
          </span>
          <div className="field-row">
            <div className="field"><label>Passphrase</label>
              <input className="input" type="password" autoComplete="new-password" value={passphrase} onChange={e=>setPassphrase(e.target.value)} placeholder="Min 8 characters"/>
            </div>
            <div className="field"><label>Confirm</label>
              <input className="input" type="password" autoComplete="new-password" value={passConfirm} onChange={e=>setPassConfirm(e.target.value)} placeholder="Re-enter passphrase"/>
            </div>
          </div>
          {passphraseProblem && <span className="help" style={{color:'var(--danger)'}}>{passphraseProblem}</span>}
        </div>
      )}
      {isEdit && (
        <div style={{marginTop:20,borderTop:'1px solid var(--line-soft)',paddingTop:16}}>
          <label style={{fontSize:13,fontWeight:600,color:'var(--ink-2)',marginBottom:12,display:'block'}}>SSH Key</label>
          <SSHKeySection session={session} keyPath={'/api/users/'+user.id+'/ssh-key'} whose={user.name + "'s"}/>
          <div style={{marginTop:16}}>
            <button className="btn btn-ghost" onClick={resetLink}><Icon name="mail" size={14}/> Create reset link</button>
            <span className="help" style={{display:'block',marginTop:6}}>
              A link {user.name} opens to choose a new passphrase. Using it replaces their SSH key, so the current public key stops working.
            </span>
          </div>
        </div>
      )}
    </Drawer>
    {link && <PassphraseLinkModal link={link} onClose={closeLink}/>}
    </>
  );
}

// The email to send with a passphrase link — an invite for a new user, a
// reset for an existing one. Editable before copying; it's only a draft.
function passphraseLinkEmail({ kind, user, url, expires_at }) {
  const cfg = window.__HOMESLICE_CONFIG || {};
  const base = cfg.apiBase || window.location.origin;
  const expires = new Date(expires_at).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' });
  const signIn = cfg.authKind === 'ssh_key'
    ? `Then sign in at ${base}/control with your email (${user.email}) and that passphrase.`
    : `The passphrase unlocks your SSH key. You sign in to ${base}/control with your usual account (${user.email}).`;
  if (kind === 'invite') return `Subject: You're invited to Fresh Breath

Hi ${user.name},

You have a ${user.role} account on Fresh Breath at ${base}.

To get started, choose your passphrase here:
${url}

The link works once and expires ${expires}. ${signIn}
`;
  return `Subject: Reset your Fresh Breath passphrase

Hi ${user.name},

Here's a link to choose a new passphrase for your Fresh Breath account:
${url}

Setting it replaces your SSH key, so anywhere the old public key was added will need the new one. The link works once and expires ${expires}. ${signIn}
`;
}

function PassphraseLinkModal({ link, onClose }) {
  const [email, setEmail] = useState(() => passphraseLinkEmail(link));
  const toast = useToast();
  return (
    <div className="modal-overlay" onClick={onClose}>
      <div className="modal" onClick={e => e.stopPropagation()} style={{maxWidth:560}}>
        <h3 style={{marginBottom:8}}>{link.kind === 'invite' ? `Invite ${link.user.name}` : `Reset link for ${link.user.name}`}</h3>
        <p className="muted" style={{fontSize:13,marginBottom:16}}>
          {link.kind === 'invite' ? 'The account is created as Invited and turns Active once they set a passphrase. ' : ''}
          Copy the email below and send it to <b>{link.user.email}</b>. Any earlier link for them no longer works.
        </p>
        <div className="field">
          <label>Link</label>
          <div style={{display:'flex',gap:8}}>
            <input className="input mono" value={link.url} readOnly style={{fontSize:12,flex:1,minWidth:0}}/>
            <button className="btn btn-ghost" onClick={() => copyText(link.url, toast)} title="Copy link"><Icon name="copy" size={14}/></button>
          </div>
        </div>
        <div className="field">
          <label>Email</label>
          <textarea className="input" rows={12} value={email} onChange={e => setEmail(e.target.value)} style={{fontSize:12.5,lineHeight:1.5,resize:'vertical'}}/>
        </div>
        <div style={{display:'flex',gap:8,justifyContent:'flex-end',marginTop:16}}>
          <button className="btn btn-ghost" onClick={onClose}>Done</button>
          <button className="btn btn-primary" onClick={() => copyText(email, toast)}><Icon name="copy" size={14}/> Copy email</button>
        </div>
      </div>
    </div>
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

// The app form's fields, shared by the app page and the upload modal's
// new-app flow, so the two can't drift apart. `app` is null for a new one;
// `onCreateGate` opens the inline auth-record modal from a gate slot.
function AppFormFields({ form, setForm, app, services, users, auth, adminAuthID, loading, onCreateGate }) {
  const toast = useToast();

  // A service reached through an app answers to the *app's* gate — its own
  // protected_by does not stack. So a service guarded more tightly than the
  // app it is being linked into is about to become reachable by a wider
  // audience than whoever set it up chose. Say so at the moment of the link;
  // don't refuse it, because sometimes that is exactly the intent.
  const appGate = effectiveGate(form.protected_by, auth, adminAuthID);
  const exposed = form.services
    .map(id => {
      const service = services.find(s => s.id === id);
      if (!service) return null;
      const gate = effectiveGate(service.protected_by, auth, adminAuthID);
      return gate && gateStrictness(gate) > gateStrictness(appGate) ? { service, gate } : null;
    })
    .filter(Boolean);

  return (
    <>
      <div className="field-row">
        <div className="field"><label>Name</label><input className="input" value={form.name} onChange={e=>setForm(f=>({...f,name:e.target.value}))} placeholder="My App"/></div>
        <div className="field"><label>URL</label>
          <input className="input" value={form.url} onChange={e=>setForm(f=>({...f,url:e.target.value}))} placeholder="https://hostname.com:port"/>
        </div>
      </div>
      {app && app.nonce && (
        <div className="field">
          <label>Nonce</label>
          <div className="input" style={{display:'flex',alignItems:'center',justifyContent:'space-between',gap:12}}>
            <span className="mono">{app.nonce}</span>
            <button className="btn btn-ghost" style={{padding:'2px 8px',fontSize:12}} onClick={()=>copyText(app.nonce, toast)}>
              <Icon name="copy" size={14}/> Copy
            </button>
          </div>
        </div>
      )}
      <div className="field-row">
        <div className="field"><label>Default environment</label>
          <select className="input" value={form.environment} onChange={e=>setForm(f=>({...f,environment:e.target.value}))}>
            <option>Production</option><option>Staging</option><option>Development</option>
          </select>
          <span className="help">Which deployment slot the bare app URL serves.</span>
        </div>
        <div className="field"><label>Owner</label>
          <select className="input" value={form.owner_id} onChange={e=>setForm(f=>({...f,owner_id:e.target.value}))}>
            <option value="">No owner</option>
            {users.map(u=><option key={u.id} value={u.id}>{u.name}</option>)}
          </select>
        </div>
      </div>
      <div className="field">
        <label>Members</label>
        <span className="help">Select which users are assigned to this app.</span>
        {app && loading ? <span className="muted">Loading…</span> : (
          <MultiSelect
            options={users.map(u=>({value:u.id,label:u.name}))}
            value={form.members}
            onChange={(v)=>setForm(f=>({...f,members:v}))}
            placeholder="No members"
          />
        )}
      </div>
      <AuthSlot
        label="Protected by"
        placeholder="— inherit (admin auth) —"
        help={
          appGate
            ? (appGate.kind === 'anonymous'
                ? 'Open to anyone who can reach this app.'
                : `Visitors clear ${appGate.name}${form.protected_by == null ? ' (inherited from admin auth)' : ''} before the page loads — and everything it calls answers to this gate.`)
            : 'No admin auth is configured, so this app is open to anyone who can reach it.'
        }
        records={auth}
        value={form.protected_by}
        onChange={v=>setForm(f=>({...f,protected_by:v}))}
        onCreate={onCreateGate}
      />
      <div className="field">
        <label>Service access</label>
        <span className="help">Select which services this app can access. Linking a service with SQL tools also grants this app's pages its database — the link is the grant.</span>
        {app && loading ? <span className="muted">Loading…</span> : (
          <MultiSelect
            options={services.map(s=>({value:s.id,label:s.name}))}
            value={form.services}
            onChange={(v)=>setForm(f=>({...f,services:v}))}
            placeholder="No service access"
          />
        )}
        {exposed.length > 0 && (
          <div className="help" style={{color:'var(--amber, #b7791f)',marginTop:8,lineHeight:1.5}}>
            <Icon name="bell" size={12}/>{' '}
            <b>Wider than {exposed.length === 1 ? 'it asks for' : 'they ask for'}.</b>{' '}
            This app's gate governs everything reached through it, so linking{' '}
            {exposed.map(e => `${e.service.name} (${e.gate.name})`).join(', ')}{' '}
            {exposed.length === 1 ? 'opens it' : 'opens them'} to everyone who clears{' '}
            {appGate ? appGate.name : 'no gate at all'}. Link anyway if that's the intent.
          </div>
        )}
      </div>
    </>
  );
}

// The app settings page — what the edit drawer used to be, full-page now.
function AppPage({ session, nonce, isNew, apps, services, users, auth, adminAuthID, onRefresh, navigate }) {
  const app = isNew ? null : apps.find(a => a.nonce === nonce);
  const [form, setForm] = useState({name:'',environment:'Development',url:'',owner_id:'',services:[],members:[],protected_by:null});
  const [creatingGate, setCreatingGate] = useState(false);
  const [loading, setLoading] = useState(false);
  const toast = useToast();

  // Keyed on the nonce (not the app object) so an onRefresh after saving
  // doesn't stamp the form back to server state mid-edit.
  useEffect(() => {
    if (app) {
      setForm({name:app.name||'',environment:app.environment||'Development',url:app.url,owner_id:app.owner_id?String(app.owner_id):'',services:[],members:[],protected_by:app.protected_by ?? null});
      setLoading(true);
      Promise.all([
        frbr(session, 'GET','/api/apps/'+app.nonce+'/services'),
        frbr(session, 'GET','/api/apps/'+app.nonce+'/members'),
      ])
        .then(([svcs,mems])=>{
          const allowed = (svcs.services||[]).filter(l=>l.allowed).map(l=>l.service_id);
          setForm(f=>({...f,services:allowed,members:mems.members||[]}));
        })
        .catch(e=>toast(e.message,true))
        .finally(()=>setLoading(false));
    } else {
      // A new app defaults to Anonymous: an app is a door onto the LAN,
      // and inheriting admin auth would gate every dashboard by surprise.
      setForm({name:'',environment:'Development',url:'',owner_id:'',services:[],members:[],
               protected_by: auth.find(r => r.kind === 'anonymous' && r.builtin)?.id ?? null});
    }
  }, [app ? app.nonce : null, isNew]); // eslint-disable-line react-hooks/exhaustive-deps

  const save = async () => {
    const payload = {name:form.name,environment:form.environment,url:form.url,owner_id:form.owner_id?Number(form.owner_id):null,protected_by:form.protected_by};
    try {
      let savedNonce;
      if (app) {
        await frbr(session, 'PUT','/api/apps/'+app.nonce,payload);
        await frbr(session, 'PUT','/api/apps/'+app.nonce+'/members',{members:form.members||[]});
        await frbr(session, 'PUT','/api/apps/'+app.nonce+'/services',{services:form.services||[]});
        toast('App updated');
      } else {
        const resp = await frbr(session, 'POST','/api/apps',payload);
        savedNonce = resp.nonce;
        await frbr(session, 'PUT','/api/apps/'+savedNonce+'/members',{members:form.members||[]});
        await frbr(session, 'PUT','/api/apps/'+savedNonce+'/services',{services:form.services||[]});
        toast('App created');
      }
      onRefresh();
      if (savedNonce) navigate('app', { nonce: savedNonce });
    } catch(e) { toast(e.message,true); }
  };

  const remove = async () => {
    if (!app) return;
    if (!confirm('Delete this app?')) return;
    try { await frbr(session, 'DELETE','/api/apps/'+app.nonce); toast('App deleted'); navigate('home'); onRefresh(); }
    catch(e) { toast(e.message,true); }
  };

  if (!isNew && !app) {
    return (
      <>
        <PageHead title="App not found" back={()=>navigate('home')} backLabel="Home"/>
        <div className="empty"><b>App not found.</b> It may have been deleted.</div>
      </>
    );
  }

  const setupPrompt = app && !loading
    ? buildPrompt(app, services.filter(s => form.services.includes(s.id)))
    : null;

  return (
    <>
      <PageHead
        title={app ? app.name : 'New app'}
        sub={app ? 'Settings, hosting slots and access.' : 'Name it and set who it answers to.'}
        back={()=>navigate('home')} backLabel="Home"
        actions={<>
          {app && <button className="btn btn-ghost" style={{color:'var(--danger)'}} onClick={remove}><Icon name="trash" size={14}/> Delete</button>}
          <button className="btn btn-primary" onClick={save} disabled={!form.name}>{app ? 'Save' : 'Create app'}</button>
        </>}
      />
      <div className={'page-form' + (app && !loading ? ' page-cols' : '')}>
        <div className="page-col">
          <AppFormFields form={form} setForm={setForm} app={app} services={services} users={users}
                         auth={auth} adminAuthID={adminAuthID} loading={loading}
                         onCreateGate={()=>setCreatingGate(true)}/>
        </div>
        {app && !loading && (
          <div className="page-col">
            <HostUpload session={session} app={app} onRefresh={onRefresh}/>
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
          </div>
        )}
      </div>
      {creatingGate && (
        <AuthRecordModal
          session={session}
          onClose={()=>setCreatingGate(false)}
          onSaved={(rec)=>{ setForm(f=>({...f,protected_by:rec.id})); setCreatingGate(false); onRefresh(); }}
        />
      )}
    </>
  );
}

const SLOT_NAMES = { dev:'Development', staging:'Staging', prod:'Production' };

// UploadModal asks what a dropped file should become. An .html/.zip lands
// in an app slot — a brand-new app with the full settings form, or a
// replacement upload for an existing one (defaults to Development; another
// slot deploys there right after the upload). A .txt publishes a tasks or
// virtual service definition — the type is asked here, not guessed,
// because both formats are just bracketed headers over plain text.
function UploadModal({ session, file, apps, services, users, auth, adminAuthID, onClose, onSaved, navigate }) {
  const { user } = useAuth();
  const { isAdmin } = useRoles();
  const ext = (file.name.split('.').pop() || '').toLowerCase();
  const isAppFile = ext === 'html' || ext === 'zip';
  const seeded = file.name.replace(/\.[^.]+$/, '');
  // Only admins create; everyone else replaces what they already have —
  // their own apps (the server lists only those) or services they're members of.
  const [mode, setMode] = useState(isAdmin ? 'new' : 'replace');
  const [slot, setSlot] = useState('dev');
  const [appNonce, setAppNonce] = useState('');
  const [svcId, setSvcId] = useState('');
  const [appForm, setAppForm] = useState(null);
  const [svcForm, setSvcForm] = useState(null);
  const [creatingGate, setCreatingGate] = useState(false);
  const [busy, setBusy] = useState(false);
  const toast = useToast();

  useEffect(() => {
    if (isAppFile) {
      setAppForm({name:seeded,environment:'Development',url:'',owner_id:'',services:[],members:[],
        protected_by: auth.find(r => r.kind === 'anonymous' && r.builtin)?.id ?? null});
    } else {
      setSvcForm({name:seeded,url:'',descriptor:{type:'tasks'},protected_by:null,acts_as:null});
    }
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  const txtServices = services.filter(s =>
    (s.descriptor?.type === 'tasks' || s.descriptor?.type === 'virtual') && (isAdmin || canEditServiceFile(s, user)));

  const submit = async () => {
    setBusy(true);
    try {
      if (isAppFile) {
        let nonce;
        if (mode === 'new') {
          const resp = await frbr(session, 'POST', '/api/apps', {
            name: appForm.name, environment: appForm.environment, url: appForm.url,
            owner_id: appForm.owner_id ? Number(appForm.owner_id) : null,
            protected_by: appForm.protected_by,
          });
          nonce = resp.nonce;
          await frbr(session, 'PUT', '/api/apps/' + nonce + '/members', {members: appForm.members || []});
          await frbr(session, 'PUT', '/api/apps/' + nonce + '/services', {services: appForm.services || []});
        } else {
          nonce = appNonce;
        }
        const fd = new FormData();
        fd.append('file', file);
        const res = await fetch('/api/apps/' + nonce + '/web', {method:'POST', headers: authHeaders(session), body: fd});
        if (!res.ok) throw new Error(await res.text());
        if (slot !== 'dev') await frbr(session, 'POST', '/api/apps/' + nonce + '/deploy', {target: slot});
        toast(mode === 'new' ? 'App created and uploaded' : 'Uploaded to ' + SLOT_NAMES[slot]);
        onClose(); onSaved?.();
        navigate('app', { nonce });
      } else {
        let id;
        if (mode === 'new') {
          const type = svcForm.descriptor.type;
          const payload = {...svcForm};
          if (type === 'virtual') payload.url = '';
          else { delete payload.descriptor.database_target; delete payload.descriptor.database_name; }
          const resp = await frbr(session, 'POST', '/api/services', payload);
          id = resp.id;
        } else {
          id = Number(svcId);
        }
        const fd = new FormData();
        fd.append('file', file, file.name);
        await frbr(session, 'POST', '/api/services/' + id + '/files', fd, {rawText: true});
        toast(mode === 'new' ? 'Service created and published' : 'Definition published');
        onClose(); onSaved?.();
        navigate('service', { serviceId: String(id) });
      }
    } catch (e) { toast(e.message, true); }
    finally { setBusy(false); }
  };

  const ready = isAppFile
    ? (mode === 'new' ? !!appForm?.name : !!appNonce)
    : (mode === 'new' ? !!svcForm?.name : !!svcId);

  const selApp = apps.find(a => a.nonce === appNonce);
  const selSvc = txtServices.find(s => String(s.id) === svcId);
  const confirmLabel = mode === 'new'
    ? (isAppFile ? 'Create app' : 'Create service')
    : (isAppFile
        ? (selApp ? `Replace ${selApp.name}` : 'Replace')
        : (selSvc ? `Replace ${selSvc.name}` : 'Replace'));

  return (
    <div className="modal-overlay" onClick={busy ? undefined : onClose}>
      <div className="modal upload-modal" onClick={e => e.stopPropagation()}>
        <div className="upload-head">
          <div className="upload-file-icon"><Icon name={isAppFile ? 'apps' : 'log'} size={19}/></div>
          <div className="upload-file-id">
            <div className="upload-file-name mono">{file.name}</div>
            <div className="upload-file-meta">{isAppFile ? 'App bundle' : 'Service definition'} · {(file.size/1024).toFixed(1)} KB</div>
          </div>
          <button className="upload-close" onClick={busy ? undefined : onClose} title="Close" disabled={busy}>
            <Icon name="close" size={16}/>
          </button>
        </div>
        <div className="upload-mode">
          {isAdmin && (
            <button className={mode === 'new' ? 'active' : ''} onClick={()=>setMode('new')}>
              {isAppFile ? 'New app' : 'New service'}
            </button>
          )}
          <button className={mode === 'replace' ? 'active' : ''} onClick={()=>setMode('replace')}>
            Replace existing
          </button>
        </div>
        {isAppFile ? (
          mode === 'new' ? (
            appForm && (
              <div className="upload-body">
                <AppFormFields form={appForm} setForm={setAppForm} app={null} services={services} users={users}
                               auth={auth} adminAuthID={adminAuthID}
                               onCreateGate={()=>setCreatingGate(true)}/>
              </div>
            )
          ) : (
            <div className="upload-body">
              <ReplacePicker
                placeholder="Find an app"
                emptyLabel={isAdmin ? 'No apps match.' : 'No apps match — you can update the apps you belong to.'}
                items={apps.slice().sort((a,b)=>a.name.localeCompare(b.name)).map(a => ({
                  id: a.nonce, name: a.name, sub: a.url || a.nonce,
                  badge: envShort(a.environment), badgeTone: envTone(a.environment),
                  when: fmtAuditTime(a.updated_at),
                }))}
                value={appNonce}
                onChange={setAppNonce}
              />
              {selApp && (
                <div className="replace-note">
                  {selApp.name} keeps its name, URL, environment, members and service links — only the web content changes.
                </div>
              )}
              <SlotPick slot={slot} setSlot={setSlot}/>
            </div>
          )
        ) : (
          mode === 'new' ? (
            svcForm && (
              <div className="upload-body">
                <ServiceFormFields form={svcForm} setForm={setSvcForm} auth={auth} adminAuthID={adminAuthID}
                                   onCreateGate={()=>setCreatingGate(true)} types={['tasks','virtual']}/>
              </div>
            )
          ) : (
            <div className="upload-body">
              <ReplacePicker
                placeholder="Find a service"
                emptyLabel={isAdmin ? 'No services match.' : "No services match — you can update services you're a member of."}
                items={txtServices.slice().sort((a,b)=>a.name.localeCompare(b.name)).map(s => ({
                  id: String(s.id), name: s.name, sub: s.url || '—',
                  badge: s.descriptor?.type, badgeTone: 'violet',
                  when: fmtAuditTime(s.updated_at),
                }))}
                value={svcId}
                onChange={setSvcId}
              />
              {selSvc && (
                <div className="replace-note">
                  {selSvc.name} keeps its settings, gates and links — only the definition text changes.
                </div>
              )}
            </div>
          )
        )}
        <div className="upload-foot">
          <button className="btn btn-ghost" onClick={onClose} disabled={busy}>Cancel</button>
          <button className="btn btn-primary" onClick={submit} disabled={busy || !ready}>
            {busy ? 'Publishing…' : confirmLabel}
          </button>
        </div>
      </div>
      {creatingGate && (
        <AuthRecordModal
          session={session}
          onClose={()=>setCreatingGate(false)}
          onSaved={(rec)=>{
            if (isAppFile) setAppForm(f => ({...f, protected_by: rec.id}));
            else setSvcForm(f => ({...f, protected_by: rec.id}));
            setCreatingGate(false);
            onSaved?.();
          }}
        />
      )}
    </div>
  );
}

// The slot picker for dropped app files: uploads always write the
// Development folder; picking another slot deploys there right after.
function SlotPick({ slot, setSlot }) {
  return (
    <div className="field"><label>Slot</label>
      <select className="input" value={slot} onChange={e=>setSlot(e.target.value)}>
        <option value="dev">Development</option>
        <option value="staging">Staging</option>
        <option value="prod">Production</option>
      </select>
      <span className="help">Development is the default. Staging and Production are deployed from the fresh upload.</span>
    </div>
  );
}

// The searchable list that stands in for a <select> when a dropped file
// replaces an existing app or service (layout borrowed from the
// frbr-concept mockup). A row can say what an <option> can't: identity,
// environment or type, and when the entity last changed — with a radio dot
// so exactly one row is picked, never several. The note under the list is
// the caller's; it spells out what a replacement does and does not touch.
function ReplacePicker({ placeholder, emptyLabel, items, value, onChange }) {
  const [q, setQ] = useState('');
  const query = q.trim().toLowerCase();
  const filtered = items.filter(it =>
    !query || (it.name + ' ' + (it.sub || '')).toLowerCase().includes(query));
  return (
    <div>
      <div className="replace-search">
        <Icon name="search" size={14}/>
        <input value={q} onChange={e=>setQ(e.target.value)} placeholder={placeholder} spellCheck={false}/>
      </div>
      <div className="replace-list">
        {filtered.map(it => (
          <button key={String(it.id)} type="button"
                  className={'replace-row' + (String(it.id) === String(value) ? ' active' : '')}
                  onClick={()=>onChange(it.id)}>
            <span className="radio-dot"/>
            <span className="replace-id">
              <span className="replace-name">{it.name}</span>
              <span className="replace-sub mono">{it.sub}</span>
            </span>
            {it.badge && <Badge tone={it.badgeTone} dot={false}>{it.badge}</Badge>}
            <span className="replace-when">{it.when}</span>
          </button>
        ))}
        {filtered.length === 0 && <div className="replace-empty muted">{emptyLabel}</div>}
      </div>
    </div>
  );
}

// The app page's hosting panel. The drop zone wears the home page's clothes
// (the same classes) but uploads straight into this app's Development
// slot — no modal to ask what the file should become, since the page
// already knows. Below it the three deployment slots, with hosting's
// Remove link living on the Development line.
function HostUpload({ session, app, onRefresh }) {
  const [hosted, setHosted] = useState(!!(app.details?.last_uploaded));
  const [uploadedAt, setUploadedAt] = useState(app.details?.last_uploaded || null);
  const [deployed, setDeployed] = useState({
    staging: app.details?.last_deployed_staging || null,
    prod: app.details?.last_deployed_production || null,
  });
  const [deploying, setDeploying] = useState(null);
  const [dragging, setDragging] = useState(false);
  const [uploading, setUploading] = useState(false);
  const inputRef = useRef(null);
  const toast = useToast();

  const route = hostRoute(app);

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
        headers: authHeaders(session),
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
        headers: authHeaders(session),
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

  // Deploys copy the current Development (web) folder into the target slot.
  const deploy = async (target) => {
    setDeploying(target);
    try {
      const res = await frbr(session, 'POST', '/api/apps/' + app.nonce + '/deploy', { target });
      setDeployed(d => ({...d, [target]: new Date().toISOString()}));
      toast('Deployed to ' + (res.route || route + '@' + target));
      onRefresh();
    } catch(e) {
      toast(e.message, true);
    } finally {
      setDeploying(null);
    }
  };

  const slots = [
    { name:'Development', suffix:'@dev', when: uploadedAt, verb:'uploaded', empty:'not uploaded' },
    { name:'Staging', suffix:'@staging', when: deployed.staging, verb:'deployed', empty:'not deployed', target:'staging' },
    { name:'Production', suffix:'@prod', when: deployed.prod, verb:'deployed', empty:'not deployed', target:'prod' },
  ];

  return (
    <>
      <div
        className={'drop-zone home-dropzone' + (dragging ? ' drop-zone-active' : '')}
        onDragOver={e=>{e.preventDefault();setDragging(true);}}
        onDragLeave={()=>setDragging(false)}
        onDrop={onDrop}
        onClick={()=>inputRef.current?.click()}
      >
        <span className="dz-icon"><Icon name="upload" size={22}/></span>
        <b>{uploading ? 'Uploading…' : (hosted ? 'Drop to replace' : 'Drop an app bundle')}</b>
        <span>.html or .zip · or click to browse</span>
        <input ref={inputRef} type="file" accept=".html,.zip" style={{display:'none'}}
          onChange={e=>upload(e.target.files[0])}/>
      </div>

      <div className="field">
        <label>Deployment slots</label>
        <span className="help">
          Deploying copies the Development folder into a slot. The bare {route} URL serves the app's default environment; each slot also has its own URL.
        </span>
        <div className="slot-card">
          {slots.map(s=>(
            <div key={s.suffix} className="slot-row">
              <div className="slot-top">
                <Badge tone={envTone(s.name)}><span className="env-full">{s.name}</span><span className="env-short">{envShort(s.name)}</span></Badge>
                <span className="slot-when" title={s.when ? s.verb + ' ' + fmtAuditTime(s.when) : undefined}>{s.when ? fmtShortTime(s.when) : s.empty}</span>
                {s.name === 'Development' && hosted && (
                  <button className="btn btn-ghost slot-btn" style={{color:'var(--tone-red)'}} onClick={remove}>Remove</button>
                )}
                {s.target && (
                  <button className="btn btn-ghost slot-btn"
                    disabled={!hosted || deploying===s.target}
                    title={hosted ? 'Copy Development into ' + s.name : 'Upload to Development first'}
                    onClick={()=>deploy(s.target)}>
                    {deploying===s.target ? 'Deploying…' : 'Deploy'}
                </button>
                )}
              </div>
              {s.when ? (
                <a className="slot-url" href={route + s.suffix} target="_blank" rel="noopener noreferrer">{route + s.suffix}</a>
              ) : (
                <span className="slot-url muted">{route + s.suffix}</span>
              )}
            </div>
          ))}
        </div>
      </div>
    </>
  );
}

// ── Auth records ──
//
// An auth record is a credential or a login method, standing on its own.
// Services and apps point at one from either of two slots: "protected by"
// (who may call in) and "acts as" (what goes upstream). Every kind is
// eligible in both — what a kind *means* is what changes with the slot.

const AUTH_KINDS = [
  { kind:'anonymous', label:'Anonymous', tone:'gray',
    blurb:'Open to anyone who can reach this instance. Inbound: no credential asked for. Outbound: the caller’s own credential is stripped.' },
  { kind:'ssh_key', label:'SSH Key', tone:'purple',
    blurb:'A registered user signing in with the passphrase on their SSH key.' },
  { kind:'oidc', label:'OIDC', tone:'blue',
    blurb:'An OpenID Connect provider, configured by discovery from its issuer.' },
  { kind:'oauth2', label:'OAuth2', tone:'blue',
    blurb:'An OAuth2 provider whose endpoints you name yourself — GitHub-shaped, no id_token.' },
  { kind:'api_key', label:'API Key', tone:'amber',
    blurb:'A stored key, sent under a header. Inbound: callers must present it. Outbound: it is injected on the way upstream.' },
];

const authKind = (k) => AUTH_KINDS.find(a => a.kind === k);
const authKindLabel = (k) => authKind(k)?.label || k || '—';
const authKindTone = (k) => authKind(k)?.tone || 'gray';
const authRecord = (records, id) => (id == null || id === '') ? null : records.find(r => String(r.id) === String(id)) || null;

// One builder for both slots' dropdowns. The two this replaces had already
// drifted apart, which is the usual fate of a list written twice.
const authOptions = (records) =>
  records.map(r => ({ value: String(r.id), label: `${r.name} · ${authKindLabel(r.kind)}` }));

// The gate actually in force: an empty slot inherits the admin record, and
// only an explicit Anonymous record means open. A null here is setup mode —
// no admin auth configured yet, which is also wide open.
const effectiveGate = (slotID, records, adminID) => authRecord(records, slotID ?? adminID);

// How narrow an audience a gate admits. The server ranks the kinds (it is
// the half that can be tested); a missing gate is the widest thing there is.
const gateStrictness = (rec) => rec ? (rec.strictness ?? 0) : 0;

// A gate as a table cell. An empty slot is not "no gate" — it inherits the
// admin record — so it says so, rather than showing a dash that reads like
// the door is open.
function GateCell({ slot, auth, adminAuthID }) {
  const rec = effectiveGate(slot, auth, adminAuthID);
  if (!rec) return <Badge tone="amber" dot={false}>Open (no admin auth)</Badge>;
  const inherited = slot == null;
  return (
    <Badge dot={false} tone={rec.kind === 'anonymous' ? 'amber' : authKindTone(rec.kind)}>
      {rec.name}{inherited ? ' (inherited)' : ''}
    </Badge>
  );
}

/**
 * One auth slot: a dropdown over every record plus an inline "+ New…", so
 * wiring up a service doesn't mean leaving the drawer to go make a record
 * and finding your way back.
 */
function AuthSlot({ label, help, placeholder, records, value, onChange, onCreate, disabled }) {
  return (
    <div className="field">
      <label>{label}</label>
      <div style={{display:'flex',gap:8,alignItems:'flex-start'}}>
        <select
          className="input"
          disabled={disabled}
          value={value == null ? '' : String(value)}
          onChange={e => onChange(e.target.value === '' ? null : Number(e.target.value))}
        >
          <option value="">{placeholder}</option>
          {authOptions(records).map(o => <option key={o.value} value={o.value}>{o.label}</option>)}
        </select>
        {onCreate && (
          <button className="btn btn-ghost" style={{whiteSpace:'nowrap'}} onClick={onCreate} disabled={disabled}>
            <Icon name="plus" size={13}/> New…
          </button>
        )}
      </div>
      {help && <span className="help">{help}</span>}
    </div>
  );
}

/**
 * The auth record form, shared by the auth record page and the inline
 * "New…" modal. The form is per-kind because the kinds genuinely have
 * nothing in common — an issuer URL means nothing to a stored key, and
 * asking for one anyway is how config screens get their reputation.
 */
function AuthForm({ form, setForm, record }) {
  const isEdit = !!record;
  const upd = (k,v) => setForm(f=>({...f, descriptor:{...f.descriptor, [k]:v}}));

  // Switching kind starts the descriptor over. Carrying stale fields across
  // is how a record ends up with an issuer *and* an authorize URL and no
  // way to tell which one is live.
  const setKind = (kind) => setForm(f=>({...f, kind, descriptor:{}}));

  const kind = form.kind;
  const d = form.descriptor;
  const secretPlaceholder = isEdit && record.has_secret ? 'Stored — leave blank to keep' : '';

  return (
    <>
      <div className="field">
        <label>Name</label>
        <input className="input" value={form.name}
               onChange={e=>setForm(f=>({...f,name:e.target.value}))} placeholder="Company SSO"/>
      </div>
      <div className="field">
        <label>Kind</label>
        <select className="input" value={kind} onChange={e=>setKind(e.target.value)}>
          {AUTH_KINDS.map(k => <option key={k.kind} value={k.kind}>{k.label}</option>)}
        </select>
        <span className="help">{authKind(kind)?.blurb}</span>
      </div>

      {(kind === 'oidc' || kind === 'oauth2') && (
        <>
          {kind === 'oidc' ? (
            <div className="field"><label>Issuer</label>
              <input className="input mono" value={d.issuer||''} onChange={e=>upd('issuer',e.target.value)} placeholder="https://accounts.example.com"/>
              <span className="help">Endpoints are discovered from here.</span>
            </div>
          ) : (
            <>
              <div className="field"><label>Authorize URL</label>
                <input className="input mono" value={d.authorize_url||''} onChange={e=>upd('authorize_url',e.target.value)} placeholder="https://github.com/login/oauth/authorize"/>
              </div>
              <div className="field"><label>Token URL</label>
                <input className="input mono" value={d.token_url||''} onChange={e=>upd('token_url',e.target.value)} placeholder="https://github.com/login/oauth/access_token"/>
              </div>
              <div className="field"><label>Userinfo URL</label>
                <input className="input mono" value={d.userinfo_url||''} onChange={e=>upd('userinfo_url',e.target.value)} placeholder="https://api.github.com/user"/>
              </div>
              <div className="field"><label>User emails URL</label>
                <input className="input mono" value={d.userinfo_emails_url||''} onChange={e=>upd('userinfo_emails_url',e.target.value)} placeholder="https://api.github.com/user/emails"/>
                <span className="help">Only when the provider keeps email off the main userinfo response.</span>
              </div>
            </>
          )}
          <div className="field-row">
            <div className="field"><label>Client ID</label>
              <input className="input mono" value={d.client_id||''} onChange={e=>upd('client_id',e.target.value)}/>
            </div>
            <div className="field"><label>Scopes</label>
              <input className="input" value={d.scopes||''} onChange={e=>upd('scopes',e.target.value)} placeholder={kind==='oidc'?'openid profile email':'repo read:user'}/>
            </div>
          </div>
          <div className="field"><label>Client Secret</label>
            <input className="input mono" type="password" value={d.client_secret||''}
                   onChange={e=>upd('client_secret',e.target.value)} placeholder={secretPlaceholder}/>
            <span className="help">Never leaves the server — token exchange and refresh run here.</span>
          </div>
          <div className="field"><label>Provider slug</label>
            <input className="input mono" value={d.provider||''} onChange={e=>upd('provider',e.target.value)} placeholder="github"/>
            <span className="help">
              The upstream this record talks to. Two records over the same provider share one
              identity for the people behind them, so give them the same slug.
            </span>
          </div>
        </>
      )}

      {kind === 'api_key' && (
        <>
          <div className="field"><label>Key</label>
            <input className="input mono" type="password" value={d.key||''}
                   onChange={e=>upd('key',e.target.value)} placeholder={secretPlaceholder}/>
          </div>
          <div className="field"><label>Header</label>
            <input className="input mono" value={d.header||''} onChange={e=>upd('header',e.target.value)} placeholder="X-API-Key (blank for Authorization: Bearer)"/>
          </div>
        </>
      )}

      {kind === 'ssh_key' && (
        <div className="field"><label>Stored private key</label>
          <textarea className="input mono" rows={4} value={d.key||''}
                    onChange={e=>upd('key',e.target.value)} placeholder={secretPlaceholder || '-----BEGIN OPENSSH PRIVATE KEY-----'}
                    style={{fontSize:11,resize:'vertical'}}/>
          <span className="help">
            Leave empty and the passphrase is checked against each user's own key — which is
            what the built-in record does, and what you almost certainly want.
          </span>
        </div>
      )}

      {kind === 'anonymous' && (
        <div className="field">
          <span className="help">Nothing to configure. That is the entire point of it.</span>
        </div>
      )}
    </>
  );
}

// The auth record settings page — what the edit drawer used to be.
function AuthPage({ session, authId, isNew, auth, services, apps, onRefresh, navigate }) {
  const { isAdmin } = useRoles();
  const record = isNew ? null : auth.find(r => String(r.id) === String(authId));
  const [form, setForm] = useState({name:'',kind:'oidc',descriptor:{}});
  const [saving, setSaving] = useState(false);
  const toast = useToast();

  useEffect(() => {
    if (record) setForm({name:record.name, kind:record.kind, descriptor:{...record.descriptor}});
    else setForm({name:'', kind:'oidc', descriptor:{}});
  }, [record ? record.id : null, isNew]); // eslint-disable-line react-hooks/exhaustive-deps

  const save = async () => {
    setSaving(true);
    try {
      if (record) {
        // PUT answers 204, so an edit reports the record it just sent.
        await frbr(session, 'PUT', '/api/auth/' + record.id, form);
        toast('Auth record updated');
        onRefresh();
      } else {
        const saved = await frbr(session, 'POST', '/api/auth', form);
        toast('Auth record created');
        onRefresh();
        navigate('authrecord', { authId: String(saved.id) });
      }
    } catch(e) { toast(e.message, true); }
    finally { setSaving(false); }
  };

  const remove = async () => {
    if (!record) return;
    const uses = authUsedBy(record.id, services, apps);
    if (uses.length) { toast(`In use by ${uses.join(', ')} — unassign it first`, true); return; }
    if (!confirm(`Delete "${record.name}"? Anyone holding a credential from it will have to log in again.`)) return;
    try { await frbr(session, 'DELETE', '/api/auth/' + record.id); toast('Auth record deleted'); navigate('home'); onRefresh(); }
    catch(e) { toast(e.message, true); }
  };

  if ((!isNew && !record) || (isNew && !isAdmin)) {
    return (
      <>
        <PageHead title="Auth record not found" back={()=>navigate('home')} backLabel="Home"/>
        <div className="empty"><b>Auth record not found.</b> It may have been deleted.</div>
      </>
    );
  }

  const uses = record ? authUsedBy(record.id, services, apps) : [];
  // The seeded anonymous and SSH-key records are the instance's own. Their
  // meaning is fixed by their kind, so there is nothing to edit or save.
  const isBuiltin = !!record?.builtin;

  return (
    <>
      <PageHead
        title={record ? record.name : 'New auth record'}
        sub={record ? 'A credential and login method, standing on its own.' : 'Name a credential and login method.'}
        back={()=>navigate('home')} backLabel="Home"
        actions={<>
          {isAdmin && record && !isBuiltin && (
            <button className="btn btn-ghost" style={{color:'var(--danger)'}} onClick={remove}><Icon name="trash" size={14}/> Delete</button>
          )}
          {isAdmin && !isBuiltin && (
            <button className="btn btn-primary" onClick={save} disabled={saving || !form.name}>
              {saving ? 'Saving…' : record ? 'Save' : 'Create record'}
            </button>
          )}
        </>}
      />
      <div className="page-form">
        {isBuiltin ? (
          <div className="field">
            <label>Built-in auth record</label>
            <div><Badge tone={authKindTone(record.kind)} dot={false}>{authKindLabel(record.kind)}</Badge></div>
            <span className="help">{authKind(record.kind)?.blurb}</span>
            <span className="help">
              Seeded at startup and maintained by the instance, it has no editable settings.
              Services and apps point at it the same as any other record.
            </span>
          </div>
        ) : (
          <fieldset className="read-only-fields" disabled={!isAdmin}>
            {!isAdmin && <span className="help" style={{display:'block',marginBottom:12}}>Read-only — only admins can change auth records.</span>}
            <AuthForm form={form} setForm={setForm} record={record}/>
          </fieldset>
        )}
        {record && (
          <div className="field"><label>Used by</label>
            <span className="help">
              {uses.length ? uses.join(', ') : 'Nothing points at this record yet.'}
            </span>
          </div>
        )}
      </div>
    </>
  );
}

// The inline "New…" record creator, opened from a gate slot or a settings
// dropdown. A modal rather than a page, so the half-filled form that asked
// for a record is still there when this one closes. `onSaved` receives the
// saved record, so the slot that asked can pick it up without a round trip.
function AuthRecordModal({ session, onClose, onSaved, defaultKind }) {
  const [form, setForm] = useState({name:'',kind:defaultKind||'oidc',descriptor:{}});
  const [saving, setSaving] = useState(false);
  const toast = useToast();

  const save = async () => {
    setSaving(true);
    try {
      const saved = await frbr(session, 'POST', '/api/auth', form);
      toast('Auth record created');
      onClose(); onSaved?.(saved);
    } catch(e) { toast(e.message, true); }
    finally { setSaving(false); }
  };

  return (
    <div className="modal-overlay" onClick={saving ? undefined : onClose}>
      <div className="modal" onClick={e => e.stopPropagation()} style={{maxWidth:480}}>
        <h3 style={{marginBottom:16}}>New auth record</h3>
        <AuthForm form={form} setForm={setForm} record={null}/>
        <div className="upload-foot" style={{marginTop:20}}>
          <button className="btn btn-ghost" onClick={onClose} disabled={saving}>Cancel</button>
          <button className="btn btn-primary" onClick={save} disabled={saving || !form.name}>
            {saving ? 'Creating…' : 'Create record'}
          </button>
        </div>
      </div>
    </div>
  );
}

// ── Services ───────────────────────────────────────────────────────────

// The service form's fields, shared by the service page and the upload
// modal's new-service flow. `types` narrows the type picker — a dropped
// .txt can only ever be tasks or virtual.
function ServiceFormFields({ form, setForm, auth, adminAuthID, onCreateGate, types, service }) {
  // onCreateGate receives the slot key ('protected_by' | 'acts_as') so a
  // page can drop the record it creates into the slot that asked.
  const updDesc = (k,v) => setForm(f=>({...f,descriptor:{...f.descriptor,[k]:v}}));
  const isTasks = form.descriptor.type === 'tasks';
  const isVirtual = form.descriptor.type === 'virtual';
  const typeOptions = types || ['mcp','api','tasks','virtual'];

  // The descriptor is down to four fields, and which ones apply still turns
  // on the type. Auth is no longer among them — that lives in the slots now.
  const setType = (t) => {
    const d = {...form.descriptor, type: t};
    if (t === 'tasks' || t === 'virtual') delete d.proxied;
    if (t !== 'virtual') { delete d.database_target; delete d.database_name; }
    setForm(f=>({...f, descriptor: d}));
  };

  const gate = effectiveGate(form.protected_by, auth, adminAuthID);
  const actsAs = authRecord(auth, form.acts_as);
  // What an empty outbound slot means depends entirely on the gate — same
  // table resolveOutboundCred walks on the server, said in words.
  const outboundHelp = actsAs
    ? null
    : !gate ? 'No gate is configured, so whatever the caller sent rides through untouched.'
    : gate.kind === 'anonymous' ? 'Behind an open gate, whatever the caller sent rides through untouched.'
    : gate.kind === 'ssh_key' ? 'A passphrase yields no upstream credential, so nothing is sent.'
    : `The caller's own ${gate.name} credential is forwarded.`;

  return (
    <>
      <div className="field"><label>Name</label><input className="input" value={form.name} onChange={e=>setForm(f=>({...f,name:e.target.value}))}/></div>
      {!isTasks && !isVirtual && <div className="field"><label>URL</label><input className="input mono" value={form.url} onChange={e=>setForm(f=>({...f,url:e.target.value}))}/></div>}
      <div className="field-row">
        <div className="field"><label>Type</label>
          <select className="input" value={form.descriptor.type} onChange={e=>setType(e.target.value)}>
            {typeOptions.map(t=><option key={t} value={t}>{t.toLocaleUpperCase()}</option>)}
          </select>
        </div>
        {!isTasks && !isVirtual && <div className="field"><label>Proxied</label>
          <select className="input" value={form.descriptor.proxied?'true':'false'} onChange={e=>updDesc('proxied',e.target.value==='true')}>
            <option value="true">Yes</option><option value="false">No</option>
          </select>
        </div>}
      </div>

      <AuthSlot
        label="Protected by"
        placeholder="— inherit (admin auth) —"
        help={
          gate
            ? (form.protected_by == null
                ? `Inheriting ${gate.name}. Reached through an app, the app's gate governs instead.`
                : `Callers must clear ${gate.name}. Reached through an app, the app's gate governs instead.`)
            : 'No admin auth is configured, so this is open to anyone who can reach it.'
        }
        records={auth}
        value={form.protected_by}
        onChange={v=>setForm(f=>({...f,protected_by:v}))}
        onCreate={onCreateGate && (()=>onCreateGate('protected_by'))}
      />

      {form.descriptor.type === 'mcp' &&
        <div className="field">
          <label>Service acts as</label>
          <span className="help">
            The MCP server decides: open servers need nothing, and OAuth servers are signed in to at login.
            OAuth only works when the service is proxied.
          </span>
        </div>
      }
      {form.descriptor.type !== 'mcp' &&
        <AuthSlot
          label="Service acts as"
          placeholder="— the caller's credential —"
          help={outboundHelp}
          records={auth}
          value={form.acts_as}
          onChange={v=>setForm(f=>({...f,acts_as:v}))}
          onCreate={onCreateGate && (()=>onCreateGate('acts_as'))}
        />
      }

      {isVirtual && (
        <>
          <div className="field-row">
            <div className="field"><label>Database</label>
              <select className="input" value={form.descriptor.database_target==='global'?'global':(form.descriptor.database_target||'').startsWith('app:')?'app':''} onChange={e=>{
                const v = e.target.value;
                const d = {...form.descriptor};
                delete d.database_target;
                if (v === 'global') d.database_target = 'global';
                if (v === 'app') d.database_target = 'app:';
                setForm(f=>({...f, descriptor: d}));
              }}>
                <option value="">Each app's own (default)</option>
                <option value="global">Global (shared between apps)</option>
                <option value="app">One app's data</option>
              </select>
              <span className="help">Which database SQL steps in this service's tools run against.</span>
            </div>
            <div className="field"><label>Database name</label>
              <input className="input mono" value={form.descriptor.database_name||''} onChange={e=>updDesc('database_name',e.target.value)} placeholder="app"/>
            </div>
          </div>
          {(form.descriptor.database_target||'').startsWith('app:') && (
            <div className="field"><label>App nonce</label>
              <input className="input mono" value={form.descriptor.database_target.slice(4)||''} onChange={e=>updDesc('database_target','app:'+e.target.value)} placeholder="nonce of the app whose data this service touches"/>
            </div>
          )}
        </>
      )}
    </>
  );
}

// The service settings page — what the edit drawer used to be.
function ServicePage({ session, serviceId, isNew, services, auth, adminAuthID, apps, users, onRefresh, navigate }) {
  const { user } = useAuth();
  const { isAdmin } = useRoles();
  const service = isNew ? null : services.find(s => String(s.id) === String(serviceId));
  const [form, setForm] = useState({name:'',url:'',descriptor:{type:'mcp',proxied:false},protected_by:null,acts_as:null});
  const [members, setMembers] = useState([]);
  const [tools, setTools] = useState([]);
  const [toolsLoading, setToolsLoading] = useState(false);
  const [toolsError, setToolsError] = useState('');
  // Which slot is waiting on the inline "New…" modal, so the record it
  // creates lands in the slot that asked for it.
  const [creatingFor, setCreatingFor] = useState(null);
  const toast = useToast();
  const isEdit = !!service;

  useEffect(()=>{
    if (isEdit) setForm({
      name:service.name, url:service.url, descriptor:{...service.descriptor},
      protected_by:service.protected_by ?? null, acts_as:service.acts_as ?? null,
    });
    else setForm({name:'',url:'',descriptor:{type:'mcp',proxied:false},protected_by:null,acts_as:null});
    setMembers(service?.members || []);
  },[service ? service.id : null, isNew]); // eslint-disable-line react-hooks/exhaustive-deps

  const type = isEdit && service.descriptor?.type;
  const hasTools = type === 'tasks' || type === 'virtual';
  // Only tasks and virtual services have a definition file to share out.
  const hasFile = hasTools;

  useEffect(()=>{
    if (!isEdit || !hasTools) { setTools([]); setToolsError(''); return; }
    let cancelled = false;
    setToolsLoading(true);
    setToolsError('');
    frbr(session,'GET','/api/services/'+service.id+'/tools')
      .then(r => { if(!cancelled){ setTools(r.tools||[]); setToolsError(''); } })
      .catch(e => { if(!cancelled){ setTools([]); setToolsError(e.message); } })
      .finally(() => { if(!cancelled) setToolsLoading(false); });
    return () => { cancelled = true; };
  },[service ? service.id : null, isEdit, session]); // eslint-disable-line react-hooks/exhaustive-deps

  const save = async () => {
    try {
      const payload = {...form};
      // Virtual services don't need a URL — the server mints /mcp/{slug}.
      if (payload.descriptor.type === 'virtual') payload.url = '';
      if (isEdit) {
        await frbr(session, 'PUT','/api/services/'+service.id,payload);
        if (hasFile && !sameIDs(members, service.members || [])) {
          await frbr(session, 'PUT','/api/services/'+service.id+'/members',{members});
        }
        toast('Service updated'); onRefresh();
      } else {
        const resp = await frbr(session, 'POST','/api/services',payload);
        toast('Service created');
        onRefresh();
        navigate('service', { serviceId: String(resp.id) });
      }
    } catch(e) { toast(e.message,true); }
  };

  const remove = async () => {
    if (!service) return;
    let usedBy = [];
    try { const r = await frbr(session, 'GET','/api/services/'+service.id+'/apps'); usedBy = r.apps||[]; }
    catch(e) { /* ignore */ }
    let msg = 'Delete this service?';
    if (usedBy.length > 0) msg += `\n\nIt's used by ${usedBy.length} app${usedBy.length>1?'s':''}:\n${usedBy.map(a=>a.name).join(', ')}`;
    if (!confirm(msg)) return;
    try { await frbr(session, 'DELETE','/api/services/'+service.id); toast('Service deleted'); navigate('home'); onRefresh(); }
    catch(e) { toast(e.message,true); }
  };

  if ((!isNew && !service) || (isNew && !isAdmin)) {
    return (
      <>
        <PageHead title="Service not found" back={()=>navigate('home')} backLabel="Home"/>
        <div className="empty"><b>Service not found.</b> It may have been deleted.</div>
      </>
    );
  }

  const isTasks = form.descriptor.type === 'tasks';
  const isVirtual = form.descriptor.type === 'virtual';
  // Admins edit everything; a service member edits only the definition file.
  const canEditFile = isAdmin || (isEdit && canEditServiceFile(service, user));
  // The SSH service is the instance's own — seeded at startup, fronting
  // users' keys and each app's known hosts. It has no editable settings.
  const isBuiltin = isEdit && service.descriptor?.type === 'ssh';

  return (
    <>
      <PageHead
        title={service ? service.name : 'New service'}
        sub={service ? 'A registered provider — MCP, API, tasks, virtual or SSH.' : 'Name it and say what it is.'}
        back={()=>navigate('home')} backLabel="Home"
        actions={<>
          {isAdmin && service && !isBuiltin && (
            <button className="btn btn-ghost" style={{color:'var(--danger)'}} onClick={remove}><Icon name="trash" size={14}/> Delete</button>
          )}
          {isAdmin && !isBuiltin && (
            <button className="btn btn-primary" onClick={save} disabled={!form.name}>{service ? 'Save' : 'Create service'}</button>
          )}
        </>}
      />
      <div className={'page-form' + (!isBuiltin && isEdit && (isTasks || isVirtual) ? ' page-cols' : '')}>
        {isBuiltin && (
          <div className="field">
            <label>Built-in service</label>
            <span className="help">
              The SSH service is part of the instance — seeded at startup, it fronts users' SSH keys and each app's known hosts.
              There is nothing to edit or save here; apps get SSH access by linking this service on their own page.
            </span>
          </div>
        )}
        {!isBuiltin && (
          <div className="page-col">
            <fieldset className="read-only-fields" disabled={!isAdmin}>
              {!isAdmin && (
                <span className="help" style={{display:'block',marginBottom:12}}>
                  Read-only — only admins can change a service.{canEditFile && hasFile ? ' You can edit its definition file.' : ''}
                </span>
              )}
              <ServiceFormFields form={form} setForm={setForm} auth={auth} adminAuthID={adminAuthID}
                                 service={service} onCreateGate={isAdmin ? (slot)=>setCreatingFor(slot) : undefined}/>
            </fieldset>
            {isAdmin && isEdit && hasFile && (
              <div className="field">
                <label>Members</label>
                <span className="help">Users who may edit this service's definition file — nothing else about it. Admins always can.</span>
                <MultiSelect
                  options={users.filter(u => u.role === 'Member' || u.role === 'Read-only').map(u => ({value:u.id,label:u.name}))}
                  value={members}
                  onChange={setMembers}
                  placeholder="No members"
                />
              </div>
            )}
          </div>
        )}
        {!isBuiltin && isEdit && (isTasks || isVirtual) && (
          <div className="page-col">
            <div className="field">
              <div style={{display:'flex',alignItems:'center',justifyContent:'space-between',gap:12}}>
                <label style={{margin:0}}>Tools <Badge tone="gray" dot={false}>{tools.length}</Badge></label>
                {canEditFile && (
                  <button className="btn btn-sm btn-primary" onClick={()=>navigate('tools', {serviceId:String(service.id)})}>
                    <Icon name="edit" size={12}/> Edit
                  </button>
                )}
              </div>
              {toolsLoading && <span className="muted">Loading…</span>}
              {!toolsLoading && toolsError && <span className="help" style={{color:'var(--danger)'}}>{toolsError}</span>}
              {!toolsLoading && !toolsError && tools.length===0 && (
                <span className="muted">No tools found. Publish a {isTasks?'tasks':'virtual'} file to define tools.</span>
              )}
              {!toolsLoading && !toolsError && tools.length>0 && (
                <ul style={{margin:'8px 0 0',padding:0,listStyle:'none'}}>
                  {tools.map((t,i)=>
                    <li key={i} style={{padding:'6px 0',borderBottom:'1px solid var(--line-soft)'}}>
                      <b>{t.name}</b>
                      {t.description && <span className="muted"> — {t.description}</span>}
                    </li>
                  )}
                </ul>
              )}
            </div>
          </div>
        )}
      </div>
      {creatingFor && (
        <AuthRecordModal
          session={session}
          onClose={()=>setCreatingFor(null)}
          onSaved={(rec)=>{
            // Drop the new record straight into the slot that asked for it,
            // then refresh so every other dropdown sees it too.
            setForm(f=>({...f,[creatingFor]:rec.id}));
            setCreatingFor(null);
            onRefresh();
          }}
        />
      )}
    </>
  );
}

// ── Service tools editor ───────────────────────────────────────────────

function ServiceToolsEditor({ session, services, serviceId, onBack, onSaved }) {
  const service = services.find(s => String(s.id) === serviceId);
  const type = service?.descriptor?.type;
  const isTasks = type === 'tasks';
  const toast = useToast();
  const textareaRef = useRef(null);

  const [content,setContent] = useState('');
  const [loading,setLoading] = useState(true);
  const [saving,setSaving] = useState(false);
  const [dirty,setDirty] = useState(false);

  useEffect(() => {
    if (!service) return;
    let cancelled = false;
    setLoading(true);
    frbr(session, 'GET', '/api/services/' + serviceId + '/files', null, { rawText: true })
      .then(text => { if (!cancelled) { setContent(text || ''); setDirty(false); } })
      .catch(e => {
        if (cancelled) return;
        if (e.message.includes('404')) { setContent(''); setDirty(false); }
        else toast('Failed to load file: ' + e.message, true);
      })
      .finally(() => { if (!cancelled) setLoading(false); });
    return () => { cancelled = true; };
  }, [service, serviceId, session]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    const onKey = (e) => {
      if ((e.metaKey || e.ctrlKey) && e.key === 's') {
        e.preventDefault();
        if (!saving) handleSave();
      }
    };
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  }, [content, saving]); // eslint-disable-line react-hooks/exhaustive-deps

  const handleSave = async () => {
    if (!service) return;
    setSaving(true);
    try {
      const blob = new Blob([content], { type: 'text/plain' });
      const form = new FormData();
      form.append('file', blob, service.name + '.txt');
      await frbr(session, 'POST', '/api/services/' + serviceId + '/files', form, { rawText: true });
      setDirty(false);
      toast('File saved');
      onSaved?.();
    } catch (e) {
      toast('Failed to save: ' + e.message, true);
    } finally {
      setSaving(false);
    }
  };

  if (!service) {
    return (
      <>
        <PageHead title="Service not found" back={onBack} backLabel="Back"/>
        <div className="empty"><b>Service not found.</b></div>
      </>
    );
  }

  return (
    <div className="editor-page">
      {/* .editor-inner matches .main — same 1080px box and gutters — so
          the editor spans exactly the width the pages beneath it do. */}
      <div className="editor-inner">
        <PageHead
          title={`Edit ${isTasks ? 'tasks' : 'virtual'} script`}
          sub={`Plain-text definition for ${service.name}.`}
          back={onBack}
          backLabel={service.name}
          actions={
            <>
              <button className="btn btn-ghost" onClick={onBack} disabled={saving}>Cancel</button>
              <button className="btn btn-primary" onClick={handleSave} disabled={saving || !dirty}>
                {saving ? 'Saving…' : 'Save'} <span className="muted" style={{fontSize:11,marginLeft:6}}>⌘S</span>
              </button>
            </>
          }
        />
        {loading ? (
          <div style={{display:'grid',placeItems:'center',padding:48,color:'var(--ink-3)'}}>Loading…</div>
        ) : (
          <textarea
            ref={textareaRef}
            className="editor-textarea"
            value={content}
            onChange={e => { setContent(e.target.value); setDirty(true); }}
            spellCheck={false}
            autoComplete="off"
            autoCorrect="off"
            autoCapitalize="off"
          />
        )}
      </div>
    </div>
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
    if(group==='Users' && item==='Read') return false; // the users list is admin-only
    if(role.name==='Member')    return item==='Read' || (group==='Apps'&&item==='Edit');
    if(role.name==='Read-only') return item==='Read';
    return false;
  };
  return (
    <>
      <PageHead
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

function AuditView({ audit, isAdmin }) {
  const [q,setQ] = useState('');
  const filtered = audit.filter(a=>!q || `${a.actor} ${a.action} ${a.target}`.toLowerCase().includes(q.toLowerCase()));
  return (
    <>
      <PageHead
        title="Audit log"
        sub={isAdmin ? "Recent changes across the system." : "Your own recent activity."}
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

// One settings section, borrowed from the frbr-concept mockup: a fixed-width
// rail naming the area (plus a one-line “what this is”), and the controls
// beside it — label left, data right, a rule between sections.
function SettingSection({ title, desc, children }) {
  return (
    <section className="settings-section">
      <div className="settings-rail">
        <div className="settings-rail-title">{title}</div>
        <div className="settings-rail-desc">{desc}</div>
      </div>
      <div className="settings-body">{children}</div>
    </section>
  );
}

function SettingsView({ session, services, apps, auth, onRefresh }) {
  const [selectedAuth, setSelectedAuth] = useState('');
  const [savedAuth, setSavedAuth] = useState('');
  const [creatingGate, setCreatingGate] = useState(false);
  const [defaultApp, setDefaultApp] = useState('');
  const [dbMode, setDbMode] = useState('read-only');
  const [loading, setLoading] = useState(true);
  const toast = useToast();

  useEffect(() => {
    frbr(session, 'GET', '/api/settings')
      .then(d => {
        const id = d.admin_auth_service || '';
        setSelectedAuth(id);
        setSavedAuth(id);
        setDefaultApp(d.default_app || '');
        setDbMode(d.mcp_database_mode || 'read-only');
      })
      .catch(e => toast(e.message, true))
      .finally(() => setLoading(false));
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  const saveAuth = async () => {
    try {
      await frbr(session, 'PUT', '/api/settings', { admin_auth_service: selectedAuth });
      setSavedAuth(selectedAuth);
      toast('Settings saved');
    } catch(e) { toast(e.message, true); }
  };

  const unlink = async () => {
    if (!confirm('Remove admin auth? The control panel — and every app and service with an empty gate — will be open until auth is reconfigured.')) return;
    try {
      await frbr(session, 'PUT', '/api/settings', { admin_auth_service: '' });
      setSelectedAuth(''); setSavedAuth('');
      toast('Admin auth removed');
    } catch(e) { toast(e.message, true); }
  };

  const saveLanding = async () => {
    try {
      await frbr(session, 'PUT', '/api/settings', { default_app: defaultApp });
      toast('Landing page saved');
    } catch(e) { toast(e.message, true); }
  };

  const saveDBMode = async () => {
    try {
      await frbr(session, 'PUT', '/api/settings', { mcp_database_mode: dbMode });
      toast('Database mode saved');
    } catch(e) { toast(e.message, true); }
  };

  return (
    <>
      <PageHead title="Settings" sub="Control panel configuration."/>
      <div className="settings-layout">
        <SettingSection
          title="Admin authentication"
          desc="The auth record guarding this control panel — and the one every empty gate falls back to. An app or service naming no record of its own inherits this one, so the lazy default is the safe one. Leave it unset and the whole instance is open."
        >
        {loading ? <span className="muted">Loading…</span> : (
          <>
            <div style={{maxWidth:460}}>
              <AuthSlot
                label="Admin auth record"
                placeholder="— None (open access) —"
                help={selectedAuth
                  ? 'Every empty “Protected by” slot in this instance resolves to this record.'
                  : 'Nothing is configured, so nothing is gated — including apps and services with empty slots.'}
                records={auth}
                value={selectedAuth === '' ? null : Number(selectedAuth)}
                onChange={v => setSelectedAuth(v == null ? '' : String(v))}
                onCreate={() => setCreatingGate(true)}
              />
            </div>
            <div style={{display:'flex',gap:8,marginTop:16}}>
              <button className="btn btn-primary" onClick={saveAuth} disabled={selectedAuth === savedAuth}>Save</button>
              {savedAuth && <button className="btn btn-ghost" onClick={unlink}>Unlink</button>}
            </div>
            {creatingGate && (
              <AuthRecordModal
                session={session}
                onClose={()=>setCreatingGate(false)}
                onSaved={(rec)=>{ setSelectedAuth(String(rec.id)); setCreatingGate(false); onRefresh?.(); }}
              />
            )}
          </>
        )}
        </SettingSection>

        <SettingSection
          title="Default landing page"
          desc="Choose where visitors land when they hit the root URL. Only hosted apps are available as targets."
        >
        {loading ? <span className="muted">Loading…</span> : (
          <div className="field" style={{maxWidth:380}}>
            <label>Landing page</label>
            <select className="input" value={defaultApp} onChange={e => setDefaultApp(e.target.value)}>
              <option value="">Control Panel</option>
              {apps.filter(isHosted).map(a => (
                <option key={a.nonce} value={a.nonce}>{a.name}</option>
              ))}
            </select>
            {apps.filter(isHosted).length === 0 && (
              <span className="help">No hosted apps yet. Upload web content to an app to make it available as a landing page.</span>
            )}
            <div style={{display:'flex',gap:8,marginTop:16}}>
              <button className="btn btn-primary" onClick={saveLanding}>Save</button>
            </div>
          </div>
        )}
        </SettingSection>

        <SettingSection
          title="Update feeds"
          desc="Track remote feeds of encrypted app/service updates (they land in staging), or publish your own for other Freshbreath instances. Archives are AES-GCM encrypted with a per-feed key — the host can't tamper with them."
        >
          <RemoteUpdates session={session} apps={apps} services={services}/>
        </SettingSection>

        <SettingSection
          title="MCP database mode"
          desc={<>Whether the central MCP server's <code>db_execute</code> tool may write to databases. <code>db_query</code> is read-only no matter what; this only governs <code>db_execute</code>. Not a privilege boundary — an admin could do the same via the HTTP API — just accident prevention for a model asked to “clean up the old rows”.</>}
        >
        {loading ? <span className="muted">Loading…</span> : (
          <div className="field" style={{maxWidth:380}}>
            <label>Mode</label>
            <select className="input" value={dbMode} onChange={e => setDbMode(e.target.value)}>
              <option value="read-only">Read-only (default)</option>
              <option value="full-access">Full access</option>
            </select>
            <div style={{display:'flex',gap:8,marginTop:16}}>
              <button className="btn btn-primary" onClick={saveDBMode}>Save</button>
            </div>
          </div>
        )}
        </SettingSection>

      </div>
    </>
  );
}

// ── Profile ────────────────────────────────────────────────────────────

// The signed-in user's own page: their name and email, and their SSH key.
// Every role gets it; it's the one place a Member can change anything
// about their own account.
function ProfileView({ session }) {
  const { user, setUser } = useAuth();
  const [form, setForm] = useState({ name: user?.name || '', email: user?.email || '' });
  const [saving, setSaving] = useState(false);
  const toast = useToast();

  if (!user || user.id < 0) {
    return (
      <>
        <PageHead title="Profile" sub="Your own account."/>
        <div className="empty"><b>No account.</b> The control panel is open — there's no signed-in user to edit.</div>
      </>
    );
  }

  const dirty = form.name !== user.name || form.email !== user.email;
  const save = async () => {
    setSaving(true);
    try {
      const d = await frbr(session, 'PUT', '/api/me', form);
      setUser(d.user);
      toast('Profile saved');
    } catch (e) { toast(e.message, true); }
    finally { setSaving(false); }
  };

  return (
    <>
      <PageHead title="Profile" sub={<>Signed in as <Badge tone={roleTone(user.role)}>{user.role}</Badge></>}/>
      <div className="settings-layout">
        <SettingSection
          title="Your details"
          desc="How you appear across the control panel and the audit log. Your email is also what single sign-on logins are matched on — change it here if it changes there."
        >
          <div style={{maxWidth:380}}>
            <div className="field"><label>Name</label>
              <input className="input" value={form.name} onChange={e => setForm(f => ({...f, name: e.target.value}))}/>
            </div>
            <div className="field"><label>Email</label>
              <input className="input" type="email" value={form.email} onChange={e => setForm(f => ({...f, email: e.target.value}))}/>
            </div>
            <div style={{display:'flex',gap:8,marginTop:16}}>
              <button className="btn btn-primary" onClick={save} disabled={saving || !dirty || !form.name.trim() || !form.email.trim()}>
                {saving ? 'Saving…' : 'Save'}
              </button>
            </div>
          </div>
        </SettingSection>
        <SettingSection
          title="SSH key"
          desc="Your SSH key pair, for passphrase sign-in and agent forwarding. Only the public key is ever shown."
        >
          <SSHKeySection session={session} keyPath="/api/me/ssh-key" whose="your"/>
        </SettingSection>
      </div>
    </>
  );
}

// One user's SSH key: shows the public half and offers delete, or offers to
// generate one under a chosen passphrase. keyPath is /api/me/ssh-key for
// your own, /api/users/:id/ssh-key for an admin managing someone else's.
function SSHKeySection({ session, keyPath, whose }) {
  const [sshKey, setSSHKey] = useState(null);
  const [loading, setLoading] = useState(true);
  const [generating, setGenerating] = useState(false);
  const toast = useToast();

  useEffect(() => {
    setLoading(true);
    frbr(session, 'GET', keyPath)
      .then(d => setSSHKey(d.ssh_key))
      .catch(() => setSSHKey(null))
      .finally(() => setLoading(false));
  }, [keyPath]); // eslint-disable-line react-hooks/exhaustive-deps

  if (loading) return <span className="muted">Loading…</span>;
  if (!sshKey) return (
    <>
      <button className="btn btn-ghost" onClick={() => setGenerating(true)}><Icon name="key" size={14}/> Generate SSH key</button>
      {generating && (
        <PassphraseModal
          title="Generate SSH key"
          blurb={`Choose a passphrase for ${whose} SSH key. It's needed at each passphrase sign-in and can't be recovered if forgotten.`}
          onClose={() => setGenerating(false)}
          onSubmit={async (passphrase) => {
            const d = await frbr(session, 'POST', keyPath, { passphrase });
            setSSHKey(d.ssh_key);
            setGenerating(false);
            toast('SSH key generated');
          }}
        />
      )}
    </>
  );
  return (
    <>
      <div style={{marginBottom:12}}>
        <Badge tone="green">Active</Badge>
        <span className="muted" style={{marginLeft:8,fontSize:13}}>{sshKey.key_type?.toUpperCase()} · {sshKey.fingerprint}</span>
      </div>
      <div className="field" style={{maxWidth:560}}>
        <label>Public key</label>
        <div style={{display:'flex',gap:8}}>
          <input className="input mono" value={sshKey.public_key?.trim()} readOnly style={{fontSize:12,flex:1,minWidth:0}} />
          <button className="btn btn-ghost" onClick={() => copyText(sshKey.public_key?.trim(), toast)}><Icon name="copy" size={14}/></button>
        </div>
      </div>
      <button className="btn btn-ghost" style={{color:'var(--tone-red)'}} onClick={async () => {
        if (!confirm(`Delete ${whose} SSH key? A new one is needed for passphrase sign-in.`)) return;
        try { await frbr(session, 'DELETE', keyPath); setSSHKey(null); toast('SSH key deleted'); }
        catch(e) { toast(e.message, true); }
      }}>Delete key</button>
    </>
  );
}

// Asks for a passphrase twice. onSubmit receives it and may throw; the
// error is toasted and the modal stays open.
function PassphraseModal({ title, blurb, onClose, onSubmit }) {
  const [passphrase, setPassphrase] = useState('');
  const [confirmed, setConfirmed] = useState('');
  const [busy, setBusy] = useState(false);
  const toast = useToast();
  const submit = async () => {
    setBusy(true);
    try { await onSubmit(passphrase); }
    catch (e) { toast(e.message, true); }
    finally { setBusy(false); }
  };
  return (
    <div className="modal-overlay" onClick={onClose}>
      <div className="modal" onClick={e => e.stopPropagation()} style={{maxWidth:420}}>
        <h3 style={{marginBottom:16}}>{title}</h3>
        <p className="muted" style={{fontSize:13,marginBottom:16}}>{blurb}</p>
        <div className="field">
          <label>Passphrase</label>
          <input className="input" type="password" value={passphrase} onChange={e => setPassphrase(e.target.value)} placeholder="Min 8 characters" autoFocus />
        </div>
        <div className="field">
          <label>Confirm passphrase</label>
          <input className="input" type="password" value={confirmed} onChange={e => setConfirmed(e.target.value)} placeholder="Re-enter passphrase" />
        </div>
        <div style={{display:'flex',gap:8,justifyContent:'flex-end',marginTop:20}}>
          <button className="btn btn-ghost" onClick={onClose}>Cancel</button>
          <button className="btn btn-primary" disabled={busy || passphrase.length < 8 || passphrase !== confirmed} onClick={submit}>Generate</button>
        </div>
      </div>
    </div>
  );
}

// ── Remote Updates ──────────────────────────────────────────────
// sseStream now lives in frbr.js (shared with the opt-in auto-updater for
// hosted apps) and is exposed on window.FrBr.

// UpdateProgress renders the live apply/build event stream as a log.
function UpdateProgress({ events, onClose }) {
  const ref = useRef(null);
  useEffect(() => { ref.current?.scrollTo(0, ref.current.scrollHeight); }, [events]);
  const line = (e, i) => {
    const d = e.data || {};
    let text;
    switch (e.event) {
      case 'fetch': case 'decrypt': case 'validate': case 'manifest': case 'encrypt':
        text = `${e.event}…${d.ops ? ` (${d.ops} ops)` : ''}`; break;
      case 'collect': text = `collecting ${d.apps ?? 0} app(s), ${d.services ?? 0} service(s)`; break;
      case 'app': text = `packaged app ${d.nonce} (from ${d.source_slot})`; break;
      case 'service': text = `packaged service ${d.name}`; break;
      case 'op':
        text = d.status === 'start' ? `op ${d.index}: ${d.action} → ${d.target}` : `op ${d.index}: done`;
        break;
      case 'skip': text = `skipped — already applied${d.version ? ` (${d.version})` : ''}`; break;
      case 'done': text = d.download_url ? `built ${d.version} — downloading archive…` : `applied ${d.version} (${d.applied ?? '?'} ops)`; break;
      case 'summary': text = `done: ${d.applied ?? 0} applied, ${d.failed ?? 0} failed, ${d.skipped ?? 0} skipped`; break;
      case 'feed_error': text = `error at ${d.step ?? 'feed'}${d.id ? ` [${d.id}]` : ''}: ${d.message}`; break;
      default: text = e.event;
    }
    const tone = e.event === 'feed_error' || e.event === 'error' ? 'var(--tone-red)' :
      e.event === 'done' || e.event === 'summary' ? 'var(--green)' : '';
    return <div key={i} style={{color: tone || undefined}}>{text}</div>;
  };
  return (
    <div style={{marginTop:16}}>
      <div ref={ref} className="mono" style={{
        background:'var(--panel)', border:'1px solid var(--line)', borderRadius:8,
        padding:'10px 12px', maxHeight:220, overflowY:'auto', fontSize:12, lineHeight:1.7,
      }}>
        {events.length === 0 ? <span className="muted">waiting for events…</span> : events.map(line)}
      </div>
      {onClose && <div style={{marginTop:8}}><button className="btn btn-ghost" onClick={onClose}>Close log</button></div>}
    </div>
  );
}

// RemoteUpdates is the settings-page section for update feeds: receive
// feeds pull key-authenticated archives into staging; publish feeds build
// them for self-hosting. See design/remote-updates.md.
function RemoteUpdates({ session, apps, services }) {
  const [feeds, setFeeds] = useState(null);
  const [mode, setMode] = useState('receive');
  const [url, setUrl] = useState('');
  const [name, setName] = useState('');
  const [keyHex, setKeyHex] = useState('');
  const [newKey, setNewKey] = useState(null); // one-time reveal after create
  const [busy, setBusy] = useState(false);
  const [applying, setApplying] = useState(null); // {events: []}
  const [buildFor, setBuildFor] = useState(null); // feed being built
  const [buildSel, setBuildSel] = useState({ apps: [], services: [] });
  const [buildVersion, setBuildVersion] = useState('');
  const [buildLog, setBuildLog] = useState(null); // {events: [], done: null}
  const [available, setAvailable] = useState({}); // feed id → version pending, from the last check
  const [checking, setChecking] = useState(false);
  const toast = useToast();

  const load = () => frbr(session, 'GET', '/api/updates')
    .then(d => setFeeds(d.feeds || []))
    .catch(e => toast(e.message, true));
  useEffect(() => { load(); }, []); // eslint-disable-line react-hooks/exhaustive-deps

  const add = async () => {
    setBusy(true);
    try {
      const body = { mode, name };
      if (mode === 'receive') body.url = url;
      if (keyHex.trim()) body.key_hex = keyHex.trim();
      const d = await frbr(session, 'POST', '/api/updates', body);
      setNewKey({ id: d.id, key: d.key });
      setUrl(''); setName(''); setKeyHex('');
      load();
    } catch (e) { toast(e.message, true); }
    finally { setBusy(false); }
  };

  const remove = async (f) => {
    if (!confirm(`Delete update feed "${f.name || f.url || f.id}"?`)) return;
    try { await frbr(session, 'DELETE', '/api/updates/' + f.id); load(); toast('Feed deleted'); }
    catch (e) { toast(e.message, true); }
  };

  // The check endpoint is instance-wide (it walks every receive feed), so a
  // row's “Check now” refreshes them all — and the response is folded back
  // into the rows, each showing its own pending version until applied.
  const checkNow = async () => {
    setChecking(true);
    try {
      const d = await frbr(session, 'GET', '/api/updates/check');
      const ups = d.updates || [];
      const map = {};
      ups.forEach(u => { map[u.id] = u.version || '?'; });
      setAvailable(map);
      toast(ups.length === 0 ? 'All feeds up to date' :
        `${ups.length} update${ups.length > 1 ? 's' : ''} available: ${ups.map(u => u.version || '?').join(', ')}`);
    } catch (e) { toast(e.message, true); }
    finally { setChecking(false); }
  };

  const applyFeed = async (f) => {
    const entry = { events: [] };
    setApplying(entry);
    try {
      await FrBr.sseStream('/api/updates/apply', {
        body: { ids: [f.id] },
        onEvent: (ev, data) => setApplying(prev => prev && { ...prev, events: [...prev.events, { event: ev, data }] }),
      });
      load();
    } catch (e) { toast(e.message, true); }
  };

  const runBuild = async () => {
    const entry = { events: [], done: null };
    setBuildLog(entry);
    try {
      await FrBr.sseStream(`/api/updates/${buildFor.id}/build`, {
        session,
        body: { apps: buildSel.apps, services: buildSel.services, version: buildVersion || undefined },
        onEvent: (ev, data) => setBuildLog(prev => prev && {
          ...prev,
          events: [...prev.events, { event: ev, data }],
          done: ev === 'done' ? data : prev.done,
        }),
      });
    } catch (e) { toast(e.message, true); }
  };

  // Auto-download once the build's done event carries a URL.
  useEffect(() => {
    if (!buildLog?.done?.download_url) return;
    (async () => {
      try {
        const r = await fetch(buildLog.done.download_url, {
          headers: authHeaders(session),
        });
        if (!r.ok) throw new Error(`download: ${r.status}`);
        const blob = await r.blob();
        const a = document.createElement('a');
        a.href = URL.createObjectURL(blob);
        a.download = `update-${buildFor.id}.tar.gz.enc`;
        a.click();
        URL.revokeObjectURL(a.href);
        toast('Archive downloaded — host it at your feed URL');
      } catch (e) { toast(e.message, true); }
    })();
  }, [buildLog?.done]); // eslint-disable-line react-hooks/exhaustive-deps

  const toggleSel = (kind, val) => setBuildSel(s => ({
    ...s,
    [kind]: s[kind].includes(val) ? s[kind].filter(v => v !== val) : [...s[kind], val],
  }));

  const pubServices = services.filter(s => s.descriptor?.type === 'tasks' || s.descriptor?.type === 'virtual');

  return (
    <div>
      {/* feed list — one bordered card, a row per feed (frbr-concept layout):
          identity left, applied/pending/error status beside it, actions right. */}
      {feeds === null ? <span className="muted">Loading…</span> : feeds.length === 0 ? (
        <div className="feed-card feed-empty muted">No update feeds yet.</div>
      ) : (
        <div className="feed-card" style={{marginBottom:24}}>
          {feeds.map(f => (
            <div key={f.id} className="feed-row">
              <div className="feed-id">
                <div className="feed-name">
                  {f.name || <span className="muted">(unnamed)</span>}
                  <Badge tone={f.mode === 'publish' ? 'violet' : 'blue'}>{f.mode}</Badge>
                </div>
                <div className="feed-url mono">{f.url || '—'}</div>
              </div>
              <div className="feed-status">
                {f.mode === 'publish' ? <span className="muted">build and host archives for other instances</span> :
                  f.last_applied_version
                    ? <>{f.last_applied_version}<div className="muted" style={{fontSize:11}}>{fmtAuditTime(f.last_applied_at)}</div></>
                    : <span className="muted">never applied</span>}
                {f.mode === 'receive' && available[f.id] &&
                  <div style={{color:'var(--tone-green)'}}>update {available[f.id]} available</div>}
                {f.last_error && <div style={{color:'var(--tone-red)'}}>⚠ {f.last_error}</div>}
              </div>
              <div className="feed-actions">
                {f.mode === 'receive' ? <>
                  <button className="btn btn-ghost btn-sm" disabled={checking} onClick={checkNow}><Icon name="refresh" size={13}/> Check now</button>
                  <button className="btn btn-ghost btn-sm" onClick={() => applyFeed(f)}><Icon name="download" size={13}/> Apply</button>
                </> : <>
                  <button className="btn btn-ghost btn-sm" onClick={() => {
                    setBuildFor(f); setBuildSel({ apps: [], services: [] }); setBuildVersion(''); setBuildLog(null);
                  }}><Icon name="sparkle" size={13}/> Build archive</button>
                </>}
                <button className="btn btn-ghost btn-sm" style={{color:'var(--tone-red)'}} onClick={() => remove(f)} title="Delete feed"><Icon name="trash" size={13}/></button>
              </div>
            </div>
          ))}
        </div>
      )}

      {applying && <UpdateProgress events={applying.events} onClose={() => setApplying(null)} />}

      {/* add form */}
      <div style={{borderTop:feeds && feeds.length ? '1px solid var(--line-soft)' : undefined,paddingTop:20}}>
        <div style={{display:'flex',gap:8,flexWrap:'wrap'}}>
          <div className="field" style={{width:110}}>
            <label>Mode</label>
            <select className="input" value={mode} onChange={e => setMode(e.target.value)}>
              <option value="receive">receive</option>
              <option value="publish">publish</option>
            </select>
          </div>
          <div className="field" style={{flex:1,minWidth:220}}>
            <label>{mode === 'receive' ? 'Archive URL' : 'Feed URL (label)'}</label>
            <input className="input mono" style={{fontSize:12}} value={url} onChange={e => setUrl(e.target.value)}
              placeholder={mode === 'receive' ? 'https://…/update.tar.gz.enc' : 'https://…/where-you-host-it'} />
          </div>
          <div className="field" style={{width:180}}>
            <label>Name</label>
            <input className="input" value={name} onChange={e => setName(e.target.value)} placeholder="optional" />
          </div>
          <div className="field" style={{width:260}}>
            <label>Key (optional)</label>
            <input className="input mono" style={{fontSize:12}} value={keyHex} onChange={e => setKeyHex(e.target.value)} placeholder="64 hex chars — else generated" />
          </div>
          <div className="field" style={{alignSelf:'flex-end'}}>
            <button className="btn btn-primary" disabled={busy || (mode === 'receive' && !url.trim())} onClick={add}>
              <Icon name="plus" size={14}/> Add feed
            </button>
          </div>
        </div>
        {mode === 'receive' && (
          <p className="help" style={{marginTop:4}}>
            Leave key blank to generate one; paste the publisher's key to pair with their publish feed.
          </p>
        )}
      </div>

      {/* one-time key reveal */}
      {newKey && (
        <div style={{marginTop:20,padding:16,border:'1px solid var(--line)',borderRadius:8}}>
          <div style={{fontWeight:500,marginBottom:6}}>Feed encryption key — shown once</div>
          <p className="muted" style={{fontSize:13,marginBottom:12}}>
            Give this key to whoever encrypts the archives for this feed (or, if you supplied a publisher's key
            when pairing, you already know it). It will not be shown again.
          </p>
          <div style={{display:'flex',gap:8}}>
            <input className="input mono" style={{fontSize:12}} value={newKey.key} readOnly />
            <button className="btn btn-ghost" onClick={() => copyText(newKey.key, toast)}><Icon name="copy" size={14}/></button>
            <button className="btn btn-ghost" onClick={() => setNewKey(null)}>Done</button>
          </div>
        </div>
      )}

      {/* build modal */}
      {buildFor && (
        <div className="modal-overlay" onClick={() => setBuildFor(null)}>
          <div className="modal" onClick={e => e.stopPropagation()} style={{maxWidth:520}}>
            <h3 style={{marginBottom:12}}>Build archive</h3>
            <p className="muted" style={{fontSize:13,marginBottom:16}}>
              Package the selected apps (current staging slot — dev if none yet) and service definitions into an
              encrypted archive for “{buildFor.name || buildFor.url}”. Receivers land it in their staging slots.
            </p>
            <div className="field">
              <label>Version</label>
              <input className="input mono" style={{fontSize:12}} value={buildVersion} onChange={e => setBuildVersion(e.target.value)} placeholder="blank = timestamp" />
            </div>
            <div className="field">
              <label>Apps ({buildSel.apps.length} selected)</label>
              <div style={{maxHeight:140,overflowY:'auto',border:'1px solid var(--line)',borderRadius:8,padding:'6px 10px'}}>
                {apps.length === 0 && <span className="muted" style={{fontSize:13}}>no apps</span>}
                {apps.map(a => (
                  <label key={a.nonce} style={{display:'flex',gap:8,alignItems:'center',padding:'3px 0',fontSize:13,cursor:'pointer'}}>
                    <input type="checkbox" checked={buildSel.apps.includes(a.nonce)} onChange={() => toggleSel('apps', a.nonce)} />
                    {a.name} <span className="muted" style={{fontSize:11}}>{a.nonce}</span>
                  </label>
                ))}
              </div>
            </div>
            <div className="field">
              <label>Services ({buildSel.services.length} selected)</label>
              <div style={{maxHeight:120,overflowY:'auto',border:'1px solid var(--line)',borderRadius:8,padding:'6px 10px'}}>
                {pubServices.length === 0 && <span className="muted" style={{fontSize:13}}>no tasks/virtual services</span>}
                {pubServices.map(s => (
                  <label key={s.id} style={{display:'flex',gap:8,alignItems:'center',padding:'3px 0',fontSize:13,cursor:'pointer'}}>
                    <input type="checkbox" checked={buildSel.services.includes(s.name)} onChange={() => toggleSel('services', s.name)} />
                    {s.name} <span className="muted" style={{fontSize:11}}>{s.descriptor?.type}</span>
                  </label>
                ))}
              </div>
            </div>
            <div style={{display:'flex',gap:8,justifyContent:'flex-end',marginTop:16}}>
              <button className="btn btn-ghost" onClick={() => setBuildFor(null)}>Cancel</button>
              <button className="btn btn-primary" disabled={buildSel.apps.length + buildSel.services.length === 0} onClick={runBuild}>
                <Icon name="sparkle" size={14}/> Build
              </button>
            </div>
            {buildLog && <UpdateProgress events={buildLog.events} />}
          </div>
        </div>
      )}
    </div>
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

// The short stamp for the slot rows' narrow column: relative while it still
// reads ("5m ago"), then just the date. fmtAuditTime's full "Sep 28, 2:04 PM"
// is the table/timeline width; this is the sidebar one.
const fmtShortTime = (iso) => {
  if (!iso) return '—';
  const d = new Date(iso);
  const mins = Math.floor((Date.now() - d.getTime()) / 60000);
  if (mins < 1) return 'just now';
  if (mins < 60) return mins + 'm ago';
  if (mins < 1440) return Math.floor(mins / 60) + 'h ago';
  if (mins < 1440 * 14) return Math.floor(mins / 1440) + 'd ago';
  const sameYear = d.getFullYear() === new Date().getFullYear();
  return d.toLocaleDateString(undefined, {
    month: 'short', day: 'numeric',
    ...(sameYear ? {} : { year: '2-digit' }),
  });
};

// ── Routing ────────────────────────────────────────────────────────────

// Parses /control/... into a stable route object.
//   /control                                  -> home
//   /control/apps/new · /control/apps/:nonce  -> app page
//   /control/services/new · /control/services/:id
//     · /control/services/:id/edit-tools      -> service page / tools editor
//   /control/auth/new · /control/auth/:id     -> auth record page
//   /control/users|roles|audit|settings       -> the user area and settings
//   /control/profile                          -> the signed-in user's own page
const parseRoute = () => {
  const parts = window.location.pathname.replace(/^\/control\/?/, '').split('/').filter(Boolean);
  const [a, b, c] = parts;
  if (!a) return { page: 'home', params: {} };
  if (a === 'apps') {
    if (b === 'new') return { page: 'app', params: { isNew: true } };
    if (b) return { page: 'app', params: { nonce: b } };
  }
  if (a === 'services') {
    if (b === 'new') return { page: 'service', params: { isNew: true } };
    if (b && c === 'edit-tools') return { page: 'tools', params: { serviceId: b } };
    if (b) return { page: 'service', params: { serviceId: b } };
  }
  if (a === 'auth') {
    if (b === 'new') return { page: 'authrecord', params: { isNew: true } };
    if (b) return { page: 'authrecord', params: { authId: b } };
  }
  if (!b && ['users', 'roles', 'audit', 'settings', 'profile'].includes(a)) return { page: a, params: {} };
  return { page: 'home', params: {} };
};

const buildPath = (page, params = {}) => {
  if (page === 'app') return params.isNew ? '/control/apps/new' : `/control/apps/${params.nonce}`;
  if (page === 'service') return params.isNew ? '/control/services/new' : `/control/services/${params.serviceId}`;
  if (page === 'tools') return `/control/services/${params.serviceId}/edit-tools`;
  if (page === 'authrecord') return params.isNew ? '/control/auth/new' : `/control/auth/${params.authId}`;
  return page === 'home' ? '/control' : `/control/${page}`;
};

// ── App ────────────────────────────────────────────────────────────────

function AppShell() {
  const { user, session, authRequired, gateName, login, logout, sessionExpired, clearExpired, authError } = useAuth();
  const [route, setRoute] = useState(parseRoute);

  useEffect(() => {
    const onPop = () => setRoute(parseRoute());
    window.addEventListener('popstate', onPop);
    return () => window.removeEventListener('popstate', onPop);
  }, []);

  const navigate = (page, params = {}) => {
    history.pushState(null, '', buildPath(page, params));
    setRoute({ page, params });
  };
  const [users,setUsers] = useState([]);
  const [apps,setApps] = useState([]);
  const [services,setServices] = useState([]);
  const [auth,setAuth] = useState([]);
  const [adminAuthID,setAdminAuthID] = useState(null);
  const [roles,setRoles] = useState([]);
  const [audit,setAudit] = useState([]);
  const [loading,setLoading] = useState(true);
  const toast = useToast();
  const isAdmin = isAdminRole(user, authRequired);
  const isSuperuser = isSuperuserRole(user, authRequired);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      // The admin record is loaded alongside the rest because every gate
      // dropdown needs it: an empty slot resolves to it, so the panel cannot
      // label a single inherited gate without knowing which record that is.
      // The users list is admin-only, so nobody else asks for it.
      const [u,a,s,r,au,ar,st] = await Promise.all([
        isAdmin ? frbr(session, 'GET','/api/users') : Promise.resolve({users: []}),
        frbr(session, 'GET','/api/apps'),
        frbr(session, 'GET','/api/services'),
        frbr(session, 'GET','/api/roles'),
        frbr(session, 'GET','/api/audit'),
        frbr(session, 'GET','/api/auth'),
        // Settings are Superuser-only; everyone else reads the admin gate's
        // id from env.js, which carries it for the control panel.
        isSuperuser
          ? frbr(session, 'GET','/api/settings').catch(()=>({}))
          : Promise.resolve({admin_auth_service: window.__HOMESLICE_CONFIG?.authRecordID || null}),
      ]);
      setUsers(u.users||[]); setApps(a.apps||[]); setServices(s.services||[]);
      setRoles(r.roles||[]); setAudit(au.audit||[]); setAuth(ar.auth||[]);
      setAdminAuthID(st.admin_auth_service ? Number(st.admin_auth_service) : null);
    } catch(e) { if (!authRequired || user) toast('Failed to load: '+e.message, true); }
    setLoading(false);
  },[authRequired, user, session, isAdmin, isSuperuser]); // eslint-disable-line react-hooks/exhaustive-deps

  // Reload when who is signed in changes — not on every profile edit,
  // which hands back a fresh user object for the same person.
  useEffect(()=>{
    if (authRequired && !user) return;
    load();
  },[authRequired, user?.id, user?.role]); // eslint-disable-line react-hooks/exhaustive-deps

  if (authRequired && !user) return <LoginScreen gateName={gateName} onLogin={login} authError={authError}/>;

  if(loading) return <div style={{display:'grid',placeItems:'center',height:'100vh',color:'var(--ink-3)'}}>Loading…</div>;

  // Pages a role can't use fall back to home rather than a wall of 403s.
  const page =
    (route.page === 'users' && !isAdmin) || (route.page === 'settings' && !isSuperuser) ? 'home' : route.page;

  return (
    <div className="app-shell">
      <TopBar user={user} onNav={(id)=>navigate(id)} onLogout={user ? logout : null}/>
      <main className="main">
        {sessionExpired && <SessionBanner onLogin={login} onDismiss={clearExpired}/>}
        {['users','roles','audit'].includes(page) && <UserAreaTabs active={page} onNav={navigate}/>}
        {page==='home'      && <HomePage session={session} navigate={navigate} apps={apps} services={services} auth={auth} users={users} adminAuthID={adminAuthID} onRefresh={load}/>}
        {page==='app'       && <AppPage session={session} nonce={route.params.nonce} isNew={route.params.isNew} apps={apps} services={services} users={users} auth={auth} adminAuthID={adminAuthID} onRefresh={load} navigate={navigate}/>}
        {page==='service'   && <ServicePage session={session} serviceId={route.params.serviceId} isNew={route.params.isNew} services={services} auth={auth} adminAuthID={adminAuthID} apps={apps} users={users} onRefresh={load} navigate={navigate}/>}
        {page==='tools'     && <ServiceToolsEditor session={session} services={services} serviceId={route.params.serviceId} onBack={()=>navigate('service',{serviceId:route.params.serviceId})} onSaved={load}/>}
        {page==='authrecord'&& <AuthPage session={session} authId={route.params.authId} isNew={route.params.isNew} auth={auth} services={services} apps={apps} onRefresh={load} navigate={navigate}/>}
        {page==='users'     && <UsersView session={session} users={users} apps={apps} onRefresh={load}/>}
        {page==='roles'     && <RolesView roles={roles}/>}
        {page==='audit'     && <AuditView audit={audit} isAdmin={isAdmin}/>}
        {page==='profile'   && <ProfileView session={session}/>}
        {page==='settings'  && <SettingsView session={session} services={services} apps={apps} auth={auth} onRefresh={load}/>}
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
