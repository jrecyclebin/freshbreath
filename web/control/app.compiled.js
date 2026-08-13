// Generated from app.js by scripts/compile-control.mjs — do not edit directly.
// Fresh Breath Control Panel — admin SPA

const {
  useState,
  useEffect,
  useCallback,
  useRef,
  createContext,
  useContext
} = React;

// ── Icons ──────────────────────────────────────────────────────────────

const Icon = ({
  name,
  size = 16
}) => {
  const s = "currentColor";
  const w = 1.5;
  const c = {
    width: size,
    height: size,
    viewBox: "0 0 24 24",
    fill: "none",
    stroke: s,
    strokeWidth: w,
    strokeLinecap: "round",
    strokeLinejoin: "round"
  };
  const p = {
    home: /*#__PURE__*/React.createElement(React.Fragment, null, /*#__PURE__*/React.createElement("path", {
      d: "M3 11l9-7 9 7v9a1 1 0 0 1-1 1h-5v-6h-6v6H4a1 1 0 0 1-1-1z"
    })),
    users: /*#__PURE__*/React.createElement(React.Fragment, null, /*#__PURE__*/React.createElement("circle", {
      cx: "9",
      cy: "8",
      r: "3.5"
    }), /*#__PURE__*/React.createElement("path", {
      d: "M2.5 19c.5-3.5 3.2-5 6.5-5s6 1.5 6.5 5"
    }), /*#__PURE__*/React.createElement("path", {
      d: "M16 4.5a3.5 3.5 0 0 1 0 7"
    }), /*#__PURE__*/React.createElement("path", {
      d: "M21.5 19c-.3-2.6-1.8-4.1-4-4.7"
    })),
    apps: /*#__PURE__*/React.createElement(React.Fragment, null, /*#__PURE__*/React.createElement("rect", {
      x: "3.5",
      y: "3.5",
      width: "7",
      height: "7",
      rx: "1.5"
    }), /*#__PURE__*/React.createElement("rect", {
      x: "13.5",
      y: "3.5",
      width: "7",
      height: "7",
      rx: "1.5"
    }), /*#__PURE__*/React.createElement("rect", {
      x: "3.5",
      y: "13.5",
      width: "7",
      height: "7",
      rx: "1.5"
    }), /*#__PURE__*/React.createElement("rect", {
      x: "13.5",
      y: "13.5",
      width: "7",
      height: "7",
      rx: "1.5"
    })),
    shield: /*#__PURE__*/React.createElement(React.Fragment, null, /*#__PURE__*/React.createElement("path", {
      d: "M12 3l8 3v6c0 5-3.5 8-8 9-4.5-1-8-4-8-9V6z"
    })),
    log: /*#__PURE__*/React.createElement(React.Fragment, null, /*#__PURE__*/React.createElement("path", {
      d: "M5 4h11l3 3v13a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1V5a1 1 0 0 1 1-1z"
    }), /*#__PURE__*/React.createElement("path", {
      d: "M8 11h8M8 15h8M8 7h5"
    })),
    plus: /*#__PURE__*/React.createElement(React.Fragment, null, /*#__PURE__*/React.createElement("path", {
      d: "M12 5v14M5 12h14"
    })),
    search: /*#__PURE__*/React.createElement(React.Fragment, null, /*#__PURE__*/React.createElement("circle", {
      cx: "11",
      cy: "11",
      r: "7"
    }), /*#__PURE__*/React.createElement("path", {
      d: "M21 21l-4.3-4.3"
    })),
    close: /*#__PURE__*/React.createElement(React.Fragment, null, /*#__PURE__*/React.createElement("path", {
      d: "M6 6l12 12M18 6L6 18"
    })),
    edit: /*#__PURE__*/React.createElement(React.Fragment, null, /*#__PURE__*/React.createElement("path", {
      d: "M14 4l6 6"
    }), /*#__PURE__*/React.createElement("path", {
      d: "M4 20l5-1L20 8l-5-5L4 14z"
    })),
    trash: /*#__PURE__*/React.createElement(React.Fragment, null, /*#__PURE__*/React.createElement("path", {
      d: "M4 7h16M9 7V4h6v3M6 7l1 13a1 1 0 0 0 1 1h8a1 1 0 0 0 1-1l1-13"
    })),
    check: /*#__PURE__*/React.createElement(React.Fragment, null, /*#__PURE__*/React.createElement("path", {
      d: "M5 12l5 5L20 7"
    })),
    more: /*#__PURE__*/React.createElement(React.Fragment, null, /*#__PURE__*/React.createElement("circle", {
      cx: "5",
      cy: "12",
      r: "1.4"
    }), /*#__PURE__*/React.createElement("circle", {
      cx: "12",
      cy: "12",
      r: "1.4"
    }), /*#__PURE__*/React.createElement("circle", {
      cx: "19",
      cy: "12",
      r: "1.4"
    })),
    download: /*#__PURE__*/React.createElement(React.Fragment, null, /*#__PURE__*/React.createElement("path", {
      d: "M12 4v12M7 11l5 5 5-5M5 20h14"
    })),
    filter: /*#__PURE__*/React.createElement(React.Fragment, null, /*#__PURE__*/React.createElement("path", {
      d: "M4 5h16l-6 8v6l-4-2v-4z"
    })),
    sort: /*#__PURE__*/React.createElement(React.Fragment, null, /*#__PURE__*/React.createElement("path", {
      d: "M7 4v16M3 8l4-4 4 4"
    }), /*#__PURE__*/React.createElement("path", {
      d: "M17 20V4M13 16l4 4 4-4"
    })),
    copy: /*#__PURE__*/React.createElement(React.Fragment, null, /*#__PURE__*/React.createElement("rect", {
      x: "8",
      y: "8",
      width: "12",
      height: "12",
      rx: "2"
    }), /*#__PURE__*/React.createElement("path", {
      d: "M16 8V5a1 1 0 0 0-1-1H5a1 1 0 0 0-1 1v10a1 1 0 0 0 1 1h3"
    })),
    sparkle: /*#__PURE__*/React.createElement(React.Fragment, null, /*#__PURE__*/React.createElement("path", {
      d: "M12 2l2.2 7.8L22 12l-7.8 2.2L12 22l-2.2-7.8L2 12l7.8-2.2z"
    })),
    bell: /*#__PURE__*/React.createElement(React.Fragment, null, /*#__PURE__*/React.createElement("path", {
      d: "M6 9a6 6 0 0 1 12 0c0 5 2 6 2 7H4c0-1 2-2 2-7z"
    }), /*#__PURE__*/React.createElement("path", {
      d: "M10 19a2 2 0 0 0 4 0"
    })),
    refresh: /*#__PURE__*/React.createElement(React.Fragment, null, /*#__PURE__*/React.createElement("path", {
      d: "M3 12a9 9 0 0 1 15-6.7L21 8"
    }), /*#__PURE__*/React.createElement("path", {
      d: "M21 3v5h-5"
    }), /*#__PURE__*/React.createElement("path", {
      d: "M21 12a9 9 0 0 1-15 6.7L3 16"
    }), /*#__PURE__*/React.createElement("path", {
      d: "M3 21v-5h5"
    })),
    lock: /*#__PURE__*/React.createElement(React.Fragment, null, /*#__PURE__*/React.createElement("rect", {
      x: "5",
      y: "11",
      width: "14",
      height: "9",
      rx: "2"
    }), /*#__PURE__*/React.createElement("path", {
      d: "M8 11V8a4 4 0 0 1 8 0v3"
    })),
    mail: /*#__PURE__*/React.createElement(React.Fragment, null, /*#__PURE__*/React.createElement("rect", {
      x: "3",
      y: "5",
      width: "18",
      height: "14",
      rx: "2"
    }), /*#__PURE__*/React.createElement("path", {
      d: "M3 7l9 6 9-6"
    })),
    cog: /*#__PURE__*/React.createElement(React.Fragment, null, /*#__PURE__*/React.createElement("circle", {
      cx: "12",
      cy: "12",
      r: "3"
    }), /*#__PURE__*/React.createElement("path", {
      d: "M19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.8-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1-1.5 1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.7 1.7 0 0 0 1.5-1 1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3 1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.8 1.7 1.7 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z"
    })),
    plug: /*#__PURE__*/React.createElement(React.Fragment, null, /*#__PURE__*/React.createElement("path", {
      d: "M12 22v-5"
    }), /*#__PURE__*/React.createElement("path", {
      d: "M9 8V2"
    }), /*#__PURE__*/React.createElement("path", {
      d: "M15 8V2"
    }), /*#__PURE__*/React.createElement("path", {
      d: "M18 8v5a6 6 0 0 1-12 0V8z"
    })),
    signout: /*#__PURE__*/React.createElement(React.Fragment, null, /*#__PURE__*/React.createElement("path", {
      d: "M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4"
    }), /*#__PURE__*/React.createElement("polyline", {
      points: "16 17 21 12 16 7"
    }), /*#__PURE__*/React.createElement("line", {
      x1: "21",
      y1: "12",
      x2: "9",
      y2: "12"
    })),
    menu: /*#__PURE__*/React.createElement(React.Fragment, null, /*#__PURE__*/React.createElement("path", {
      d: "M3 6h18M3 12h18M3 18h18"
    })),
    moon: /*#__PURE__*/React.createElement(React.Fragment, null, /*#__PURE__*/React.createElement("path", {
      d: "M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z"
    })),
    sun: /*#__PURE__*/React.createElement(React.Fragment, null, /*#__PURE__*/React.createElement("circle", {
      cx: "12",
      cy: "12",
      r: "5"
    }), /*#__PURE__*/React.createElement("path", {
      d: "M12 1v2M12 21v2M4.22 4.22l1.42 1.42M18.36 18.36l1.42 1.42M1 12h2M21 12h2M4.22 19.78l1.42-1.42M18.36 5.64l1.42-1.42"
    }))
  };
  return /*#__PURE__*/React.createElement("svg", c, p[name] || p.more);
};

// ── UI primitives ──────────────────────────────────────────────────────

const AVATAR_HUES = [20, 60, 110, 150, 200, 240, 280, 320];
const initls = n => n?.split(/\s+/).map(s => s[0]).slice(0, 2).join('').toUpperCase() || '??';
const hashHue = (s = '') => {
  let h = 0;
  for (let i = 0; i < s.length; i++) h = h * 31 + s.charCodeAt(i) >>> 0;
  return AVATAR_HUES[h % AVATAR_HUES.length];
};
const Avatar = ({
  name,
  size = 32
}) => {
  const hue = hashHue(name);
  const bg = `linear-gradient(135deg, oklch(0.78 0.07 ${hue}), oklch(0.55 0.1 ${(hue + 30) % 360}))`;
  return /*#__PURE__*/React.createElement("div", {
    className: "avatar",
    style: {
      width: size,
      height: size,
      fontSize: size * 0.36,
      background: bg
    }
  }, initls(name));
};
const Badge = ({
  tone = "gray",
  dot = true,
  children
}) => /*#__PURE__*/React.createElement("span", {
  className: `badge ${tone}`
}, dot && /*#__PURE__*/React.createElement("span", {
  className: "dot"
}), children);
const statusTone = (s = '') => ({
  Active: 'green',
  Invited: 'blue',
  Suspended: 'red'
})[s] || 'gray';
const envTone = (e = '') => ({
  Production: 'green',
  Staging: 'amber',
  Development: 'blue'
})[e] || 'gray';
const envShort = (e = '') => ({
  Production: 'Prod',
  Staging: 'Staging',
  Development: 'Dev'
})[e] || e;
const roleTone = (r = '') => ({
  Superuser: 'violet',
  Admin: 'blue',
  Member: 'gray',
  'Read-only': 'gray'
})[r] || 'gray';
const actionIcon = (a = '') => {
  const x = a.toLowerCase();
  if (x.includes('login') || x.includes('sign in')) return {
    icon: 'lock',
    tone: 'blue'
  };
  if (x.includes('created')) return {
    icon: 'plus',
    tone: 'green'
  };
  if (x.includes('deleted') || x.includes('removed')) return {
    icon: 'trash',
    tone: 'red'
  };
  if (x.includes('updated') || x.includes('edited')) return {
    icon: 'edit',
    tone: 'gray'
  };
  if (x.includes('role')) return {
    icon: 'shield',
    tone: 'violet'
  };
  return {
    icon: 'cog',
    tone: 'gray'
  };
};
const Drawer = ({
  open,
  title,
  onClose,
  footer,
  children
}) => {
  useEffect(() => {
    const esc = e => {
      if (e.key === 'Escape') onClose();
    };
    if (open) window.addEventListener('keydown', esc);
    return () => window.removeEventListener('keydown', esc);
  }, [open, onClose]);
  return /*#__PURE__*/React.createElement(React.Fragment, null, /*#__PURE__*/React.createElement("div", {
    className: `drawer-scrim ${open ? 'open' : ''}`,
    onClick: onClose
  }), /*#__PURE__*/React.createElement("div", {
    className: `drawer ${open ? 'open' : ''}`,
    role: "dialog",
    "aria-modal": "true"
  }, /*#__PURE__*/React.createElement("div", {
    className: "drawer-head"
  }, /*#__PURE__*/React.createElement("h3", null, title), /*#__PURE__*/React.createElement("button", {
    className: "btn btn-icon btn-ghost",
    onClick: onClose
  }, /*#__PURE__*/React.createElement(Icon, {
    name: "close",
    size: 16
  }))), /*#__PURE__*/React.createElement("div", {
    className: "drawer-body"
  }, children), footer && /*#__PURE__*/React.createElement("div", {
    className: "drawer-foot"
  }, footer)));
};

// ── MultiSelect ────────────────────────────────────────────────────────

const MultiSelect = ({
  options,
  value = [],
  onChange,
  placeholder = 'Select…'
}) => {
  const [open, setOpen] = useState(false);
  const ref = useRef(null);
  useEffect(() => {
    const handler = e => {
      if (ref.current && !ref.current.contains(e.target)) setOpen(false);
    };
    document.addEventListener('mousedown', handler);
    return () => document.removeEventListener('mousedown', handler);
  }, []);
  const sel = new Set(value);
  const toggle = val => {
    const next = new Set(sel);
    if (next.has(val)) next.delete(val);else next.add(val);
    onChange([...next]);
  };
  const remove = (val, e) => {
    e.stopPropagation();
    onChange(value.filter(v => v !== val));
  };
  return /*#__PURE__*/React.createElement("div", {
    className: "multiselect",
    ref: ref
  }, /*#__PURE__*/React.createElement("div", {
    className: "multiselect-control",
    onClick: () => setOpen(o => !o)
  }, /*#__PURE__*/React.createElement("div", {
    className: "multiselect-tags"
  }, value.length === 0 ? /*#__PURE__*/React.createElement("span", {
    className: "multiselect-placeholder"
  }, placeholder) : value.map(val => {
    const opt = options.find(o => o.value === val);
    return /*#__PURE__*/React.createElement("span", {
      key: val,
      className: "multiselect-tag"
    }, opt?.label ?? val, /*#__PURE__*/React.createElement("button", {
      type: "button",
      className: "multiselect-tag-remove",
      onClick: e => remove(val, e)
    }, /*#__PURE__*/React.createElement(Icon, {
      name: "close",
      size: 9
    })));
  })), /*#__PURE__*/React.createElement("span", {
    className: "multiselect-chevron"
  }, /*#__PURE__*/React.createElement(Icon, {
    name: "sort",
    size: 12
  }))), open && /*#__PURE__*/React.createElement("div", {
    className: "multiselect-dropdown"
  }, options.length === 0 ? /*#__PURE__*/React.createElement("div", {
    className: "multiselect-empty"
  }, "No options available") : options.map(opt => {
    const isSelected = sel.has(opt.value);
    return /*#__PURE__*/React.createElement("div", {
      key: opt.value,
      className: `multiselect-option${isSelected ? ' selected' : ''}`,
      onClick: () => toggle(opt.value)
    }, /*#__PURE__*/React.createElement("span", {
      className: "multiselect-check"
    }, isSelected && /*#__PURE__*/React.createElement(Icon, {
      name: "check",
      size: 12
    })), opt.label);
  })));
};

// Toast
const ToastCtx = createContext(() => {});
const useToast = () => useContext(ToastCtx);
const ToastProvider = ({
  children
}) => {
  const [toasts, setToasts] = useState([]);
  const push = useCallback((msg, err) => {
    const id = Math.random().toString(36).slice(2, 8);
    setToasts(t => [...t, {
      id,
      msg,
      err
    }]);
    setTimeout(() => setToasts(t => t.filter(x => x.id !== id)), 2800);
  }, []);
  return /*#__PURE__*/React.createElement(ToastCtx.Provider, {
    value: push
  }, children, /*#__PURE__*/React.createElement("div", {
    className: "toast-wrap"
  }, toasts.map(t => /*#__PURE__*/React.createElement("div", {
    key: t.id,
    className: `toast ${t.err ? 'toast-error' : ''}`
  }, /*#__PURE__*/React.createElement("span", {
    className: "check"
  }, /*#__PURE__*/React.createElement(Icon, {
    name: t.err ? 'close' : 'check',
    size: 10
  })), t.msg))));
};

// ── Auth ───────────────────────────────────────────────────────────────

const AuthCtx = createContext({
  user: null,
  token: null,
  authRequired: false,
  serviceName: '',
  sessionExpired: false,
  login: () => {},
  logout: () => {},
  clearExpired: () => {}
});
const useAuth = () => useContext(AuthCtx);
function AuthProvider({
  children
}) {
  const [ready, setReady] = useState(false);
  const [user, setUser] = useState(null);
  const [token, setToken] = useState(null);
  const [authRequired, setAuthRequired] = useState(false);
  const [serviceName, setServiceName] = useState('');
  const [sessionExpired, setSessionExpired] = useState(false);
  const [authError, setAuthError] = useState('');
  useEffect(() => {
    _onUnauthorized = () => {
      setUser(null);
      setToken(null);
      setSessionExpired(true);
    };
    return () => {
      _onUnauthorized = null;
    };
  }, []);
  useEffect(() => {
    (async () => {
      try {
        const cfg = window.__HOMESLICE_CONFIG || {};
        window.FrBr.Svc.on('refresh', s => {
          console.log("Access token expired, refreshing...");
          localStorage.setItem('frebre_admin', s.toJSON());
          setToken(s);
        });
        if (!cfg.authRequired) {
          setReady(true);
          return;
        }
        setAuthRequired(true);
        setServiceName(cfg.authServiceName || '');
        const stored = localStorage.getItem('frebre_admin');
        if (stored) {
          const result = window.FrBr.load(stored);
          setToken(result);
          const d = await api(result, 'GET', '/api/me');
          setUser(d.user);
        }
      } catch {}
      setReady(true);
    })();
  }, []);
  const login = async () => {
    const cfg = window.__HOMESLICE_CONFIG || {};
    const serviceURL = cfg.authServiceURL;
    if (!serviceURL) return;
    const result = await window.FrBr.login(serviceURL);
    setSessionExpired(false);
    localStorage.setItem('frebre_admin', result.toJSON());
    setToken(result);

    // Verify the token and load user
    const d = await api(result, 'GET', '/api/me');
    setUser(d.user);
  };
  const logout = () => {
    localStorage.removeItem('frebre_admin');
    setUser(null);
    setSessionExpired(false);
  };
  const clearExpired = () => setSessionExpired(false);
  if (!ready) return /*#__PURE__*/React.createElement("div", {
    style: {
      display: 'grid',
      placeItems: 'center',
      height: '100vh',
      color: 'var(--ink-3)'
    }
  }, "Loading\u2026");
  return /*#__PURE__*/React.createElement(AuthCtx.Provider, {
    value: {
      user,
      token,
      authRequired,
      serviceName,
      sessionExpired,
      login,
      logout,
      clearExpired,
      authError
    }
  }, children);
}
function LoginScreen({
  serviceName,
  onLogin,
  authError
}) {
  const cfg = window.__HOMESLICE_CONFIG || {};
  const isSSH = cfg.authServiceType === 'ssh';
  const errorMessages = {
    no_user: 'Your account is not registered. Please contact an administrator.',
    invalid_token: 'Authentication failed. Please try again.',
    no_email: 'Your identity provider did not share an email address.'
  };
  return /*#__PURE__*/React.createElement("div", {
    className: "login-screen"
  }, /*#__PURE__*/React.createElement("aside", {
    className: "login-aside"
  }, /*#__PURE__*/React.createElement("div", {
    className: "quiet-grid"
  }), /*#__PURE__*/React.createElement("div", {
    className: "login-aside-inner"
  }, /*#__PURE__*/React.createElement("div", {
    className: "login-brand"
  }, /*#__PURE__*/React.createElement("span", {
    className: "brand-mark"
  }), "Fresh Breath"), /*#__PURE__*/React.createElement("h1", {
    className: "login-headline"
  }, "A quieter place", /*#__PURE__*/React.createElement("br", null), "to run ", /*#__PURE__*/React.createElement("em", null, "your services.")), /*#__PURE__*/React.createElement("p", {
    className: "login-sub"
  }, "Manage users, applications, and auth across every service you operate \u2014 without leaving the calm.")), /*#__PURE__*/React.createElement("div", {
    className: "login-foot"
  }, /*#__PURE__*/React.createElement("span", null, "admin panel"), /*#__PURE__*/React.createElement("span", null, window.__HOMESLICE_CONFIG?.version || 'dev'))), /*#__PURE__*/React.createElement("main", {
    className: "login-main"
  }, /*#__PURE__*/React.createElement("div", {
    className: "login-card"
  }, /*#__PURE__*/React.createElement("div", null, /*#__PURE__*/React.createElement("h2", null, "Sign in to Fresh Breath"), /*#__PURE__*/React.createElement("p", {
    className: "lead"
  }, isSSH ? 'Sign in with your SSH key passphrase.' : 'Use your work account to access the control panel.')), authError && /*#__PURE__*/React.createElement("div", {
    className: "login-error"
  }, /*#__PURE__*/React.createElement(Icon, {
    name: "bell",
    size: 14
  }), /*#__PURE__*/React.createElement("span", null, errorMessages[authError] || 'Authentication error.')), /*#__PURE__*/React.createElement("button", {
    className: "oidc-btn oidc-primary",
    onClick: onLogin
  }, /*#__PURE__*/React.createElement("span", {
    className: "glyph"
  }, /*#__PURE__*/React.createElement(Icon, {
    name: isSSH ? 'lock' : 'lock',
    size: 16
  })), isSSH ? 'Sign in with SSH key' : `Continue with ${serviceName || 'your identity provider'}`, /*#__PURE__*/React.createElement("span", {
    className: "meta"
  }, isSSH ? 'SSH' : 'OIDC')))));
}
function SessionBanner({
  onLogin,
  onDismiss
}) {
  return /*#__PURE__*/React.createElement("div", {
    className: "session-banner"
  }, /*#__PURE__*/React.createElement(Icon, {
    name: "lock",
    size: 14
  }), /*#__PURE__*/React.createElement("span", null, "Your session has expired."), /*#__PURE__*/React.createElement("button", {
    className: "btn btn-sm btn-primary",
    onClick: onLogin
  }, "Sign in again"), /*#__PURE__*/React.createElement("button", {
    className: "btn btn-icon btn-ghost",
    style: {
      marginLeft: 'auto',
      color: 'inherit'
    },
    onClick: onDismiss
  }, /*#__PURE__*/React.createElement(Icon, {
    name: "close",
    size: 12
  })));
}

// ── Nav ────────────────────────────────────────────────────────────────

const NAV = [{
  id: 'home',
  label: 'Overview',
  icon: 'home'
}, {
  id: 'apps',
  label: 'Apps',
  icon: 'apps',
  countKey: 'apps'
}, {
  id: 'services',
  label: 'Services',
  icon: 'plug',
  countKey: 'services'
}, {
  id: 'users',
  label: 'Users',
  icon: 'users',
  countKey: 'users'
}, {
  id: 'roles',
  label: 'Roles',
  icon: 'shield'
}, {
  id: 'audit',
  label: 'Audit log',
  icon: 'log'
}, {
  id: 'settings',
  label: 'Settings',
  icon: 'cog'
}];
function MobileTopBar({
  onMenuOpen,
  pageLabel
}) {
  return /*#__PURE__*/React.createElement("div", {
    className: "mobile-topbar"
  }, /*#__PURE__*/React.createElement("div", {
    className: "mb-brand"
  }, /*#__PURE__*/React.createElement("img", {
    src: "/control/freshbreath.svg",
    alt: "Fresh Breath",
    className: "brand-logo"
  })), /*#__PURE__*/React.createElement("div", {
    style: {
      display: 'flex',
      alignItems: 'center',
      gap: 8
    }
  }, pageLabel && /*#__PURE__*/React.createElement("span", {
    className: "mb-page"
  }, pageLabel), /*#__PURE__*/React.createElement("button", {
    className: "btn btn-icon btn-ghost",
    onClick: onMenuOpen,
    "aria-label": "Open menu"
  }, /*#__PURE__*/React.createElement(Icon, {
    name: "menu",
    size: 18
  }))));
}
function Sidebar({
  active,
  onNav,
  counts,
  user,
  onLogout,
  mobileOpen,
  onMobileClose
}) {
  const workspace = NAV.slice(0, 4);
  const security = NAV.slice(4);
  const displayName = user?.name || 'Admin';
  const displayRole = user?.role || 'Superuser';
  const [dark, setDark] = useState(() => document.documentElement.dataset.theme === 'dark');
  useEffect(() => {
    const obs = new MutationObserver(() => setDark(document.documentElement.dataset.theme === 'dark'));
    obs.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ['data-theme']
    });
    return () => obs.disconnect();
  }, []);
  const toggleTheme = () => {
    const next = document.documentElement.dataset.theme === 'dark' ? 'light' : 'dark';
    document.documentElement.dataset.theme = next;
    localStorage.setItem('frebre_theme', next);
    setDark(next === 'dark');
  };
  const handleNav = id => {
    onNav(id);
    onMobileClose?.();
  };
  return /*#__PURE__*/React.createElement("aside", {
    className: `sidebar${mobileOpen ? ' mobile-open' : ''}`
  }, /*#__PURE__*/React.createElement("div", {
    className: "sb-brand"
  }, /*#__PURE__*/React.createElement("img", {
    src: "/control/freshbreath.svg",
    alt: "Fresh Breath",
    className: "brand-logo"
  })), /*#__PURE__*/React.createElement("div", null, /*#__PURE__*/React.createElement("div", {
    className: "sb-section sb-section-row"
  }, /*#__PURE__*/React.createElement("span", null, "Workspace"), /*#__PURE__*/React.createElement("button", {
    className: "theme-toggle",
    onClick: toggleTheme,
    title: dark ? 'Switch to light' : 'Switch to dark'
  }, /*#__PURE__*/React.createElement(Icon, {
    name: dark ? 'sun' : 'moon',
    size: 16
  }))), /*#__PURE__*/React.createElement("div", {
    className: "sb-nav"
  }, workspace.map(n => NavLink(n, active, handleNav, counts)))), /*#__PURE__*/React.createElement("div", null, /*#__PURE__*/React.createElement("div", {
    className: "sb-section"
  }, "Security"), /*#__PURE__*/React.createElement("div", {
    className: "sb-nav"
  }, security.map(n => NavLink(n, active, handleNav, counts)))), /*#__PURE__*/React.createElement("div", {
    className: "sb-foot"
  }, /*#__PURE__*/React.createElement("div", {
    style: {
      display: 'flex',
      alignItems: 'center',
      gap: 4
    }
  }, /*#__PURE__*/React.createElement("div", {
    className: "sb-user",
    style: {
      flex: 1
    },
    title: displayName
  }, /*#__PURE__*/React.createElement(Avatar, {
    name: displayName,
    size: 28
  }), /*#__PURE__*/React.createElement("div", {
    className: "sb-user-text"
  }, /*#__PURE__*/React.createElement("b", null, displayName), /*#__PURE__*/React.createElement("span", null, displayRole))), onLogout && /*#__PURE__*/React.createElement("button", {
    className: "btn btn-icon btn-ghost",
    onClick: onLogout,
    title: "Sign out",
    style: {
      flexShrink: 0
    }
  }, /*#__PURE__*/React.createElement(Icon, {
    name: "signout",
    size: 14
  }))), /*#__PURE__*/React.createElement("div", {
    className: "sb-version",
    title: window.__HOMESLICE_CONFIG?.commit || 'none'
  }, window.__HOMESLICE_CONFIG?.version || 'dev')));
}
function NavLink(n, active, onNav, counts) {
  return /*#__PURE__*/React.createElement("button", {
    key: n.id,
    className: `sb-link ${active === n.id ? 'active' : ''}`,
    onClick: () => onNav(n.id)
  }, /*#__PURE__*/React.createElement("span", {
    className: "icn"
  }, /*#__PURE__*/React.createElement(Icon, {
    name: n.icon
  })), n.label, n.countKey && counts[n.countKey] != null && /*#__PURE__*/React.createElement("span", {
    className: "count"
  }, counts[n.countKey]));
}

// ── Shell ──────────────────────────────────────────────────────────────

function PageHead({
  crumbs,
  title,
  sub,
  actions
}) {
  return /*#__PURE__*/React.createElement("div", {
    className: "page-head"
  }, /*#__PURE__*/React.createElement("div", null, crumbs && /*#__PURE__*/React.createElement("div", {
    className: "crumbs"
  }, crumbs.map((c, i) => /*#__PURE__*/React.createElement(React.Fragment, {
    key: i
  }, i > 0 && ' / ', /*#__PURE__*/React.createElement("span", null, c)))), /*#__PURE__*/React.createElement("h1", {
    className: "page-title"
  }, title), sub && /*#__PURE__*/React.createElement("p", {
    className: "page-sub"
  }, sub)), actions && /*#__PURE__*/React.createElement("div", {
    className: "head-actions"
  }, actions));
}
function Toolbar({
  search,
  onSearch,
  placeholder,
  filters = [],
  activeFilter,
  onFilter,
  children
}) {
  return /*#__PURE__*/React.createElement("div", {
    className: "toolbar"
  }, /*#__PURE__*/React.createElement("div", {
    className: "search"
  }, /*#__PURE__*/React.createElement("span", {
    className: "icn"
  }, /*#__PURE__*/React.createElement(Icon, {
    name: "search",
    size: 14
  })), /*#__PURE__*/React.createElement("input", {
    value: search,
    onChange: e => onSearch(e.target.value),
    placeholder: placeholder
  })), filters.map(f => {
    const value = typeof f === 'string' ? f : f.value;
    const label = typeof f === 'string' ? f : f.label;
    const mobile = typeof f === 'string' ? f : f.mobile || f.label;
    return /*#__PURE__*/React.createElement("button", {
      key: value,
      className: `filter-chip ${activeFilter === value ? 'active' : ''}`,
      onClick: () => onFilter(activeFilter === value ? null : value)
    }, activeFilter === value && /*#__PURE__*/React.createElement(Icon, {
      name: "check",
      size: 11
    }), /*#__PURE__*/React.createElement("span", {
      className: "env-full"
    }, label), /*#__PURE__*/React.createElement("span", {
      className: "env-short"
    }, mobile));
  }), /*#__PURE__*/React.createElement("div", {
    style: {
      flex: 1
    }
  }), children);
}

// ── API helpers ────────────────────────────────────────────────────────

let _onUnauthorized = null;
async function api(token, method, path, body, {
  rawText = false
} = {}) {
  const opts = {
    method,
    headers: {}
  };
  if (token?.data?.access_token) opts.headers['Authorization'] = 'Bearer ' + token.data.access_token;
  if (body) {
    if (body instanceof FormData) {
      opts.body = body;
      // Let the browser set the multipart boundary.
    } else {
      opts.headers['Content-Type'] = 'application/json';
      opts.body = JSON.stringify(body);
    }
  }
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
  if (r.status === 401) {
    _onUnauthorized?.();
    throw new Error('Session expired');
  }
  if (!r.ok) {
    const t = await r.text().catch(() => '');
    throw new Error(`${r.status}: ${t || r.statusText}`);
  }
  if (r.status === 204) return null;
  return rawText ? r.text() : r.json();
}
const copyText = async (text, toast) => {
  try {
    await navigator.clipboard.writeText(text);
    toast('Copied to clipboard');
  } catch {
    toast('Failed to copy', true);
  }
};
function serviceInstructions(service) {
  if (service.descriptor?.type === "ssh") {
    return `  - ${service.name} (see the SSH guide in the 'freshbreath' skill)`;
  } else if (service.descriptor?.type === "tasks") {
    return `  - ${service.name} (see the tasks guide in the 'freshbreath' skill)`;
  } else if (service.descriptor?.type === "virtual") {
    return `  - ${service.name} (MCP): "${service.url}"`;
  }
  return `  - ${service.name} (${service.descriptor?.type?.toLocaleUpperCase()}): "${service.url}"`;
}
function hostRoute(app) {
  if (app.url && !app.url.includes('://')) return '/' + app.url.replace(/^\//, '');
  const slug = (app.name || '').toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '');
  return '/' + slug;
}

// An app is hosted when any deployment slot has content: the dev upload or a
// staging/production deploy. Slots live at <route>@dev|@staging|@prod; the
// bare route serves the app's default environment.
const isHosted = a => !!(a.details?.last_uploaded || a.details?.last_deployed_staging || a.details?.last_deployed_production);
function buildPrompt(app, appServices) {
  const fbURL = window.__HOMESLICE_CONFIG?.apiBase || window.location.origin;
  const serviceLines = appServices.length ? "\nIntegrations: (be sure to use any URLs exactly)\n" + appServices.map(serviceInstructions).join('\n') : '';
  return `Use the 'freshbreath' skill to add integrations to this app.\n\nSettings:\n  App nonce: ${app.nonce}\n  Fresh Breath URL: ${fbURL}\n${serviceLines}`;
}

// ── Sections ───────────────────────────────────────────────────────────

function Overview({
  users,
  apps,
  services,
  audit
}) {
  const activeUsers = users.filter(u => u.status === 'Active').length;
  const prodApps = apps.filter(a => a.details?.last_deployed_production).length;
  return /*#__PURE__*/React.createElement(React.Fragment, null, /*#__PURE__*/React.createElement(PageHead, {
    crumbs: ['Fresh Breath'],
    title: "Overview",
    sub: "A snapshot of your workspace."
  }), /*#__PURE__*/React.createElement("div", {
    className: "stats"
  }, /*#__PURE__*/React.createElement("div", {
    className: "stat"
  }, /*#__PURE__*/React.createElement("span", {
    className: "lbl"
  }, "Applications"), /*#__PURE__*/React.createElement("span", {
    className: "val"
  }, apps.length), /*#__PURE__*/React.createElement("span", {
    className: "sub"
  }, prodApps, " in production")), /*#__PURE__*/React.createElement("div", {
    className: "stat"
  }, /*#__PURE__*/React.createElement("span", {
    className: "lbl"
  }, "Services"), /*#__PURE__*/React.createElement("span", {
    className: "val"
  }, services.length), /*#__PURE__*/React.createElement("span", {
    className: "sub"
  }, "registered providers")), /*#__PURE__*/React.createElement("div", {
    className: "stat"
  }, /*#__PURE__*/React.createElement("span", {
    className: "lbl"
  }, "Recent events"), /*#__PURE__*/React.createElement("span", {
    className: "val"
  }, Math.min(audit.length, 10)), /*#__PURE__*/React.createElement("span", {
    className: "sub"
  }, "last ", Math.min(audit.length, 100), " records")), /*#__PURE__*/React.createElement("div", {
    className: "stat"
  }, /*#__PURE__*/React.createElement("span", {
    className: "lbl"
  }, "Users"), /*#__PURE__*/React.createElement("span", {
    className: "val"
  }, users.length), /*#__PURE__*/React.createElement("span", {
    className: "sub"
  }, activeUsers, " active \xB7 ", users.length - activeUsers, " other"))), /*#__PURE__*/React.createElement("div", {
    className: "overview-grid"
  }, /*#__PURE__*/React.createElement("div", null, /*#__PURE__*/React.createElement("h3", {
    style: {
      margin: '0 0 12px',
      fontSize: 14,
      fontWeight: 500
    }
  }, "Recent activity"), /*#__PURE__*/React.createElement("div", {
    className: "table-wrap"
  }, /*#__PURE__*/React.createElement("div", {
    style: {
      padding: '8px 16px'
    }
  }, audit.length === 0 ? /*#__PURE__*/React.createElement("div", {
    className: "empty",
    style: {
      padding: '24px 0'
    }
  }, /*#__PURE__*/React.createElement("b", null, "No recent activity."), /*#__PURE__*/React.createElement("br", null), "Events will appear here as users interact with services.") : /*#__PURE__*/React.createElement("div", {
    className: "timeline"
  }, audit.slice(0, 6).map(a => {
    const ai = actionIcon(a.action);
    return /*#__PURE__*/React.createElement("div", {
      key: a.id,
      className: "tl-row"
    }, /*#__PURE__*/React.createElement("span", {
      className: "tl-when"
    }, fmtAuditTime(a.when)), /*#__PURE__*/React.createElement("span", {
      className: `tl-icn tone-${ai.tone}`
    }, /*#__PURE__*/React.createElement(Icon, {
      name: ai.icon,
      size: 14
    })), /*#__PURE__*/React.createElement("div", {
      className: "tl-body"
    }, /*#__PURE__*/React.createElement("div", null, /*#__PURE__*/React.createElement("b", null, a.actor), " ", /*#__PURE__*/React.createElement("span", {
      className: "muted"
    }, a.action)), /*#__PURE__*/React.createElement("div", {
      className: "target"
    }, a.target)));
  }))))), /*#__PURE__*/React.createElement("div", null, /*#__PURE__*/React.createElement("h3", {
    style: {
      margin: '0 0 12px',
      fontSize: 14,
      fontWeight: 500
    }
  }, "Hosted Apps"), /*#__PURE__*/React.createElement("div", {
    className: "table-wrap",
    style: {
      padding: 4
    }
  }, apps.filter(isHosted).length === 0 ? /*#__PURE__*/React.createElement("div", {
    className: "empty",
    style: {
      padding: '24px 16px'
    }
  }, /*#__PURE__*/React.createElement("b", null, "No hosted apps yet."), /*#__PURE__*/React.createElement("br", null), "Upload web content to an app to make it reachable.") : apps.filter(isHosted).slice(0, 6).map((a, i, arr) => /*#__PURE__*/React.createElement("div", {
    key: a.nonce,
    style: {
      padding: '12px 16px',
      display: 'flex',
      alignItems: 'center',
      gap: 16,
      borderBottom: i < arr.length - 1 ? '1px solid var(--line-soft)' : 0
    }
  }, /*#__PURE__*/React.createElement("div", {
    style: {
      flex: 1,
      minWidth: 0
    }
  }, /*#__PURE__*/React.createElement("a", {
    className: "mono hosted-app-link",
    href: hostRoute(a),
    target: "_blank",
    rel: "noopener noreferrer"
  }, a.name), /*#__PURE__*/React.createElement("div", {
    style: {
      fontSize: 11.5,
      color: 'var(--ink-3)'
    }
  }, a.owner_name || 'No owner')), /*#__PURE__*/React.createElement(Badge, {
    tone: envTone(a.environment)
  }, /*#__PURE__*/React.createElement("span", {
    className: "env-full"
  }, a.environment || '—'), /*#__PURE__*/React.createElement("span", {
    className: "env-short"
  }, envShort(a.environment)))))))));
}

// ── Users ──────────────────────────────────────────────────────────────

function UsersView({
  token,
  users,
  apps,
  onRefresh
}) {
  const [q, setQ] = useState('');
  const [filter, setFilter] = useState(null);
  const [editing, setEditing] = useState(null);
  const toast = useToast();
  const filtered = users.filter(u => {
    if (q && !`${u.name} ${u.email}`.toLowerCase().includes(q.toLowerCase())) return false;
    if (filter && u.status !== filter) return false;
    return true;
  });
  const remove = async id => {
    if (!confirm('Delete this user?')) return;
    try {
      await api(token, 'DELETE', '/api/users/' + id);
      toast('User deleted');
      onRefresh();
    } catch (e) {
      toast(e.message, true);
    }
  };
  return /*#__PURE__*/React.createElement(React.Fragment, null, /*#__PURE__*/React.createElement(PageHead, {
    crumbs: ['Workspace', 'Users'],
    title: "Users",
    sub: "Say who can manage apps and services and their permissions.",
    actions: /*#__PURE__*/React.createElement("button", {
      className: "btn btn-primary",
      onClick: () => setEditing('new')
    }, /*#__PURE__*/React.createElement(Icon, {
      name: "plus",
      size: 14
    }), " New user")
  }), /*#__PURE__*/React.createElement(Toolbar, {
    search: q,
    onSearch: setQ,
    placeholder: "Search by name or email\u2026",
    filters: ['Active', 'Invited', 'Suspended'],
    activeFilter: filter,
    onFilter: setFilter
  }), /*#__PURE__*/React.createElement("div", {
    className: "table-wrap"
  }, /*#__PURE__*/React.createElement("table", {
    className: "tbl",
    "data-mobile": true
  }, /*#__PURE__*/React.createElement("thead", null, /*#__PURE__*/React.createElement("tr", null, /*#__PURE__*/React.createElement("th", {
    style: {
      width: '28%'
    }
  }, "Name"), /*#__PURE__*/React.createElement("th", null, "Role"), /*#__PURE__*/React.createElement("th", null, "Status"), /*#__PURE__*/React.createElement("th", null, "Apps"), /*#__PURE__*/React.createElement("th", null, "Last seen"), /*#__PURE__*/React.createElement("th", {
    style: {
      width: 80
    }
  }))), /*#__PURE__*/React.createElement("tbody", null, filtered.map(u => /*#__PURE__*/React.createElement("tr", {
    key: u.id
  }, /*#__PURE__*/React.createElement("td", {
    "data-col": "identity"
  }, /*#__PURE__*/React.createElement("div", {
    className: "user-cell"
  }, /*#__PURE__*/React.createElement(Avatar, {
    name: u.name
  }), /*#__PURE__*/React.createElement("div", {
    className: "meta"
  }, /*#__PURE__*/React.createElement("b", null, u.name), /*#__PURE__*/React.createElement("span", null, u.email)))), /*#__PURE__*/React.createElement("td", {
    "data-col": "detail"
  }, /*#__PURE__*/React.createElement(Badge, {
    tone: roleTone(u.role)
  }, u.role)), /*#__PURE__*/React.createElement("td", {
    "data-col": "badge"
  }, /*#__PURE__*/React.createElement(Badge, {
    tone: statusTone(u.status)
  }, u.status)), /*#__PURE__*/React.createElement("td", {
    "data-col": "detail"
  }, /*#__PURE__*/React.createElement(UserAppTags, {
    apps: u.apps,
    appList: apps
  })), /*#__PURE__*/React.createElement("td", {
    "data-col": "detail",
    className: "muted"
  }, fmtAuditTime(u.last_seen)), /*#__PURE__*/React.createElement("td", {
    "data-col": "actions"
  }, /*#__PURE__*/React.createElement("div", {
    className: "row-actions"
  }, /*#__PURE__*/React.createElement("button", {
    className: "btn btn-icon btn-ghost",
    onClick: () => setEditing(u),
    title: "Edit"
  }, /*#__PURE__*/React.createElement(Icon, {
    name: "edit",
    size: 14
  })), /*#__PURE__*/React.createElement("button", {
    className: "btn btn-icon btn-ghost",
    onClick: () => remove(u.id),
    title: "Delete"
  }, /*#__PURE__*/React.createElement(Icon, {
    name: "trash",
    size: 14
  })))))))), filtered.length === 0 && /*#__PURE__*/React.createElement("div", {
    className: "empty"
  }, /*#__PURE__*/React.createElement("b", null, "No users match."), "Try a different search.")), /*#__PURE__*/React.createElement(UserDrawer, {
    user: editing,
    token: token,
    apps: apps,
    onClose: () => setEditing(null),
    onSaved: onRefresh
  }));
}
function UserDrawer({
  user,
  token,
  apps,
  onClose,
  onSaved
}) {
  const {
    user: actor,
    authRequired
  } = useAuth();
  const canManageSSH = !authRequired || actor && (actor.role === 'Superuser' || actor.role === 'Admin');
  const [form, setForm] = useState({
    name: '',
    email: '',
    role: 'Member',
    status: 'Active',
    apps: []
  });
  const [loading, setLoading] = useState(false);
  const [sshKey, setSSHKey] = useState(null);
  const [sshLoading, setSSHLoading] = useState(false);
  const [showSSHGen, setShowSSHGen] = useState(false);
  const [passphrase, setPassphrase] = useState('');
  const [passConfirm, setPassConfirm] = useState('');
  const toast = useToast();
  const isNew = user === 'new';
  const isEdit = user && user.id;
  useEffect(() => {
    if (isEdit) {
      setForm({
        name: user.name,
        email: user.email,
        role: user.role || 'Member',
        status: user.status || 'Active',
        apps: []
      });
      setLoading(true);
      api(token, 'GET', '/api/users/' + user.id + '/apps').then(d => {
        setForm(f => ({
          ...f,
          apps: d.apps || []
        }));
      }).catch(e => toast(e.message, true)).finally(() => setLoading(false));
      // Load SSH key status for admins
      if (canManageSSH) {
        setSSHLoading(true);
        api(token, 'GET', '/api/users/' + user.id + '/ssh-key').then(d => setSSHKey(d.ssh_key)).catch(() => setSSHKey(null)).finally(() => setSSHLoading(false));
      }
    } else {
      setForm({
        name: '',
        email: '',
        role: 'Member',
        status: 'Active',
        apps: []
      });
      setSSHKey(null);
    }
    setShowSSHGen(false);
    setSSHLoading(false);
    setPassphrase('');
    setPassConfirm('');
  }, [user]); // eslint-disable-line react-hooks/exhaustive-deps

  const save = async () => {
    try {
      let uid;
      if (isEdit) {
        await api(token, 'PUT', '/api/users/' + user.id, form);
        await api(token, 'PUT', '/api/users/' + user.id + '/apps', {
          apps: form.apps || []
        });
        toast('User updated');
      } else {
        const resp = await api(token, 'POST', '/api/users', form);
        uid = resp.id;
        await api(token, 'PUT', '/api/users/' + uid + '/apps', {
          apps: form.apps || []
        });
        toast('User created');
      }
      onClose();
      onSaved();
    } catch (e) {
      toast(e.message, true);
    }
  };
  return /*#__PURE__*/React.createElement(Drawer, {
    open: isNew || isEdit,
    title: isNew ? 'New user' : 'Edit user',
    onClose: onClose,
    footer: /*#__PURE__*/React.createElement(React.Fragment, null, /*#__PURE__*/React.createElement("button", {
      className: "btn btn-ghost",
      onClick: onClose
    }, "Cancel"), /*#__PURE__*/React.createElement("button", {
      className: "btn btn-primary",
      onClick: save
    }, isNew ? 'Create' : 'Save'))
  }, /*#__PURE__*/React.createElement("p", null, /*#__PURE__*/React.createElement("strong", null, "NOTE:"), " You don't need to create accounts for people who are just using the apps and logging in with their own creds! This is only for users who need to log in to this admin panel and manage apps and services."), /*#__PURE__*/React.createElement("div", {
    className: "field"
  }, /*#__PURE__*/React.createElement("label", null, "Name"), /*#__PURE__*/React.createElement("input", {
    className: "input",
    value: form.name,
    onChange: e => setForm(f => ({
      ...f,
      name: e.target.value
    })),
    placeholder: "Ada Lovelace"
  })), /*#__PURE__*/React.createElement("div", {
    className: "field"
  }, /*#__PURE__*/React.createElement("label", null, "Email"), /*#__PURE__*/React.createElement("input", {
    className: "input",
    value: form.email,
    onChange: e => setForm(f => ({
      ...f,
      email: e.target.value
    })),
    placeholder: "ada@company.com"
  })), /*#__PURE__*/React.createElement("div", {
    className: "field-row"
  }, /*#__PURE__*/React.createElement("div", {
    className: "field"
  }, /*#__PURE__*/React.createElement("label", null, "Role"), /*#__PURE__*/React.createElement("select", {
    className: "input",
    value: form.role,
    onChange: e => setForm(f => ({
      ...f,
      role: e.target.value
    }))
  }, /*#__PURE__*/React.createElement("option", null, "Superuser"), /*#__PURE__*/React.createElement("option", null, "Admin"), /*#__PURE__*/React.createElement("option", null, "Member"), /*#__PURE__*/React.createElement("option", null, "Read-only"))), /*#__PURE__*/React.createElement("div", {
    className: "field"
  }, /*#__PURE__*/React.createElement("label", null, "Status"), /*#__PURE__*/React.createElement("select", {
    className: "input",
    value: form.status,
    onChange: e => setForm(f => ({
      ...f,
      status: e.target.value
    }))
  }, /*#__PURE__*/React.createElement("option", null, "Active"), /*#__PURE__*/React.createElement("option", null, "Invited"), /*#__PURE__*/React.createElement("option", null, "Suspended")))), (isNew || isEdit) && /*#__PURE__*/React.createElement("div", {
    className: "field"
  }, /*#__PURE__*/React.createElement("label", null, "Assigned apps"), /*#__PURE__*/React.createElement("span", {
    className: "help"
  }, "Select apps this user can access."), isEdit && loading ? /*#__PURE__*/React.createElement("span", {
    className: "muted"
  }, "Loading\u2026") : /*#__PURE__*/React.createElement(MultiSelect, {
    options: apps.map(a => ({
      value: a.nonce,
      label: a.name
    })),
    value: form.apps,
    onChange: v => setForm(f => ({
      ...f,
      apps: v
    })),
    placeholder: "No apps"
  })), isEdit && canManageSSH && /*#__PURE__*/React.createElement(React.Fragment, null, /*#__PURE__*/React.createElement("div", {
    style: {
      marginTop: 20,
      borderTop: '1px solid var(--border)',
      paddingTop: 16
    }
  }, /*#__PURE__*/React.createElement("label", {
    style: {
      fontSize: 13,
      fontWeight: 600,
      color: 'var(--ink-2)',
      marginBottom: 12,
      display: 'block'
    }
  }, "SSH Key"), sshLoading ? /*#__PURE__*/React.createElement("span", {
    className: "muted"
  }, "Loading\u2026") : sshKey ? /*#__PURE__*/React.createElement(React.Fragment, null, /*#__PURE__*/React.createElement("div", {
    style: {
      marginBottom: 8
    }
  }, /*#__PURE__*/React.createElement(Badge, {
    tone: "green"
  }, "Active"), /*#__PURE__*/React.createElement("span", {
    className: "muted",
    style: {
      marginLeft: 8,
      fontSize: 13
    }
  }, sshKey.key_type?.toUpperCase(), " \xB7 ", sshKey.fingerprint)), /*#__PURE__*/React.createElement("div", {
    style: {
      display: 'flex',
      gap: 8,
      alignItems: 'center'
    }
  }, /*#__PURE__*/React.createElement("input", {
    className: "input mono",
    value: sshKey.public_key?.trim(),
    readOnly: true,
    style: {
      fontSize: 11
    }
  }), /*#__PURE__*/React.createElement("button", {
    className: "btn btn-ghost",
    onClick: () => copyText(sshKey.public_key?.trim(), toast)
  }, /*#__PURE__*/React.createElement(Icon, {
    name: "copy",
    size: 14
  }))), /*#__PURE__*/React.createElement("button", {
    className: "btn btn-ghost",
    style: {
      color: 'var(--red)',
      marginTop: 8
    },
    onClick: async () => {
      if (!confirm('Delete this user\'s SSH key? They\'ll need a new one to use SSH auth.')) return;
      try {
        await api(token, 'DELETE', '/api/users/' + user.id + '/ssh-key');
        setSSHKey(null);
        toast('SSH key deleted');
      } catch (e) {
        toast(e.message, true);
      }
    }
  }, "Delete key")) : /*#__PURE__*/React.createElement("button", {
    className: "btn btn-ghost",
    onClick: () => setShowSSHGen(true)
  }, /*#__PURE__*/React.createElement(Icon, {
    name: "lock",
    size: 14
  }), " Generate SSH Key")), showSSHGen && /*#__PURE__*/React.createElement("div", {
    className: "modal-overlay",
    onClick: () => {
      setShowSSHGen(false);
      setPassphrase('');
      setPassConfirm('');
    }
  }, /*#__PURE__*/React.createElement("div", {
    className: "modal",
    onClick: e => e.stopPropagation(),
    style: {
      maxWidth: 420
    }
  }, /*#__PURE__*/React.createElement("h3", {
    style: {
      marginBottom: 16
    }
  }, "Generate SSH Key"), /*#__PURE__*/React.createElement("p", {
    className: "muted",
    style: {
      fontSize: 13,
      marginBottom: 16
    }
  }, "Choose a passphrase for ", user.name, "'s SSH key. They'll need it each time they log in via SSH."), /*#__PURE__*/React.createElement("div", {
    className: "field"
  }, /*#__PURE__*/React.createElement("label", null, "Passphrase"), /*#__PURE__*/React.createElement("input", {
    className: "input",
    type: "password",
    value: passphrase,
    onChange: e => setPassphrase(e.target.value),
    placeholder: "Min 8 characters",
    autoFocus: true
  })), /*#__PURE__*/React.createElement("div", {
    className: "field"
  }, /*#__PURE__*/React.createElement("label", null, "Confirm passphrase"), /*#__PURE__*/React.createElement("input", {
    className: "input",
    type: "password",
    value: passConfirm,
    onChange: e => setPassConfirm(e.target.value),
    placeholder: "Re-enter passphrase"
  })), /*#__PURE__*/React.createElement("div", {
    style: {
      display: 'flex',
      gap: 8,
      justifyContent: 'flex-end',
      marginTop: 20
    }
  }, /*#__PURE__*/React.createElement("button", {
    className: "btn btn-ghost",
    onClick: () => {
      setShowSSHGen(false);
      setPassphrase('');
      setPassConfirm('');
    }
  }, "Cancel"), /*#__PURE__*/React.createElement("button", {
    className: "btn btn-primary",
    disabled: passphrase.length < 8 || passphrase !== passConfirm,
    onClick: async () => {
      try {
        const d = await api(token, 'POST', '/api/users/' + user.id + '/ssh-key', {
          passphrase
        });
        setSSHKey(d.ssh_key);
        setShowSSHGen(false);
        setPassphrase('');
        setPassConfirm('');
        toast('SSH key generated');
      } catch (e) {
        toast(e.message, true);
      }
    }
  }, "Generate"))))));
}

// ── Apps ───────────────────────────────────────────────────────────────

function AppMemberTags({
  members,
  users
}) {
  if (!members || members.length === 0) return /*#__PURE__*/React.createElement("span", {
    className: "muted"
  }, "\u2014");
  const names = members.map(id => {
    const u = users.find(x => x.id === id);
    return u ? u.name : '?';
  }).slice(0, 3);
  const extra = members.length - names.length;
  return /*#__PURE__*/React.createElement("span", {
    className: "tags"
  }, names.map((n, i) => /*#__PURE__*/React.createElement("span", {
    key: i,
    className: "tag"
  }, n)), extra > 0 && /*#__PURE__*/React.createElement("span", {
    className: "tag muted"
  }, "+", extra));
}
function UserAppTags({
  apps,
  appList
}) {
  if (!apps || apps.length === 0) return /*#__PURE__*/React.createElement("span", {
    className: "muted"
  }, "\u2014");
  const names = apps.map(nonce => {
    const a = appList.find(x => x.nonce === nonce);
    return a ? a.name : '?';
  }).slice(0, 3);
  const extra = apps.length - names.length;
  return /*#__PURE__*/React.createElement("span", {
    className: "tags"
  }, names.map((n, i) => /*#__PURE__*/React.createElement("span", {
    key: i,
    className: "tag"
  }, n)), extra > 0 && /*#__PURE__*/React.createElement("span", {
    className: "tag muted"
  }, "+", extra));
}
function AppsView({
  token,
  apps,
  services,
  users,
  onRefresh
}) {
  const [q, setQ] = useState('');
  const [filter, setFilter] = useState(null);
  const [editing, setEditing] = useState(null);
  const toast = useToast();
  const filtered = apps.filter(a => {
    if (q && !`${a.name} ${a.owner_name || ''}`.toLowerCase().includes(q.toLowerCase())) return false;
    if (filter && a.environment !== filter) return false;
    return true;
  });
  const remove = async nonce => {
    if (!confirm('Delete this app?')) return;
    try {
      await api(token, 'DELETE', '/api/apps/' + nonce);
      toast('App deleted');
      onRefresh();
    } catch (e) {
      toast(e.message, true);
    }
  };
  const copyNonce = nonce => copyText(nonce, toast);
  const copyPrompt = async a => {
    try {
      const r = await api(token, 'GET', '/api/apps/' + a.nonce + '/services');
      const allowedIds = (r.services || []).filter(l => l.allowed).map(l => l.service_id);
      const appSvcs = services.filter(s => allowedIds.includes(s.id));
      copyText(buildPrompt(a, appSvcs), toast);
    } catch (e) {
      toast(e.message, true);
    }
  };
  return /*#__PURE__*/React.createElement(React.Fragment, null, /*#__PURE__*/React.createElement(PageHead, {
    crumbs: ['Workspace', 'Apps'],
    title: "Applications",
    sub: "Service consumers with managed access.",
    actions: /*#__PURE__*/React.createElement("button", {
      className: "btn btn-primary",
      onClick: () => setEditing('new')
    }, /*#__PURE__*/React.createElement(Icon, {
      name: "plus",
      size: 14
    }), " New app")
  }), /*#__PURE__*/React.createElement(Toolbar, {
    search: q,
    onSearch: setQ,
    placeholder: "Search apps\u2026",
    filters: [{
      value: 'Production',
      label: 'Production',
      mobile: 'Prod'
    }, {
      value: 'Staging',
      label: 'Staging',
      mobile: 'Staging'
    }, {
      value: 'Development',
      label: 'Development',
      mobile: 'Dev'
    }],
    activeFilter: filter,
    onFilter: setFilter
  }), /*#__PURE__*/React.createElement("div", {
    className: "table-wrap"
  }, /*#__PURE__*/React.createElement("table", {
    className: "tbl",
    "data-mobile": true
  }, /*#__PURE__*/React.createElement("thead", null, /*#__PURE__*/React.createElement("tr", null, /*#__PURE__*/React.createElement("th", {
    style: {
      width: '26%'
    }
  }, "Name"), /*#__PURE__*/React.createElement("th", null, "Environment"), /*#__PURE__*/React.createElement("th", null, "Owner"), /*#__PURE__*/React.createElement("th", null, "Members"), /*#__PURE__*/React.createElement("th", null, "Services"), /*#__PURE__*/React.createElement("th", {
    style: {
      width: 80
    }
  }))), /*#__PURE__*/React.createElement("tbody", null, filtered.map(a => /*#__PURE__*/React.createElement("tr", {
    key: a.nonce
  }, /*#__PURE__*/React.createElement("td", {
    "data-col": "identity"
  }, /*#__PURE__*/React.createElement("div", {
    className: "user-cell"
  }, /*#__PURE__*/React.createElement("div", {
    className: "avatar",
    style: {
      width: 32,
      height: 32,
      borderRadius: 8,
      background: `linear-gradient(135deg,oklch(0.85 0.06 ${hashHue(a.name)}),oklch(0.55 0.1 ${(hashHue(a.name) + 30) % 360}))`,
      fontSize: 13,
      color: 'white',
      display: 'grid',
      placeItems: 'center'
    }
  }, a.name?.[0]), /*#__PURE__*/React.createElement("div", {
    className: "meta"
  }, /*#__PURE__*/React.createElement("div", {
    style: {
      display: 'flex',
      alignItems: 'center',
      gap: 6
    }
  }, /*#__PURE__*/React.createElement("b", null, a.name), isHosted(a) && /*#__PURE__*/React.createElement("span", {
    style: {
      fontSize: 10,
      padding: '1px 5px',
      borderRadius: 4,
      background: 'oklch(from var(--tone-green) var(--tone-bg-l) calc(c*.25) h)',
      color: 'oklch(from var(--tone-green) var(--tone-fg-l) calc(c*.67) h)',
      border: '1px solid oklch(from var(--tone-green) var(--tone-border-l) calc(c*.33) h)',
      lineHeight: 1.4
    }
  }, "hosted")), /*#__PURE__*/React.createElement("span", {
    className: "mono",
    style: {
      cursor: 'pointer'
    },
    onClick: () => copyNonce(a.nonce),
    title: `${a.nonce} — click to copy`
  }, a.nonce.slice(0, 8), "\u2026 ", /*#__PURE__*/React.createElement("span", {
    style: {
      opacity: 0.6,
      verticalAlign: 'middle',
      marginLeft: 2
    }
  }, /*#__PURE__*/React.createElement(Icon, {
    name: "copy",
    size: 12
  })))))), /*#__PURE__*/React.createElement("td", {
    "data-col": "badge"
  }, /*#__PURE__*/React.createElement(Badge, {
    tone: envTone(a.environment)
  }, /*#__PURE__*/React.createElement("span", {
    className: "env-full"
  }, a.environment || '—'), /*#__PURE__*/React.createElement("span", {
    className: "env-short"
  }, envShort(a.environment)))), /*#__PURE__*/React.createElement("td", {
    "data-col": "detail"
  }, a.owner_name || /*#__PURE__*/React.createElement("span", {
    className: "muted"
  }, "\u2014")), /*#__PURE__*/React.createElement("td", {
    "data-col": "detail",
    className: "mono"
  }, a.member_count ?? 0), /*#__PURE__*/React.createElement("td", {
    "data-col": "detail",
    className: "mono"
  }, a.service_count ?? 0), /*#__PURE__*/React.createElement("td", {
    "data-col": "actions"
  }, /*#__PURE__*/React.createElement("div", {
    className: "row-actions"
  }, /*#__PURE__*/React.createElement("button", {
    className: "btn btn-icon btn-ghost",
    onClick: () => copyPrompt(a),
    title: "Copy setup prompt"
  }, /*#__PURE__*/React.createElement(Icon, {
    name: "sparkle",
    size: 14
  })), /*#__PURE__*/React.createElement("button", {
    className: "btn btn-icon btn-ghost",
    onClick: () => setEditing(a),
    title: "Edit"
  }, /*#__PURE__*/React.createElement(Icon, {
    name: "edit",
    size: 14
  })), /*#__PURE__*/React.createElement("button", {
    className: "btn btn-icon btn-ghost",
    onClick: () => remove(a.nonce),
    title: "Delete"
  }, /*#__PURE__*/React.createElement(Icon, {
    name: "trash",
    size: 14
  })))))))), filtered.length === 0 && /*#__PURE__*/React.createElement("div", {
    className: "empty"
  }, /*#__PURE__*/React.createElement("b", null, "No apps found."))), /*#__PURE__*/React.createElement(AppDrawer, {
    token: token,
    app: editing,
    services: services,
    users: users,
    apps: apps,
    onClose: () => setEditing(null),
    onSaved: onRefresh
  }));
}
function HostUpload({
  token,
  app,
  onRefresh
}) {
  const [hosted, setHosted] = useState(!!app.details?.last_uploaded);
  const [uploadedAt, setUploadedAt] = useState(app.details?.last_uploaded || null);
  const [deployed, setDeployed] = useState({
    staging: app.details?.last_deployed_staging || null,
    prod: app.details?.last_deployed_production || null
  });
  const [deploying, setDeploying] = useState(null);
  const [dragging, setDragging] = useState(false);
  const [uploading, setUploading] = useState(false);
  const inputRef = useRef(null);
  const toast = useToast();
  const route = hostRoute(app);
  const upload = async file => {
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
        headers: {
          'Authorization': 'Bearer ' + token?.data?.access_token
        },
        body: fd
      });
      if (!res.ok) throw new Error(await res.text());
      const now = new Date().toISOString();
      setHosted(true);
      setUploadedAt(now);
      toast('Hosted at ' + route);
      onRefresh();
    } catch (e) {
      toast(e.message, true);
    } finally {
      setUploading(false);
    }
  };
  const remove = async () => {
    try {
      const res = await fetch('/api/apps/' + app.nonce + '/web', {
        method: 'DELETE',
        headers: {
          'Authorization': 'Bearer ' + token?.data?.access_token
        }
      });
      if (!res.ok) throw new Error(await res.text());
      setHosted(false);
      setUploadedAt(null);
      toast('Hosting removed');
      onRefresh();
    } catch (e) {
      toast(e.message, true);
    }
  };
  const onDrop = e => {
    e.preventDefault();
    setDragging(false);
    upload(e.dataTransfer.files[0]);
  };

  // Deploys copy the current Development (web) folder into the target slot.
  const deploy = async target => {
    setDeploying(target);
    try {
      const res = await api(token, 'POST', '/api/apps/' + app.nonce + '/deploy', {
        target
      });
      setDeployed(d => ({
        ...d,
        [target]: new Date().toISOString()
      }));
      toast('Deployed to ' + (res.route || route + '@' + target));
      onRefresh();
    } catch (e) {
      toast(e.message, true);
    } finally {
      setDeploying(null);
    }
  };
  const slots = [{
    name: 'Development',
    suffix: '@dev',
    when: uploadedAt,
    verb: 'uploaded',
    empty: 'not uploaded'
  }, {
    name: 'Staging',
    suffix: '@staging',
    when: deployed.staging,
    verb: 'deployed',
    empty: 'not deployed',
    target: 'staging'
  }, {
    name: 'Production',
    suffix: '@prod',
    when: deployed.prod,
    verb: 'deployed',
    empty: 'not deployed',
    target: 'prod'
  }];
  return /*#__PURE__*/React.createElement("div", {
    className: "field"
  }, /*#__PURE__*/React.createElement("label", null, "Web hosting"), hosted ? /*#__PURE__*/React.createElement("div", {
    style: {
      display: 'flex',
      alignItems: 'center',
      gap: 8,
      flexWrap: 'wrap',
      marginBottom: 4
    }
  }, /*#__PURE__*/React.createElement(Badge, {
    tone: "green"
  }, "Hosted"), /*#__PURE__*/React.createElement("span", {
    className: "mono",
    style: {
      fontSize: 13
    }
  }, route), /*#__PURE__*/React.createElement("span", {
    className: "muted",
    style: {
      fontSize: 12,
      flex: 1
    }
  }, "uploaded ", fmtAuditTime(uploadedAt)), /*#__PURE__*/React.createElement("button", {
    className: "btn btn-ghost",
    style: {
      padding: '2px 8px',
      fontSize: 12,
      color: 'var(--tone-red)'
    },
    onClick: remove
  }, "Remove")) : /*#__PURE__*/React.createElement("span", {
    className: "help"
  }, "Upload an HTML file or a ZIP containing your app."), /*#__PURE__*/React.createElement("div", {
    className: 'drop-zone' + (dragging ? ' drop-zone-active' : ''),
    onDragOver: e => {
      e.preventDefault();
      setDragging(true);
    },
    onDragLeave: () => setDragging(false),
    onDrop: onDrop,
    onClick: () => inputRef.current?.click()
  }, uploading ? 'Uploading…' : hosted ? 'Drop to replace' : 'Drop .html or .zip here, or click to browse', /*#__PURE__*/React.createElement("input", {
    ref: inputRef,
    type: "file",
    accept: ".html,.zip",
    style: {
      display: 'none'
    },
    onChange: e => upload(e.target.files[0])
  })), /*#__PURE__*/React.createElement("div", {
    style: {
      marginTop: 16
    }
  }, /*#__PURE__*/React.createElement("div", {
    style: {
      fontSize: 13,
      fontWeight: 500,
      marginBottom: 4
    }
  }, "Deployment slots"), /*#__PURE__*/React.createElement("span", {
    className: "help",
    style: {
      display: 'block',
      marginBottom: 10
    }
  }, "Deploying copies the Development folder into a slot. The bare ", route, " URL serves the default environment (above); each slot also has its own URL."), /*#__PURE__*/React.createElement("div", {
    style: {
      display: 'flex',
      flexDirection: 'column',
      gap: 8
    }
  }, slots.map(s => /*#__PURE__*/React.createElement("div", {
    key: s.suffix,
    style: {
      display: 'flex',
      alignItems: 'center',
      gap: 8,
      flexWrap: 'wrap'
    }
  }, /*#__PURE__*/React.createElement(Badge, {
    tone: envTone(s.name)
  }, /*#__PURE__*/React.createElement("span", {
    className: "env-full"
  }, s.name), /*#__PURE__*/React.createElement("span", {
    className: "env-short"
  }, envShort(s.name))), s.when ? /*#__PURE__*/React.createElement("a", {
    className: "mono hosted-app-link",
    style: {
      fontSize: 12.5
    },
    href: route + s.suffix,
    target: "_blank",
    rel: "noopener noreferrer"
  }, route + s.suffix) : /*#__PURE__*/React.createElement("span", {
    className: "mono muted",
    style: {
      fontSize: 12.5
    }
  }, route + s.suffix), /*#__PURE__*/React.createElement("span", {
    className: "muted",
    style: {
      fontSize: 12,
      flex: 1
    }
  }, s.when ? s.verb + ' ' + fmtAuditTime(s.when) : s.empty), s.target && /*#__PURE__*/React.createElement("button", {
    className: "btn btn-ghost",
    style: {
      padding: '2px 8px',
      fontSize: 12
    },
    disabled: !hosted || deploying === s.target,
    title: hosted ? 'Copy Development into ' + s.name : 'Upload to Development first',
    onClick: () => deploy(s.target)
  }, deploying === s.target ? 'Deploying…' : 'Deploy'))))));
}
function AppDrawer({
  token,
  app,
  services,
  users,
  apps,
  onClose,
  onSaved
}) {
  const [form, setForm] = useState({
    name: '',
    environment: 'Development',
    url: '',
    owner_id: '',
    services: [],
    members: []
  });
  const [loading, setLoading] = useState(false);
  const toast = useToast();
  const isNew = app === 'new';
  const isEdit = app && app.nonce;
  useEffect(() => {
    if (isEdit) {
      setForm({
        name: app.name || '',
        environment: app.environment || 'Development',
        url: app.url,
        owner_id: app.owner_id ? String(app.owner_id) : '',
        services: [],
        members: []
      });
      setLoading(true);
      Promise.all([api(token, 'GET', '/api/apps/' + app.nonce + '/services'), api(token, 'GET', '/api/apps/' + app.nonce + '/members')]).then(([svcs, mems]) => {
        const allowed = (svcs.services || []).filter(l => l.allowed).map(l => l.service_id);
        setForm(f => ({
          ...f,
          services: allowed,
          members: mems.members || []
        }));
      }).catch(e => toast(e.message, true)).finally(() => setLoading(false));
    } else {
      setForm({
        name: '',
        environment: 'Development',
        url: '',
        owner_id: '',
        services: [],
        members: []
      });
    }
  }, [app]); // eslint-disable-line react-hooks/exhaustive-deps

  const handleServicesChange = newServices => {
    setForm(f => ({
      ...f,
      services: newServices
    }));
  };
  const save = async () => {
    const payload = {
      name: form.name,
      environment: form.environment,
      url: form.url,
      owner_id: form.owner_id ? Number(form.owner_id) : null
    };
    try {
      let nonce;
      if (isEdit) {
        await api(token, 'PUT', '/api/apps/' + app.nonce, payload);
        await api(token, 'PUT', '/api/apps/' + app.nonce + '/members', {
          members: form.members || []
        });
        await api(token, 'PUT', '/api/apps/' + app.nonce + '/services', {
          services: form.services || []
        });
        toast('App updated');
      } else {
        const resp = await api(token, 'POST', '/api/apps', payload);
        nonce = resp.nonce;
        await api(token, 'PUT', '/api/apps/' + nonce + '/members', {
          members: form.members || []
        });
        await api(token, 'PUT', '/api/apps/' + nonce + '/services', {
          services: form.services || []
        });
        toast('App created');
      }
      onClose();
      onSaved();
    } catch (e) {
      toast(e.message, true);
    }
  };
  const copyNonce = nonce => copyText(nonce, toast);
  const setupPrompt = isEdit && !loading ? buildPrompt(app, services.filter(s => form.services.includes(s.id))) : null;
  return /*#__PURE__*/React.createElement(Drawer, {
    open: isNew || isEdit,
    title: isNew ? 'New application' : 'Edit application',
    onClose: onClose,
    footer: /*#__PURE__*/React.createElement(React.Fragment, null, /*#__PURE__*/React.createElement("button", {
      className: "btn btn-ghost",
      onClick: onClose
    }, "Cancel"), /*#__PURE__*/React.createElement("button", {
      className: "btn btn-primary",
      onClick: save
    }, isNew ? 'Create' : 'Save'))
  }, /*#__PURE__*/React.createElement("div", {
    className: "field"
  }, /*#__PURE__*/React.createElement("label", null, "Name"), /*#__PURE__*/React.createElement("input", {
    className: "input",
    value: form.name,
    onChange: e => setForm(f => ({
      ...f,
      name: e.target.value
    })),
    placeholder: "My App"
  })), isEdit && app.nonce && /*#__PURE__*/React.createElement("div", {
    className: "field"
  }, /*#__PURE__*/React.createElement("label", null, "Nonce"), /*#__PURE__*/React.createElement("div", {
    className: "input",
    style: {
      display: 'flex',
      alignItems: 'center',
      justifyContent: 'space-between',
      gap: 12
    }
  }, /*#__PURE__*/React.createElement("span", {
    className: "mono"
  }, app.nonce), /*#__PURE__*/React.createElement("button", {
    className: "btn btn-ghost",
    style: {
      padding: '2px 8px',
      fontSize: 12
    },
    onClick: () => copyNonce(app.nonce)
  }, /*#__PURE__*/React.createElement(Icon, {
    name: "copy",
    size: 14
  }), " Copy"))), /*#__PURE__*/React.createElement("div", {
    className: "field"
  }, /*#__PURE__*/React.createElement("label", null, "URL"), /*#__PURE__*/React.createElement("input", {
    className: "input",
    value: form.url,
    onChange: e => setForm(f => ({
      ...f,
      url: e.target.value
    })),
    placeholder: "https://hostname.com:port"
  })), /*#__PURE__*/React.createElement("div", {
    className: "field-row"
  }, /*#__PURE__*/React.createElement("div", {
    className: "field"
  }, /*#__PURE__*/React.createElement("label", null, "Default environment"), /*#__PURE__*/React.createElement("select", {
    className: "input",
    value: form.environment,
    onChange: e => setForm(f => ({
      ...f,
      environment: e.target.value
    }))
  }, /*#__PURE__*/React.createElement("option", null, "Production"), /*#__PURE__*/React.createElement("option", null, "Staging"), /*#__PURE__*/React.createElement("option", null, "Development")), /*#__PURE__*/React.createElement("span", {
    className: "help"
  }, "Which deployment slot the bare app URL serves.")), /*#__PURE__*/React.createElement("div", {
    className: "field"
  }, /*#__PURE__*/React.createElement("label", null, "Owner"), /*#__PURE__*/React.createElement("select", {
    className: "input",
    value: form.owner_id,
    onChange: e => setForm(f => ({
      ...f,
      owner_id: e.target.value
    }))
  }, /*#__PURE__*/React.createElement("option", {
    value: ""
  }, "No owner"), users.map(u => /*#__PURE__*/React.createElement("option", {
    key: u.id,
    value: u.id
  }, u.name))))), (isNew || isEdit) && /*#__PURE__*/React.createElement("div", {
    className: "field"
  }, /*#__PURE__*/React.createElement("label", null, "Members"), /*#__PURE__*/React.createElement("span", {
    className: "help"
  }, "Select which users are assigned to this app."), isEdit && loading ? /*#__PURE__*/React.createElement("span", {
    className: "muted"
  }, "Loading\u2026") : /*#__PURE__*/React.createElement(MultiSelect, {
    options: users.map(u => ({
      value: u.id,
      label: u.name
    })),
    value: form.members,
    onChange: v => setForm(f => ({
      ...f,
      members: v
    })),
    placeholder: "No members"
  })), (isNew || isEdit) && /*#__PURE__*/React.createElement("div", {
    className: "field"
  }, /*#__PURE__*/React.createElement("label", null, "Service access"), /*#__PURE__*/React.createElement("span", {
    className: "help"
  }, "Select which services this app can access."), isEdit && loading ? /*#__PURE__*/React.createElement("span", {
    className: "muted"
  }, "Loading\u2026") : /*#__PURE__*/React.createElement(MultiSelect, {
    options: services.map(s => ({
      value: s.id,
      label: s.name
    })),
    value: form.services,
    onChange: handleServicesChange,
    placeholder: "No service access"
  })), setupPrompt && /*#__PURE__*/React.createElement("div", {
    className: "field"
  }, /*#__PURE__*/React.createElement("label", null, "Setup prompt"), /*#__PURE__*/React.createElement("span", {
    className: "help"
  }, "Paste into Claude Code to wire up this app with the freshbreath skill."), /*#__PURE__*/React.createElement("div", {
    style: {
      position: 'relative'
    }
  }, /*#__PURE__*/React.createElement("textarea", {
    className: "input",
    readOnly: true,
    style: {
      fontFamily: 'var(--font-mono)',
      fontSize: 11,
      lineHeight: 1.6,
      resize: 'vertical',
      paddingRight: 38,
      width: '100%',
      fieldSizing: 'content'
    },
    value: setupPrompt,
    onClick: e => e.target.select()
  }), /*#__PURE__*/React.createElement("button", {
    className: "btn btn-ghost",
    style: {
      position: 'absolute',
      top: 8,
      right: 8,
      padding: '4px 6px'
    },
    title: "Copy prompt",
    onClick: () => copyText(setupPrompt, toast)
  }, /*#__PURE__*/React.createElement(Icon, {
    name: "copy",
    size: 13
  })))), isEdit && !loading && /*#__PURE__*/React.createElement(HostUpload, {
    token: token,
    app: app,
    onRefresh: onSaved
  }));
}

// ── Services ───────────────────────────────────────────────────────────

function ServicesView({
  token,
  services,
  onRefresh,
  onEditTools
}) {
  const [q, setQ] = useState('');
  const [editing, setEditing] = useState(null);
  const toast = useToast();
  const filtered = services.filter(s => {
    if (q && !`${s.name} ${s.url}`.toLowerCase().includes(q.toLowerCase())) return false;
    return true;
  });
  const remove = async id => {
    let apps = [];
    try {
      const r = await api(token, 'GET', '/api/services/' + id + '/apps');
      apps = r.apps || [];
    } catch (e) {/* ignore */}
    let msg = 'Delete this service?';
    if (apps.length > 0) {
      msg += `\n\nIt's used by ${apps.length} app${apps.length > 1 ? 's' : ''}:\n${apps.map(a => a.name).join(', ')}`;
    }
    if (!confirm(msg)) return;
    try {
      await api(token, 'DELETE', '/api/services/' + id);
      toast('Service deleted');
      onRefresh();
    } catch (e) {
      toast(e.message, true);
    }
  };
  return /*#__PURE__*/React.createElement(React.Fragment, null, /*#__PURE__*/React.createElement(PageHead, {
    crumbs: ['Workspace', 'Services'],
    title: "Services",
    sub: "Registered MCP, OAuth, API, and task providers.",
    actions: /*#__PURE__*/React.createElement("button", {
      className: "btn btn-primary",
      onClick: () => setEditing('new')
    }, /*#__PURE__*/React.createElement(Icon, {
      name: "plus",
      size: 14
    }), " New service")
  }), /*#__PURE__*/React.createElement(Toolbar, {
    search: q,
    onSearch: setQ,
    placeholder: "Search services\u2026"
  }), /*#__PURE__*/React.createElement("div", {
    className: "table-wrap"
  }, /*#__PURE__*/React.createElement("table", {
    className: "tbl",
    "data-mobile": true
  }, /*#__PURE__*/React.createElement("thead", null, /*#__PURE__*/React.createElement("tr", null, /*#__PURE__*/React.createElement("th", {
    style: {
      width: '25%'
    }
  }, "Name"), /*#__PURE__*/React.createElement("th", null, "URL"), /*#__PURE__*/React.createElement("th", null, "Type"), /*#__PURE__*/React.createElement("th", null, "Proxied"), /*#__PURE__*/React.createElement("th", {
    style: {
      width: 80
    }
  }))), /*#__PURE__*/React.createElement("tbody", null, filtered.map(s => /*#__PURE__*/React.createElement("tr", {
    key: s.id
  }, /*#__PURE__*/React.createElement("td", {
    "data-col": "identity"
  }, /*#__PURE__*/React.createElement("b", null, s.name), s.descriptor?.type === 'ssh' && /*#__PURE__*/React.createElement(Badge, {
    tone: "purple",
    style: {
      marginLeft: 6
    }
  }, "Built-in")), /*#__PURE__*/React.createElement("td", {
    "data-col": "url"
  }, /*#__PURE__*/React.createElement("span", {
    className: "mono",
    style: {
      fontSize: 12.5,
      color: 'var(--ink-3)',
      cursor: 'pointer'
    },
    onClick: () => copyText(s.url, toast),
    title: `${s.url} — click to copy`
  }, s.url.length > 48 ? s.url.slice(0, 48) + '…' : s.url, " ", /*#__PURE__*/React.createElement("span", {
    style: {
      opacity: 0.6,
      verticalAlign: 'middle',
      marginLeft: 2
    }
  }, /*#__PURE__*/React.createElement(Icon, {
    name: "copy",
    size: 12
  })))), /*#__PURE__*/React.createElement("td", {
    "data-col": "badge"
  }, /*#__PURE__*/React.createElement(Badge, {
    dot: false,
    tone: "gray"
  }, s.descriptor?.type?.toLocaleUpperCase() || '—')), /*#__PURE__*/React.createElement("td", {
    "data-col": "detail"
  }, s.descriptor?.proxied ? /*#__PURE__*/React.createElement(Badge, {
    tone: "blue"
  }, "Proxied") : /*#__PURE__*/React.createElement("span", {
    className: "muted"
  }, "\u2014")), /*#__PURE__*/React.createElement("td", {
    "data-col": "actions"
  }, /*#__PURE__*/React.createElement("div", {
    className: "row-actions"
  }, /*#__PURE__*/React.createElement("button", {
    className: "btn btn-icon btn-ghost",
    onClick: () => setEditing(s),
    title: "Edit"
  }, /*#__PURE__*/React.createElement(Icon, {
    name: "edit",
    size: 14
  })), s.descriptor?.type !== 'ssh' && /*#__PURE__*/React.createElement("button", {
    className: "btn btn-icon btn-ghost",
    onClick: () => remove(s.id),
    title: "Delete"
  }, /*#__PURE__*/React.createElement(Icon, {
    name: "trash",
    size: 14
  })))))))), filtered.length === 0 && /*#__PURE__*/React.createElement("div", {
    className: "empty"
  }, /*#__PURE__*/React.createElement("b", null, "No services found."))), /*#__PURE__*/React.createElement(ServiceDrawer, {
    token: token,
    services: services,
    service: editing,
    onClose: () => setEditing(null),
    onSaved: onRefresh,
    onEditTools: onEditTools
  }));
}
function ServiceDrawer({
  token,
  services,
  service,
  onClose,
  onSaved,
  onEditTools
}) {
  const [form, setForm] = useState({
    name: '',
    url: '',
    descriptor: {
      type: 'mcp',
      proxied: false
    }
  });
  const [tools, setTools] = useState([]);
  const [toolsLoading, setToolsLoading] = useState(false);
  const [toolsError, setToolsError] = useState('');
  const toast = useToast();
  const isNew = service === 'new';
  const isEdit = service && service.id;
  useEffect(() => {
    if (isEdit) setForm({
      name: service.name,
      url: service.url,
      descriptor: {
        ...service.descriptor
      }
    });else setForm({
      name: '',
      url: '',
      descriptor: {
        type: 'mcp',
        proxied: false
      }
    });
  }, [service]);
  useEffect(() => {
    if (!isEdit) {
      setTools([]);
      setToolsError('');
      return;
    }
    const type = service.descriptor?.type;
    if (type !== 'tasks' && type !== 'virtual') {
      setTools([]);
      setToolsError('');
      return;
    }
    let cancelled = false;
    setToolsLoading(true);
    setToolsError('');
    api(token, 'GET', '/api/services/' + service.id + '/tools').then(r => {
      if (!cancelled) {
        setTools(r.tools || []);
        setToolsError('');
      }
    }).catch(e => {
      if (!cancelled) {
        setTools([]);
        setToolsError(e.message);
      }
    }).finally(() => {
      if (!cancelled) setToolsLoading(false);
    });
    return () => {
      cancelled = true;
    };
  }, [service, token]);
  const updDesc = (k, v) => setForm(f => ({
    ...f,
    descriptor: {
      ...f.descriptor,
      [k]: v
    }
  }));

  // When switching type, clear fields that don't apply
  const setType = t => {
    const d = {
      ...form.descriptor,
      type: t
    };
    if (t === 'tasks') {
      delete d.auth;
      delete d.api_key;
      delete d.header;
      delete d.proxied;
      delete d.client_id;
      delete d.client_secret;
      delete d.oauth_url;
      delete d.scopes;
      delete d.userinfo_url;
      delete d.userinfo_emails_url;
    } else if (t === 'virtual') {
      delete d.proxied;
      delete d.auth_service_id;
      delete d.userinfo_url;
      delete d.userinfo_emails_url;
    } else {
      delete d.auth_service_id;
    }
    setForm(f => ({
      ...f,
      descriptor: d
    }));
  };
  const save = async () => {
    try {
      // Strip auth_service_id if not tasks type
      const payload = {
        ...form
      };
      if (payload.descriptor.type !== 'tasks' && payload.descriptor.auth_service_id) {
        const d = {
          ...payload.descriptor
        };
        delete d.auth_service_id;
        payload.descriptor = d;
      }
      // Virtual services don't need a URL — server generates /mcp/{slug}
      if (payload.descriptor.type === 'virtual') {
        payload.url = '';
      }
      if (isEdit) {
        await api(token, 'PUT', '/api/services/' + service.id, payload);
        toast('Service updated');
      } else {
        await api(token, 'POST', '/api/services', payload);
        toast('Service created');
      }
      onClose();
      onSaved();
    } catch (e) {
      toast(e.message, true);
    }
  };
  const isSSH = isEdit && service.descriptor?.type === 'ssh';
  const isTasks = form.descriptor.type === 'tasks';
  const isVirtual = form.descriptor.type === 'virtual';

  // Auth service options for tasks: OIDC services + built-in SSH
  const oidcSvc = services.filter(s => s.descriptor?.type === 'oidc');
  const sshSvc = services.find(s => s.descriptor?.type === 'ssh');
  const authSvcOptions = [...oidcSvc.map(s => ({
    id: String(s.id),
    label: s.name,
    type: 'OIDC'
  })), ...(sshSvc ? [{
    id: String(sshSvc.id),
    label: 'SSH Key',
    type: 'SSH'
  }] : [])];
  return /*#__PURE__*/React.createElement(Drawer, {
    open: isNew || isEdit,
    title: isNew ? 'New service' : 'Edit service',
    onClose: onClose,
    footer: /*#__PURE__*/React.createElement(React.Fragment, null, /*#__PURE__*/React.createElement("button", {
      className: "btn btn-ghost",
      onClick: onClose
    }, "Cancel"), /*#__PURE__*/React.createElement("button", {
      className: "btn btn-primary",
      onClick: save
    }, isNew ? 'Create' : 'Save'))
  }, /*#__PURE__*/React.createElement("div", {
    className: "field"
  }, /*#__PURE__*/React.createElement("label", null, "Name"), /*#__PURE__*/React.createElement("input", {
    className: "input",
    value: form.name,
    onChange: e => setForm(f => ({
      ...f,
      name: e.target.value
    })),
    disabled: isSSH
  })), !isTasks && !isVirtual && /*#__PURE__*/React.createElement("div", {
    className: "field"
  }, /*#__PURE__*/React.createElement("label", null, "URL"), /*#__PURE__*/React.createElement("input", {
    className: "input mono",
    value: form.url,
    onChange: e => setForm(f => ({
      ...f,
      url: e.target.value
    })),
    disabled: isSSH
  })), isSSH ? /*#__PURE__*/React.createElement("div", {
    className: "field"
  }, /*#__PURE__*/React.createElement("label", null, "Type"), /*#__PURE__*/React.createElement(Badge, {
    tone: "purple"
  }, "SSH")) : /*#__PURE__*/React.createElement("div", {
    className: "field-row"
  }, /*#__PURE__*/React.createElement("div", {
    className: "field"
  }, /*#__PURE__*/React.createElement("label", null, "Type"), /*#__PURE__*/React.createElement("select", {
    className: "input",
    value: form.descriptor.type,
    onChange: e => setType(e.target.value)
  }, /*#__PURE__*/React.createElement("option", {
    value: "mcp"
  }, "MCP"), /*#__PURE__*/React.createElement("option", {
    value: "api"
  }, "API"), /*#__PURE__*/React.createElement("option", {
    value: "oidc"
  }, "OIDC"), /*#__PURE__*/React.createElement("option", {
    value: "tasks"
  }, "Tasks"), /*#__PURE__*/React.createElement("option", {
    value: "virtual"
  }, "Virtual"))), !isTasks && !isVirtual && /*#__PURE__*/React.createElement("div", {
    className: "field"
  }, /*#__PURE__*/React.createElement("label", null, "Proxied"), /*#__PURE__*/React.createElement("select", {
    className: "input",
    value: form.descriptor.proxied ? 'true' : 'false',
    onChange: e => updDesc('proxied', e.target.value === 'true')
  }, /*#__PURE__*/React.createElement("option", {
    value: "false"
  }, "No"), /*#__PURE__*/React.createElement("option", {
    value: "true"
  }, "Yes")))), isTasks && /*#__PURE__*/React.createElement("div", {
    className: "field",
    style: {
      maxWidth: 380
    }
  }, /*#__PURE__*/React.createElement("label", null, "Auth service"), /*#__PURE__*/React.createElement("select", {
    className: "input",
    value: form.descriptor.auth_service_id || '',
    onChange: e => updDesc('auth_service_id', e.target.value)
  }, /*#__PURE__*/React.createElement("option", {
    value: ""
  }, "\u2014 None (app nonce only) \u2014"), authSvcOptions.map(s => /*#__PURE__*/React.createElement("option", {
    key: s.id,
    value: s.id
  }, s.label, " (", s.type, ")"))), authSvcOptions.length === 0 && /*#__PURE__*/React.createElement("span", {
    className: "help"
  }, "No auth services available. Add an OIDC service or use the built-in SSH service.")), (form.descriptor.type === 'api' || form.descriptor.type === 'virtual') && /*#__PURE__*/React.createElement(React.Fragment, null, /*#__PURE__*/React.createElement("div", {
    className: "field-row"
  }, /*#__PURE__*/React.createElement("div", {
    className: "field"
  }, /*#__PURE__*/React.createElement("label", null, "Auth"), /*#__PURE__*/React.createElement("select", {
    className: "input",
    value: form.descriptor.auth || '',
    onChange: e => updDesc('auth', e.target.value)
  }, /*#__PURE__*/React.createElement("option", {
    value: ""
  }, "OAuth (default)"), /*#__PURE__*/React.createElement("option", {
    value: "key"
  }, "API key"))), form.descriptor.auth === 'key' && /*#__PURE__*/React.createElement("div", {
    className: "field"
  }, /*#__PURE__*/React.createElement("label", null, "API Key"), /*#__PURE__*/React.createElement("input", {
    className: "input mono",
    type: "password",
    value: form.descriptor.api_key || '',
    onChange: e => updDesc('api_key', e.target.value)
  }))), form.descriptor.auth === 'key' && /*#__PURE__*/React.createElement("div", {
    className: "field"
  }, /*#__PURE__*/React.createElement("label", null, "API Key Header"), /*#__PURE__*/React.createElement("input", {
    className: "input mono",
    value: form.descriptor.header || '',
    onChange: e => updDesc('header', e.target.value),
    placeholder: "X-API-Key (or empty for Bearer)"
  }))), (form.descriptor.type === 'oidc' || (form.descriptor.type === 'api' || form.descriptor.type === 'virtual') && !form.descriptor.auth) && /*#__PURE__*/React.createElement(React.Fragment, null, /*#__PURE__*/React.createElement("div", {
    className: "field"
  }, /*#__PURE__*/React.createElement("label", null, "OAuth URL"), /*#__PURE__*/React.createElement("input", {
    className: "input mono",
    value: form.descriptor.oauth_url || '',
    onChange: e => updDesc('oauth_url', e.target.value),
    placeholder: "https://provider.com/oauth"
  })), /*#__PURE__*/React.createElement("div", {
    className: "field-row"
  }, /*#__PURE__*/React.createElement("div", {
    className: "field"
  }, /*#__PURE__*/React.createElement("label", null, "Client ID"), /*#__PURE__*/React.createElement("input", {
    className: "input mono",
    value: form.descriptor.client_id || '',
    onChange: e => updDesc('client_id', e.target.value)
  })), /*#__PURE__*/React.createElement("div", {
    className: "field"
  }, /*#__PURE__*/React.createElement("label", null, "Scopes"), /*#__PURE__*/React.createElement("input", {
    className: "input",
    value: form.descriptor.scopes || '',
    onChange: e => updDesc('scopes', e.target.value),
    placeholder: "openid profile email"
  }))), /*#__PURE__*/React.createElement("div", {
    className: "field"
  }, /*#__PURE__*/React.createElement("label", null, "Client Secret"), /*#__PURE__*/React.createElement("input", {
    className: "input mono",
    type: "password",
    value: form.descriptor.client_secret || '',
    onChange: e => updDesc('client_secret', e.target.value)
  }))), form.descriptor.type === 'oidc' && /*#__PURE__*/React.createElement(React.Fragment, null, /*#__PURE__*/React.createElement("div", {
    className: "field"
  }, /*#__PURE__*/React.createElement("label", null, "Userinfo URL"), /*#__PURE__*/React.createElement("input", {
    className: "input mono",
    value: form.descriptor.userinfo_url || '',
    onChange: e => updDesc('userinfo_url', e.target.value)
  })), /*#__PURE__*/React.createElement("div", {
    className: "field"
  }, /*#__PURE__*/React.createElement("label", null, "User Email URL"), /*#__PURE__*/React.createElement("input", {
    className: "input mono",
    value: form.descriptor.userinfo_emails_url || '',
    onChange: e => updDesc('userinfo_emails_url', e.target.value)
  }))), isEdit && (isTasks || isVirtual) && /*#__PURE__*/React.createElement("div", {
    className: "field",
    style: {
      marginTop: 16
    }
  }, /*#__PURE__*/React.createElement("div", {
    style: {
      display: 'flex',
      alignItems: 'center',
      justifyContent: 'space-between',
      gap: 12
    }
  }, /*#__PURE__*/React.createElement("label", {
    style: {
      margin: 0
    }
  }, "Tools ", /*#__PURE__*/React.createElement(Badge, {
    tone: "gray",
    dot: false
  }, tools.length)), /*#__PURE__*/React.createElement("button", {
    className: "btn btn-sm btn-primary",
    onClick: () => onEditTools?.(service.id)
  }, /*#__PURE__*/React.createElement(Icon, {
    name: "edit",
    size: 12
  }), " Edit")), toolsLoading && /*#__PURE__*/React.createElement("span", {
    className: "muted"
  }, "Loading\u2026"), !toolsLoading && toolsError && /*#__PURE__*/React.createElement("span", {
    className: "help",
    style: {
      color: 'var(--danger)'
    }
  }, toolsError), !toolsLoading && !toolsError && tools.length === 0 && /*#__PURE__*/React.createElement("span", {
    className: "muted"
  }, "No tools found. Publish a ", isTasks ? 'tasks' : 'virtual', " file to define tools."), !toolsLoading && !toolsError && tools.length > 0 && /*#__PURE__*/React.createElement("ul", {
    style: {
      margin: '8px 0 0',
      padding: 0,
      listStyle: 'none'
    }
  }, tools.map((t, i) => /*#__PURE__*/React.createElement("li", {
    key: i,
    style: {
      padding: '6px 0',
      borderBottom: '1px solid var(--line-soft)'
    }
  }, /*#__PURE__*/React.createElement("b", null, t.name), t.description && /*#__PURE__*/React.createElement("span", {
    className: "muted"
  }, " \u2014 ", t.description))))));
}

// ── Service tools editor ───────────────────────────────────────────────

function ServiceToolsEditor({
  token,
  services,
  serviceId,
  onBack,
  onSaved
}) {
  const service = services.find(s => String(s.id) === serviceId);
  const type = service?.descriptor?.type;
  const isTasks = type === 'tasks';
  const toast = useToast();
  const textareaRef = useRef(null);
  const [content, setContent] = useState('');
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [dirty, setDirty] = useState(false);
  useEffect(() => {
    if (!service) return;
    let cancelled = false;
    setLoading(true);
    api(token, 'GET', '/api/services/' + serviceId + '/files', null, {
      rawText: true
    }).then(text => {
      if (!cancelled) {
        setContent(text || '');
        setDirty(false);
      }
    }).catch(e => {
      if (cancelled) return;
      if (e.message.includes('404')) {
        setContent('');
        setDirty(false);
      } else toast('Failed to load file: ' + e.message, true);
    }).finally(() => {
      if (!cancelled) setLoading(false);
    });
    return () => {
      cancelled = true;
    };
  }, [service, serviceId, token]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    const onKey = e => {
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
      const blob = new Blob([content], {
        type: 'text/plain'
      });
      const form = new FormData();
      form.append('file', blob, service.name + '.txt');
      await api(token, 'POST', '/api/services/' + serviceId + '/files', form, {
        rawText: true
      });
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
    return /*#__PURE__*/React.createElement(React.Fragment, null, /*#__PURE__*/React.createElement(PageHead, {
      crumbs: ['Workspace', 'Services'],
      title: "Service not found",
      actions: /*#__PURE__*/React.createElement("button", {
        className: "btn btn-ghost",
        onClick: onBack
      }, "Back")
    }), /*#__PURE__*/React.createElement("div", {
      className: "empty"
    }, /*#__PURE__*/React.createElement("b", null, "Service not found.")));
  }
  return /*#__PURE__*/React.createElement("div", {
    className: "editor-page"
  }, /*#__PURE__*/React.createElement(PageHead, {
    crumbs: ['Workspace', 'Services', service.name],
    title: `Edit ${isTasks ? 'tasks' : 'virtual'} script`,
    sub: `Plain-text definition for ${service.name}.`,
    actions: /*#__PURE__*/React.createElement(React.Fragment, null, /*#__PURE__*/React.createElement("button", {
      className: "btn btn-ghost",
      onClick: onBack,
      disabled: saving
    }, "Cancel"), /*#__PURE__*/React.createElement("button", {
      className: "btn btn-primary",
      onClick: handleSave,
      disabled: saving || !dirty
    }, saving ? 'Saving…' : 'Save', " ", /*#__PURE__*/React.createElement("span", {
      className: "muted",
      style: {
        fontSize: 11,
        marginLeft: 6
      }
    }, "\u2318S")))
  }), loading ? /*#__PURE__*/React.createElement("div", {
    style: {
      display: 'grid',
      placeItems: 'center',
      padding: 48,
      color: 'var(--ink-3)'
    }
  }, "Loading\u2026") : /*#__PURE__*/React.createElement("textarea", {
    ref: textareaRef,
    className: "editor-textarea",
    value: content,
    onChange: e => {
      setContent(e.target.value);
      setDirty(true);
    },
    spellCheck: false,
    autoComplete: "off",
    autoCorrect: "off",
    autoCapitalize: "off"
  }));
}

// ── Roles ──────────────────────────────────────────────────────────────

function RolesView({
  roles
}) {
  const PERMS = [{
    group: 'Apps',
    items: ['Read', 'Create', 'Edit', 'Delete']
  }, {
    group: 'Services',
    items: ['Read', 'Create', 'Edit', 'Delete']
  }, {
    group: 'Users',
    items: ['Read', 'Invite', 'Suspend', 'Delete']
  }];
  const checkedFor = (role, group, item) => {
    if (role.name === 'Superuser') return true;
    if (role.name === 'Admin') return true;
    if (role.name === 'Member') return item === 'Read' || group === 'Apps' && item === 'Edit';
    if (role.name === 'Read-only') return item === 'Read';
    return false;
  };
  return /*#__PURE__*/React.createElement(React.Fragment, null, /*#__PURE__*/React.createElement(PageHead, {
    crumbs: ['Security', 'Roles'],
    title: "Roles & permissions",
    sub: "Built-in roles. Custom roles coming later."
  }), /*#__PURE__*/React.createElement("div", {
    className: "table-wrap",
    style: {
      marginBottom: 28
    }
  }, /*#__PURE__*/React.createElement("table", {
    className: "tbl"
  }, /*#__PURE__*/React.createElement("thead", null, /*#__PURE__*/React.createElement("tr", null, /*#__PURE__*/React.createElement("th", {
    style: {
      width: '20%'
    }
  }, "Role"), /*#__PURE__*/React.createElement("th", null, "Description"), /*#__PURE__*/React.createElement("th", null, "Members"))), /*#__PURE__*/React.createElement("tbody", null, roles.map(r => /*#__PURE__*/React.createElement("tr", {
    key: r.id
  }, /*#__PURE__*/React.createElement("td", null, /*#__PURE__*/React.createElement(Badge, {
    tone: roleTone(r.name)
  }, r.name)), /*#__PURE__*/React.createElement("td", null, r.description), /*#__PURE__*/React.createElement("td", {
    className: "mono"
  }, r.members)))))), /*#__PURE__*/React.createElement("h3", {
    style: {
      margin: '0 0 12px',
      fontSize: 14,
      fontWeight: 500
    }
  }, "Permission matrix"), /*#__PURE__*/React.createElement("div", {
    className: "table-wrap"
  }, /*#__PURE__*/React.createElement("table", {
    className: "tbl"
  }, /*#__PURE__*/React.createElement("thead", null, /*#__PURE__*/React.createElement("tr", null, /*#__PURE__*/React.createElement("th", null, "Capability"), roles.map(r => /*#__PURE__*/React.createElement("th", {
    key: r.id,
    style: {
      textAlign: 'center'
    }
  }, r.name)))), /*#__PURE__*/React.createElement("tbody", null, PERMS.flatMap(p => p.items.map(item => /*#__PURE__*/React.createElement("tr", {
    key: p.group + item
  }, /*#__PURE__*/React.createElement("td", null, /*#__PURE__*/React.createElement("span", {
    className: "muted mono",
    style: {
      fontSize: 11
    }
  }, p.group), " \xA0", item), roles.map(r => /*#__PURE__*/React.createElement("td", {
    key: r.id,
    style: {
      textAlign: 'center'
    }
  }, checkedFor(r, p.group, item) ? /*#__PURE__*/React.createElement("span", {
    className: "mono"
  }, /*#__PURE__*/React.createElement(Icon, {
    name: "check",
    size: 14
  })) : /*#__PURE__*/React.createElement("span", {
    className: "muted"
  }, "\u2014"))))))))));
}

// ── Audit ──────────────────────────────────────────────────────────────

function AuditView({
  audit
}) {
  const [q, setQ] = useState('');
  const filtered = audit.filter(a => !q || `${a.actor} ${a.action} ${a.target}`.toLowerCase().includes(q.toLowerCase()));
  return /*#__PURE__*/React.createElement(React.Fragment, null, /*#__PURE__*/React.createElement(PageHead, {
    crumbs: ['Security', 'Audit log'],
    title: "Audit log",
    sub: "Recent changes across the system."
  }), /*#__PURE__*/React.createElement(Toolbar, {
    search: q,
    onSearch: setQ,
    placeholder: "Search events\u2026"
  }), /*#__PURE__*/React.createElement("div", {
    className: "table-wrap",
    style: {
      padding: '8px 24px'
    }
  }, /*#__PURE__*/React.createElement("div", {
    className: "timeline"
  }, filtered.map(a => {
    const ai = actionIcon(a.action);
    return /*#__PURE__*/React.createElement("div", {
      key: a.id,
      className: "tl-row"
    }, /*#__PURE__*/React.createElement("span", {
      className: "tl-when"
    }, fmtAuditTime(a.when)), /*#__PURE__*/React.createElement("span", {
      className: `tl-icn tone-${ai.tone}`
    }, /*#__PURE__*/React.createElement(Icon, {
      name: ai.icon,
      size: 14
    })), /*#__PURE__*/React.createElement("div", {
      className: "tl-body"
    }, /*#__PURE__*/React.createElement("div", null, /*#__PURE__*/React.createElement("b", null, a.actor), " ", /*#__PURE__*/React.createElement("span", {
      className: "muted"
    }, a.action)), /*#__PURE__*/React.createElement("div", {
      className: "target"
    }, a.target)));
  }))));
}

// ── Settings ───────────────────────────────────────────────────────────

function SettingsView({
  token,
  services,
  apps,
  user
}) {
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
    api(token, 'GET', '/api/settings').then(d => {
      const id = d.admin_auth_service || '';
      setSelectedSvc(id);
      setCurrentSvc(id ? services.find(s => String(s.id) === id) || null : null);
      setDefaultApp(d.default_app || '');
    }).catch(e => toast(e.message, true)).finally(() => setLoading(false));
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    if (!user || user.id < 0) return;
    setSSHLoading(true);
    api(token, 'GET', '/api/me/ssh-key').then(d => setSSHKey(d.ssh_key)).catch(() => setSSHKey(null)).finally(() => setSSHLoading(false));
  }, [user]); // eslint-disable-line react-hooks/exhaustive-deps

  const oidcServices = services.filter(s => s.descriptor?.type === 'oidc');
  const sshService = services.find(s => s.descriptor?.type === 'ssh');
  const authServices = [...oidcServices.map(s => ({
    id: String(s.id),
    label: s.name,
    type: 'OIDC'
  })), ...(sshService ? [{
    id: String(sshService.id),
    label: 'SSH Key',
    type: 'SSH'
  }] : [])];
  const saveAuth = async () => {
    try {
      await api(token, 'PUT', '/api/settings', {
        admin_auth_service: selectedSvc
      });
      setCurrentSvc(oidcServices.find(s => String(s.id) === selectedSvc) || null);
      toast('Settings saved');
    } catch (e) {
      toast(e.message, true);
    }
  };
  const unlink = async () => {
    if (!confirm('Remove admin auth? The control panel will be open until auth is reconfigured.')) return;
    try {
      await api(token, 'PUT', '/api/settings', {
        admin_auth_service: ''
      });
      setSelectedSvc('');
      setCurrentSvc(null);
      toast('Admin auth removed');
    } catch (e) {
      toast(e.message, true);
    }
  };
  const saveLanding = async () => {
    try {
      await api(token, 'PUT', '/api/settings', {
        default_app: defaultApp
      });
      toast('Landing page saved');
    } catch (e) {
      toast(e.message, true);
    }
  };
  return /*#__PURE__*/React.createElement(React.Fragment, null, /*#__PURE__*/React.createElement(PageHead, {
    crumbs: ['Security', 'Settings'],
    title: "Settings",
    sub: "Control panel configuration."
  }), /*#__PURE__*/React.createElement("div", {
    className: "setting-section"
  }, /*#__PURE__*/React.createElement("h3", {
    className: "setting-heading"
  }, "Admin authentication"), /*#__PURE__*/React.createElement("p", {
    className: "muted",
    style: {
      marginBottom: 20,
      fontSize: 13
    }
  }, "Gate this control panel with an OIDC service. Once set, all API calls require a valid identity token."), loading ? /*#__PURE__*/React.createElement("span", {
    className: "muted"
  }, "Loading\u2026") : /*#__PURE__*/React.createElement(React.Fragment, null, /*#__PURE__*/React.createElement("div", {
    className: "field",
    style: {
      maxWidth: 380
    }
  }, /*#__PURE__*/React.createElement("label", null, "Auth service"), /*#__PURE__*/React.createElement("select", {
    className: "input",
    value: selectedSvc,
    onChange: e => setSelectedSvc(e.target.value)
  }, /*#__PURE__*/React.createElement("option", {
    value: ""
  }, "\u2014 None (open access) \u2014"), authServices.map(s => /*#__PURE__*/React.createElement("option", {
    key: s.id,
    value: s.id
  }, s.label, " (", s.type, ")"))), oidcServices.length === 0 && !sshService && /*#__PURE__*/React.createElement("span", {
    className: "help"
  }, "No auth services available. Add an OIDC service or use the built-in SSH service.")), /*#__PURE__*/React.createElement("div", {
    style: {
      display: 'flex',
      gap: 8,
      marginTop: 16
    }
  }, /*#__PURE__*/React.createElement("button", {
    className: "btn btn-primary",
    onClick: saveAuth
  }, "Save"), currentSvc && /*#__PURE__*/React.createElement("button", {
    className: "btn btn-ghost",
    onClick: unlink
  }, "Unlink")))), /*#__PURE__*/React.createElement("div", {
    className: "setting-section",
    style: {
      marginTop: 32
    }
  }, /*#__PURE__*/React.createElement("h3", {
    className: "setting-heading"
  }, "Default landing page"), /*#__PURE__*/React.createElement("p", {
    className: "muted",
    style: {
      marginBottom: 20,
      fontSize: 13
    }
  }, "Choose where visitors land when they hit the root URL. Only hosted apps are available as targets."), loading ? /*#__PURE__*/React.createElement("span", {
    className: "muted"
  }, "Loading\u2026") : /*#__PURE__*/React.createElement("div", {
    className: "field",
    style: {
      maxWidth: 380
    }
  }, /*#__PURE__*/React.createElement("label", null, "Landing page"), /*#__PURE__*/React.createElement("select", {
    className: "input",
    value: defaultApp,
    onChange: e => setDefaultApp(e.target.value)
  }, /*#__PURE__*/React.createElement("option", {
    value: ""
  }, "Control Panel"), apps.filter(isHosted).map(a => /*#__PURE__*/React.createElement("option", {
    key: a.nonce,
    value: a.nonce
  }, a.name))), apps.filter(isHosted).length === 0 && /*#__PURE__*/React.createElement("span", {
    className: "help"
  }, "No hosted apps yet. Upload web content to an app to make it available as a landing page."), /*#__PURE__*/React.createElement("div", {
    style: {
      display: 'flex',
      gap: 8,
      marginTop: 16
    }
  }, /*#__PURE__*/React.createElement("button", {
    className: "btn btn-primary",
    onClick: saveLanding
  }, "Save")))), user && user.id > 0 && /*#__PURE__*/React.createElement("div", {
    className: "setting-section",
    style: {
      marginTop: 32
    }
  }, /*#__PURE__*/React.createElement("h3", {
    className: "setting-heading"
  }, "SSH Key"), /*#__PURE__*/React.createElement("p", {
    className: "muted",
    style: {
      marginBottom: 20,
      fontSize: 13
    }
  }, "Generate an SSH key pair for authentication and agent forwarding. Only the public key is shown after creation."), sshLoading ? /*#__PURE__*/React.createElement("span", {
    className: "muted"
  }, "Loading\u2026") : sshKey ? /*#__PURE__*/React.createElement(React.Fragment, null, /*#__PURE__*/React.createElement("div", {
    style: {
      marginBottom: 12
    }
  }, /*#__PURE__*/React.createElement(Badge, {
    tone: "green"
  }, "Active"), /*#__PURE__*/React.createElement("span", {
    className: "muted",
    style: {
      marginLeft: 8,
      fontSize: 13
    }
  }, sshKey.key_type?.toUpperCase(), " \xB7 ", sshKey.fingerprint)), /*#__PURE__*/React.createElement("div", {
    className: "field",
    style: {
      maxWidth: 560
    }
  }, /*#__PURE__*/React.createElement("label", null, "Public key"), /*#__PURE__*/React.createElement("div", {
    style: {
      display: 'flex',
      gap: 8
    }
  }, /*#__PURE__*/React.createElement("input", {
    className: "input mono",
    value: sshKey.public_key?.trim(),
    readOnly: true,
    style: {
      fontSize: 12
    }
  }), /*#__PURE__*/React.createElement("button", {
    className: "btn btn-ghost",
    onClick: () => copyText(sshKey.public_key?.trim(), toast)
  }, /*#__PURE__*/React.createElement(Icon, {
    name: "copy",
    size: 14
  })))), /*#__PURE__*/React.createElement("div", {
    style: {
      marginTop: 12
    }
  }, /*#__PURE__*/React.createElement("button", {
    className: "btn btn-ghost",
    style: {
      color: 'var(--red)'
    },
    onClick: async () => {
      if (!confirm('Delete your SSH key? You\'ll need to generate a new one to use SSH auth.')) return;
      try {
        await api(token, 'DELETE', '/api/me/ssh-key');
        setSSHKey(null);
        toast('SSH key deleted');
      } catch (e) {
        toast(e.message, true);
      }
    }
  }, "Delete key"))) : /*#__PURE__*/React.createElement("button", {
    className: "btn btn-primary",
    onClick: () => setShowGenModal(true)
  }, /*#__PURE__*/React.createElement(Icon, {
    name: "key",
    size: 14
  }), " Generate SSH Key")), showGenModal && /*#__PURE__*/React.createElement("div", {
    className: "modal-overlay",
    onClick: () => setShowGenModal(false)
  }, /*#__PURE__*/React.createElement("div", {
    className: "modal",
    onClick: e => e.stopPropagation(),
    style: {
      maxWidth: 420
    }
  }, /*#__PURE__*/React.createElement("h3", {
    style: {
      marginBottom: 16
    }
  }, "Generate SSH Key"), /*#__PURE__*/React.createElement("p", {
    className: "muted",
    style: {
      fontSize: 13,
      marginBottom: 16
    }
  }, "Choose a strong passphrase. You'll need it each time you log in via SSH. It cannot be recovered if forgotten."), /*#__PURE__*/React.createElement("div", {
    className: "field"
  }, /*#__PURE__*/React.createElement("label", null, "Passphrase"), /*#__PURE__*/React.createElement("input", {
    className: "input",
    type: "password",
    value: passphrase,
    onChange: e => setPassphrase(e.target.value),
    placeholder: "Min 8 characters",
    autoFocus: true
  })), /*#__PURE__*/React.createElement("div", {
    className: "field"
  }, /*#__PURE__*/React.createElement("label", null, "Confirm passphrase"), /*#__PURE__*/React.createElement("input", {
    className: "input",
    type: "password",
    value: passConfirm,
    onChange: e => setPassConfirm(e.target.value),
    placeholder: "Re-enter passphrase"
  })), /*#__PURE__*/React.createElement("div", {
    style: {
      display: 'flex',
      gap: 8,
      justifyContent: 'flex-end',
      marginTop: 20
    }
  }, /*#__PURE__*/React.createElement("button", {
    className: "btn btn-ghost",
    onClick: () => {
      setShowGenModal(false);
      setPassphrase('');
      setPassConfirm('');
    }
  }, "Cancel"), /*#__PURE__*/React.createElement("button", {
    className: "btn btn-primary",
    disabled: passphrase.length < 8 || passphrase !== passConfirm,
    onClick: async () => {
      try {
        const d = await api(token, 'POST', '/api/me/ssh-key', {
          passphrase
        });
        setSSHKey(d.ssh_key);
        setShowGenModal(false);
        setPassphrase('');
        setPassConfirm('');
        toast('SSH key generated');
      } catch (e) {
        toast(e.message, true);
      }
    }
  }, "Generate")))));
}

// ── Helpers ────────────────────────────────────────────────────────────

const fmtAuditTime = iso => {
  if (!iso) return '—';
  const d = new Date(iso);
  const sameYear = d.getFullYear() === new Date().getFullYear();
  return d.toLocaleString(undefined, {
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
    ...(sameYear ? {} : {
      year: 'numeric'
    })
  });
};

// ── Routing ────────────────────────────────────────────────────────────

// ── Routing ────────────────────────────────────────────────────────────

// Parses /control and /control/:page/:... into a stable route object.
// Top-level pages are the NAV ids. Nested routes:
//   /control/services/:id/edit-tools  -> page='services', params={serviceId, screen:'edit-tools'}
const parseRoute = () => {
  const parts = window.location.pathname.replace(/^\/control\/?/, '').split('/').filter(Boolean);
  const top = parts[0] || 'home';
  if (!NAV.some(n => n.id === top)) return {
    page: 'home',
    params: {}
  };
  if (top === 'services' && parts.length === 3 && parts[2] === 'edit-tools') {
    return {
      page: 'services',
      params: {
        serviceId: parts[1],
        screen: 'edit-tools'
      }
    };
  }
  return {
    page: top,
    params: {}
  };
};
const buildPath = (page, params = {}) => {
  if (page === 'services' && params.screen === 'edit-tools' && params.serviceId) {
    return `/control/services/${params.serviceId}/edit-tools`;
  }
  return page === 'home' ? '/control' : `/control/${page}`;
};

// ── App ────────────────────────────────────────────────────────────────

function AppShell() {
  const {
    user,
    token,
    authRequired,
    serviceName,
    login,
    logout,
    sessionExpired,
    clearExpired,
    authError
  } = useAuth();
  const [route, setRoute] = useState(parseRoute);
  const [sidebarOpen, setSidebarOpen] = useState(false);
  useEffect(() => {
    const onPop = () => setRoute(parseRoute());
    window.addEventListener('popstate', onPop);
    return () => window.removeEventListener('popstate', onPop);
  }, []);
  const navigate = (page, params = {}) => {
    history.pushState(null, '', buildPath(page, params));
    setRoute({
      page,
      params
    });
  };
  const [users, setUsers] = useState([]);
  const [apps, setApps] = useState([]);
  const [services, setServices] = useState([]);
  const [roles, setRoles] = useState([]);
  const [audit, setAudit] = useState([]);
  const [loading, setLoading] = useState(true);
  const toast = useToast();
  const load = useCallback(async () => {
    setLoading(true);
    try {
      const [u, a, s, r, au] = await Promise.all([api(token, 'GET', '/api/users'), api(token, 'GET', '/api/apps'), api(token, 'GET', '/api/services'), api(token, 'GET', '/api/roles'), api(token, 'GET', '/api/audit')]);
      setUsers(u.users || []);
      setApps(a.apps || []);
      setServices(s.services || []);
      setRoles(r.roles || []);
      setAudit(au.audit || []);
    } catch (e) {
      if (!authRequired || user) toast('Failed to load: ' + e.message, true);
    }
    setLoading(false);
  }, [authRequired, user]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    if (authRequired && !user) return;
    load();
  }, [authRequired, user]); // eslint-disable-line react-hooks/exhaustive-deps

  if (authRequired && !user) return /*#__PURE__*/React.createElement(LoginScreen, {
    serviceName: serviceName,
    onLogin: login,
    authError: authError
  });
  const counts = {
    users: users.length,
    apps: apps.length,
    services: services.length
  };
  if (loading) return /*#__PURE__*/React.createElement("div", {
    style: {
      display: 'grid',
      placeItems: 'center',
      height: '100vh',
      color: 'var(--ink-3)'
    }
  }, "Loading\u2026");
  const activeLabel = NAV.find(n => n.id === route.page)?.label;
  return /*#__PURE__*/React.createElement("div", {
    className: "app-shell"
  }, /*#__PURE__*/React.createElement("div", {
    className: `sidebar-scrim${sidebarOpen ? ' open' : ''}`,
    onClick: () => setSidebarOpen(false)
  }), /*#__PURE__*/React.createElement(Sidebar, {
    active: route.page,
    onNav: id => navigate(id),
    counts: counts,
    user: user,
    onLogout: user ? logout : null,
    mobileOpen: sidebarOpen,
    onMobileClose: () => setSidebarOpen(false)
  }), /*#__PURE__*/React.createElement("main", {
    className: "main",
    "data-screen-label": activeLabel
  }, /*#__PURE__*/React.createElement(MobileTopBar, {
    onMenuOpen: () => setSidebarOpen(true),
    pageLabel: activeLabel
  }), sessionExpired && /*#__PURE__*/React.createElement(SessionBanner, {
    onLogin: login,
    onDismiss: clearExpired
  }), route.page === 'home' && /*#__PURE__*/React.createElement(Overview, {
    users: users,
    apps: apps,
    services: services,
    audit: audit
  }), route.page === 'apps' && /*#__PURE__*/React.createElement(AppsView, {
    token: token,
    apps: apps,
    services: services,
    users: users,
    onRefresh: load
  }), route.page === 'services' && route.params.screen !== 'edit-tools' && /*#__PURE__*/React.createElement(ServicesView, {
    token: token,
    services: services,
    onRefresh: load,
    onEditTools: id => navigate('services', {
      serviceId: String(id),
      screen: 'edit-tools'
    })
  }), route.page === 'services' && route.params.screen === 'edit-tools' && /*#__PURE__*/React.createElement(ServiceToolsEditor, {
    token: token,
    services: services,
    serviceId: route.params.serviceId,
    onBack: () => navigate('services'),
    onSaved: load
  }), route.page === 'users' && /*#__PURE__*/React.createElement(UsersView, {
    token: token,
    users: users,
    apps: apps,
    onRefresh: load
  }), route.page === 'roles' && /*#__PURE__*/React.createElement(RolesView, {
    roles: roles
  }), route.page === 'audit' && /*#__PURE__*/React.createElement(AuditView, {
    audit: audit
  }), route.page === 'settings' && /*#__PURE__*/React.createElement(SettingsView, {
    token: token,
    services: services,
    apps: apps,
    user: user
  })));
}
function App() {
  return /*#__PURE__*/React.createElement(ToastProvider, null, /*#__PURE__*/React.createElement(AuthProvider, null, /*#__PURE__*/React.createElement(AppShell, null)));
}
ReactDOM.createRoot(document.getElementById('root')).render(/*#__PURE__*/React.createElement(App, null));