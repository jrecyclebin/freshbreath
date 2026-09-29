// window-avatar.js — Fresh Breath avatars
// Generates a hand-drawn "window with a view" avatar as a line-drawn SVG string,
// deterministically from any string (typically a user's e-mail address).
// Drawn for small sizes (around 30px): heavy strokes, one idea per window.
//
//   import { windowAvatar } from './window-avatar.js';
//   el.innerHTML = windowAvatar('someone@example.com', { size: 30 });
//
// Same input always gives the same picture. No dependencies; works in the
// browser and in Node (server-side rendering, caching as .svg files, etc).

export function windowAvatar(email, options = {}) {
  const size = options.size ?? 30;
  const color = options.color ?? '#1a1a1a';
  const key = String(email).trim().toLowerCase();
  const seed = hashString(key);
  const rnd = random(seed);
  const uid = 'fb' + seed.toString(36);

  const frame = makeFrame(rnd);
  const scene = composeScene(rnd, frame.bounds);

  const content = scene.map(s => strokePath(s.points, rnd, s)).join('');
  const framing = frame.strokes.map(s => strokePath(s.points, rnd, s)).join('');
  const clip = 'M' + frame.inner.map(p => p[0].toFixed(1) + ' ' + p[1].toFixed(1)).join('L') + 'Z';

  // At larger sizes a displacement filter roughens the edges like felt-tip.
  // At avatar sizes it only blurs, so it is left off.
  const rough = size >= 64;
  const filter = rough
    ? `<filter id="${uid}f" x="-10%" y="-10%" width="120%" height="120%">`
      + `<feTurbulence type="fractalNoise" baseFrequency="0.7" numOctaves="2" seed="${seed % 1000}" result="n"/>`
      + `<feDisplacementMap in="SourceGraphic" in2="n" scale="2" xChannelSelector="R" yChannelSelector="G"/></filter>`
    : '';

  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100" width="${size}" height="${size}" role="img">`
    + `<defs><clipPath id="${uid}c"><path d="${clip}"/></clipPath>${filter}</defs>`
    + `<g fill="none" stroke="${color}" stroke-linecap="round" stroke-linejoin="round"${rough ? ` filter="url(#${uid}f)"` : ''}>`
    + `<g clip-path="url(#${uid}c)">${content}</g>${framing}</g></svg>`;
}

// ---------------------------------------------------------------------------
// Randomness: string hash -> seeded PRNG

function hashString(str) {
  let h1 = 0xdeadbeef, h2 = 0x41c6ce57;
  for (let i = 0; i < str.length; i++) {
    const c = str.charCodeAt(i);
    h1 = Math.imul(h1 ^ c, 2654435761);
    h2 = Math.imul(h2 ^ c, 1597334677);
  }
  h1 = Math.imul(h1 ^ (h1 >>> 16), 2246822507) ^ Math.imul(h2 ^ (h2 >>> 13), 3266489909);
  return h1 >>> 0;
}

function random(seed) {
  let a = seed;
  const next = () => {
    a = (a + 0x6d2b79f5) | 0;
    let t = Math.imul(a ^ (a >>> 15), 1 | a);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
  next.range = (lo, hi) => lo + next() * (hi - lo);
  next.int = (lo, hi) => Math.floor(next.range(lo, hi + 1));
  next.chance = p => next() < p;
  next.pick = arr => arr[Math.floor(next() * arr.length)];
  next.weighted = table => {
    const total = Object.values(table).reduce((a, b) => a + b, 0);
    let r = next() * total;
    for (const [k, w] of Object.entries(table)) if ((r -= w) < 0) return k;
    return Object.keys(table)[0];
  };
  return next;
}

// ---------------------------------------------------------------------------
// Hand-drawn strokes: resample a polyline, push it around with slow, smooth
// noise, then draw it as a Catmull-Rom curve. The wobble is low-frequency so
// it still reads as "hand-drawn" when the whole picture is 30px wide.

function strokePath(points, rnd, { width = 6, wobble = 0.9 } = {}) {
  const pts = thin(jitter(resample(points, 2.5), rnd, wobble));
  return `<path d="${curve(pts)}" stroke-width="${width.toFixed(1)}"/>`;
}

function thin(pts) {
  if (pts.length < 5) return pts;
  const out = pts.filter((_, i) => i % 2 === 0);
  if ((pts.length - 1) % 2) out.push(pts[pts.length - 1]);
  return out;
}

function resample(points, step) {
  const out = [points[0]];
  for (let i = 1; i < points.length; i++) {
    const [ax, ay] = points[i - 1], [bx, by] = points[i];
    const n = Math.max(1, Math.round(Math.hypot(bx - ax, by - ay) / step));
    for (let k = 1; k <= n; k++) out.push([ax + (bx - ax) * k / n, ay + (by - ay) * k / n]);
  }
  return out;
}

function jitter(pts, rnd, amount) {
  if (pts.length < 3 || amount === 0) return pts;
  const f1 = rnd.range(0.04, 0.08), f2 = rnd.range(0.12, 0.2);
  const p1 = rnd.range(0, 6.3), p2 = rnd.range(0, 6.3);
  let dist = 0;
  return pts.map((p, i) => {
    const a = pts[Math.max(0, i - 1)], b = pts[Math.min(pts.length - 1, i + 1)];
    if (i > 0) dist += Math.hypot(p[0] - pts[i - 1][0], p[1] - pts[i - 1][1]);
    const len = Math.hypot(b[0] - a[0], b[1] - a[1]) || 1;
    const nx = -(b[1] - a[1]) / len, ny = (b[0] - a[0]) / len;
    const off = amount * (Math.sin(p1 + dist * f1) * 0.75 + Math.sin(p2 + dist * f2) * 0.25);
    return [p[0] + nx * off, p[1] + ny * off];
  });
}

function curve(pts) {
  if (pts.length === 1) return `M${pts[0][0].toFixed(1)} ${pts[0][1].toFixed(1)}h0.01`;
  let d = `M${pts[0][0].toFixed(1)} ${pts[0][1].toFixed(1)}`;
  for (let i = 0; i < pts.length - 1; i++) {
    const p0 = pts[Math.max(0, i - 1)], p1 = pts[i], p2 = pts[i + 1], p3 = pts[Math.min(pts.length - 1, i + 2)];
    const c1 = [p1[0] + (p2[0] - p0[0]) / 6, p1[1] + (p2[1] - p0[1]) / 6];
    const c2 = [p2[0] - (p3[0] - p1[0]) / 6, p2[1] - (p3[1] - p1[1]) / 6];
    d += `C${c1[0].toFixed(1)} ${c1[1].toFixed(1)} ${c2[0].toFixed(1)} ${c2[1].toFixed(1)} ${p2[0].toFixed(1)} ${p2[1].toFixed(1)}`;
  }
  return d;
}

// ---------------------------------------------------------------------------
// Frames. Each frame is an outline traced clockwise from the bottom-left.
// The inner "reveal" line on the left (the window's depth) is the outline
// pushed inwards, tapering to nothing as it goes over the top. The view is
// clipped to the area inside the reveal.

function makeFrame(rnd) {
  const kind = rnd.weighted({ arch: 40, round: 22, square: 24, pointed: 14 });
  let outline, closed = false;

  if (kind === 'arch') {
    const w = rnd.range(38, 41), cx = 50, bottom = rnd.range(91, 93);
    const spring = bottom - rnd.range(40, 45);
    outline = [[cx - w, bottom], [cx - w, spring]];
    for (let a = 180; a <= 360; a += 6) outline.push(arcPoint(cx, spring, w, a));
    outline.push([cx + w, bottom]);
  } else if (kind === 'pointed') {
    // Two arcs meeting at a soft point, like a chapel window.
    const w = rnd.range(37, 40), cx = 50, bottom = rnd.range(91, 93), R = w * 1.12;
    const spring = bottom - rnd.range(40, 44);
    const top = (Math.acos((w - R) / R) * 180) / Math.PI;
    const left = [];
    for (let a = 180; a <= 360 - top; a += 6) left.push(arcPoint(cx - w + R, spring, R, a));
    left.push([cx, spring - Math.sqrt(R * R - (R - w) * (R - w))]);
    const right = left.slice(0, -1).reverse().map(([x, y]) => [2 * cx - x, y]);
    outline = [[cx - w, bottom], ...left, ...right, [cx + w, bottom]];
  } else if (kind === 'round') {
    const r = rnd.range(41, 43), start = rnd.range(115, 130);
    outline = [];
    for (let a = start; a <= start + 360; a += 6) outline.push(arcPoint(50, 50, r, a));
    closed = true;
  } else {
    const w = rnd.range(39, 41), h = rnd.range(39, 41), r = rnd.range(4, 8);
    const x0 = 50 - w, x1 = 50 + w, y0 = 50 - h, y1 = 50 + h;
    outline = [[x0, y1]];
    for (let a = 180; a <= 270; a += 15) outline.push(arcPoint(x0 + r, y0 + r, r, a));
    for (let a = 270; a <= 360; a += 15) outline.push(arcPoint(x1 - r, y0 + r, r, a));
    outline.push([x1, y1]);
  }

  outline = resample(outline, 2);
  const reveal = rnd.range(10, 12);
  const revealEnd = Math.floor(outline.length * rnd.range(0.5, 0.62));
  const depth = i => (i < revealEnd ? reveal * Math.pow(1 - i / revealEnd, 0.7) : 0);
  const inner = outline.map((p, i) => {
    const a = outline[Math.max(0, i - 1)], b = outline[Math.min(outline.length - 1, i + 1)];
    const len = Math.hypot(b[0] - a[0], b[1] - a[1]) || 1;
    const nx = -(b[1] - a[1]) / len, ny = (b[0] - a[0]) / len;
    return [p[0] + nx * depth(i), p[1] + ny * depth(i)];
  });

  const strokes = [];
  if (closed) {
    // Porthole: go round once and overshoot a little, as a hand would.
    const extra = outline.slice(1, rnd.int(3, 6)).map(([x, y]) => [x + (x - 50) * 0.03, y + (y - 50) * 0.03]);
    strokes.push({ points: outline.concat(extra), width: 7 });
  } else {
    // Sides and top in one stroke, then a sill that overshoots on the left.
    strokes.push({ points: outline, width: 7 });
    const y = outline[0][1];
    strokes.push({ points: [[outline[0][0] - rnd.range(2, 5), y], [outline[outline.length - 1][0] + rnd.range(0, 2), y]], width: 7, wobble: 0.4 });
  }
  // The reveal stops short of the sill so the two don't merge into a blob.
  strokes.push({ points: inner.slice(3, revealEnd + 1), width: 5.2 });

  // The region the scene is composed into.
  const xs = inner.map(p => p[0]), ys = inner.map(p => p[1]);
  const bounds = { x0: Math.min(...xs), x1: Math.max(...xs), y0: Math.min(...ys), y1: Math.max(...ys) };
  bounds.w = bounds.x1 - bounds.x0;
  bounds.h = bounds.y1 - bounds.y0;
  bounds.cx = (bounds.x0 + bounds.x1) / 2;
  // Curved tops leave little room at the sides, so the sky starts lower there.
  bounds.skyTop = bounds.y0 + (kind === 'square' ? 8 : kind === 'pointed' ? 24 : 20);
  return { kind, strokes, inner, bounds };
}

function arcPoint(cx, cy, r, deg) {
  const a = (deg * Math.PI) / 180;
  return [cx + r * Math.cos(a), cy + r * Math.sin(a)];
}

// ---------------------------------------------------------------------------
// Scenes. One thing in the sky, optionally one kind of ground beneath it,
// optionally one small companion (a sail, a star). Lots of empty glass.

const SKY = {
  sun: 14, cloud: 12, moon: 12, birds: 10, rain: 8, curl: 9,
  sunset: 8, stars: 7, snow: 5, storm: 4, breeze: 7,
};

function composeScene(rnd, b) {
  const item = rnd.weighted(SKY);
  let ground = rnd.weighted({ sea: 32, hills: 24, mountains: 20, none: 24 });
  if (item === 'sunset') ground = 'sea';
  // Round things floating in an empty window read as eyes; give them a horizon.
  if (ground === 'none' && ['sun', 'moon', 'stars', 'snow'].includes(item)) ground = rnd.pick(['sea', 'hills']);
  // Mountains take up a lot of sky, so they only go with small sky things.
  if (ground === 'mountains' && !['sun', 'moon', 'stars', 'snow'].includes(item)) ground = rnd.pick(['sea', 'hills']);

  const lines = [];
  const add = (points, opts = {}) => lines.push({ points, ...opts });
  const horizon = ground === 'none' ? b.y1 : b.y0 + b.h * rnd.range(0.7, 0.78);
  const rise = { sea: 0, hills: 10, mountains: 18, none: 0 }[ground];
  const skyTop = b.skyTop, skyBottom = horizon - rise - 8;
  const skyMid = (skyTop + skyBottom) / 2;
  // Sky things sit a little off-centre, towards the open right-hand side.
  const side = rnd.pick([-1, 1, 1]);
  // A sun or moon straight above a hump reads as a head and shoulders (the
  // generic "user" icon), so those sit further out, over a dip in the land.
  const lamp = ['sun', 'moon'].includes(item);
  const sx = b.cx + side * b.w * (lamp && ground !== 'sea' ? rnd.range(0.12, 0.15) : rnd.range(0.05, 0.12));

  // --- sky -----------------------------------------------------------------
  if (item === 'sun') {
    circle(add, rnd, sx, skyMid + rnd.range(0, 3), rnd.range(11, 13));
  } else if (item === 'moon') {
    moon(add, rnd, sx, skyMid + 1, rnd.range(12, 14));
    if (rnd.chance(0.5)) sparkle(add, sx - side * 26, skyMid - rnd.range(8, 11), 5.5);
  } else if (item === 'stars') {
    // Placed on a diagonal: two marks side by side read as eyes.
    sparkle(add, b.cx + side * 12, skyMid - 7, 8);
    dot(add, b.cx - side * 6, skyMid + 9, 8);
  } else if (item === 'cloud') {
    cloud(add, rnd, sx, skyMid + 7, rnd.range(46, 52));
  } else if (item === 'rain' || item === 'storm') {
    const cy = skyTop + 18;
    cloud(add, rnd, b.cx + 2, cy, 44);
    if (item === 'rain') {
      for (const dx of [-12, 2, 16]) add([[b.cx + dx, cy + 9], [b.cx + dx - 3, cy + 20]], { width: 5.2, wobble: 0 });
    } else {
      const x = b.cx + 3;
      add([[x + 3, cy + 8], [x - 6, cy + 21], [x + 4, cy + 21], [x - 4, cy + 35]], { width: 5.4, wobble: 0.2 });
    }
  } else if (item === 'birds') {
    gull(add, sx + side * 4, skyMid - 3, rnd.range(12, 14));
    gull(add, sx - side * 20, skyMid + rnd.range(8, 11), rnd.range(8, 9.5));
  } else if (item === 'curl') {
    curl(add, rnd, b, skyMid + 11);
  } else if (item === 'sunset') {
    const x = b.cx + rnd.range(-2, 8), r = rnd.range(15, 18), pts = [];
    for (let a = 180; a <= 360; a += 12) pts.push(arcPoint(x, horizon, r, a));
    add(pts);
  } else if (item === 'snow') {
    const spots = [[-18, -9], [5, -15], [21, 0], [-7, 7], [12, 16]];
    for (const [dx, dy] of spots.slice(0, rnd.int(4, 5))) dot(add, b.cx + dx + rnd.range(-2, 2), skyMid + dy + rnd.range(-2, 2), 8.5);
  } else if (item === 'breeze') {
    // Two long strokes across the glass, like wind or a reflection.
    const tilt = rnd.range(-0.45, -0.3), x = b.x0 + b.w * 0.35;
    add([[x, skyMid + 4], [x + b.w * 0.55, skyMid + 4 + b.w * 0.55 * tilt]], { width: 5.4 });
    add([[x + 10, skyMid + 14], [x + 10 + b.w * 0.35, skyMid + 14 + b.w * 0.35 * tilt]], { width: 5.4 });
  }

  // --- ground --------------------------------------------------------------
  const L = b.x0 - 8, R = b.x1 + 8;
  if (ground === 'sea') {
    add([[L, horizon], [R, horizon + rnd.range(-1, 1)]]);
    const x = b.x0 + b.w * rnd.range(0.3, 0.45), y = horizon + rnd.range(9, 11);
    add([[x, y], [x + b.w * rnd.range(0.25, 0.35), y]], { width: 5.4 });
    if (['sun', 'cloud', 'birds', 'moon', 'breeze'].includes(item) && rnd.chance(0.4)) {
      sail(add, rnd, b.cx - side * b.w * 0.25 - 4, horizon);
    }
  } else if (ground === 'hills') {
    const peak = lamp ? b.cx - side * b.w * 0.3 : b.x0 + b.w * rnd.range(0.3, 0.7), pts = [];
    for (let x = L; x <= R; x += 4) {
      const t = (x - peak) / (b.w * 0.7);
      pts.push([x, horizon - rise * Math.exp(-t * t * 2)]);
    }
    add(pts);
  } else if (ground === 'mountains') {
    // A tall peak away from the sky item, a lower one towards it.
    const high = b.cx - side * b.w * rnd.range(0.12, 0.22), low = b.cx + side * b.w * rnd.range(0.12, 0.2);
    const peaks = [[high, horizon - rise], [(high + low) / 2, horizon - rise * 0.3], [low, horizon - rise * 0.55]];
    peaks.sort((a, c) => a[0] - c[0]);
    add([[L, horizon + 8], ...peaks, [R, horizon + 6]], { wobble: 0.5 });
  }
  return lines;
}

// ---------------------------------------------------------------------------
// Scene elements. Each draws with add(points, options).

function circle(add, rnd, x, y, r) {
  add(loop(x, y, r, rnd));
}

function dot(add, x, y, width = 8) {
  add([[x, y]], { width });
}

function sparkle(add, x, y, s) {
  add([[x - s, y], [x + s, y]], { width: 5, wobble: 0 });
  add([[x, y - s], [x, y + s]], { width: 5, wobble: 0 });
}

function moon(add, rnd, x, y, r) {
  const flip = rnd.chance(0.5) ? 1 : -1;
  const pts = [];
  for (let a = 55; a <= 305; a += 10) {
    const p = arcPoint(0, 0, r, a);
    pts.push([x + p[0] * flip, y + p[1]]);
  }
  // Inner curve back from one horn to the other.
  const h1 = arcPoint(0, 0, r, 305), h2 = arcPoint(0, 0, r, 55), mx = -r * 0.2;
  for (let t = 0.1; t <= 1.001; t += 0.1) {
    const px = (1 - t) * (1 - t) * h1[0] + 2 * (1 - t) * t * mx + t * t * h2[0];
    const py = (1 - t) * (1 - t) * h1[1] + t * t * h2[1];
    pts.push([x + px * flip, y + py]);
  }
  add(pts);
}

function cloud(add, rnd, x, y, w) {
  // Flat base with two bumps, the second taller.
  const r1 = w * rnd.range(0.2, 0.24), r2 = w / 2 - r1;
  const left = x - w / 2;
  const pts = [[left - 2, y]];
  for (let a = 180; a <= 330; a += 15) pts.push(arcPoint(left + r1, y - 1, r1, a));
  for (let a = 200; a <= 360; a += 15) pts.push(arcPoint(left + 2 * r1 + r2 - 2, y - 1, r2, a));
  pts.push([x + w / 2, y + 0.5], [left + 3, y + 0.5]);
  add(pts);
}

// The cursive cloud from the original sketch, reduced to one curl: a line in
// from the right edge that loops back on itself.
function curl(add, rnd, b, y) {
  const cx = b.x0 + b.w * rnd.range(0.32, 0.4), s = rnd.range(2, 2.3);
  add([
    [b.x1 + 8, y], [cx + 4 * s, y], [cx - 5 * s, y - 0.5], [cx - 8 * s, y - 4 * s],
    [cx - 5 * s, y - 8 * s], [cx + 4 * s, y - 9 * s], [cx + 16 * s, y - 10 * s],
  ]);
}

function gull(add, x, y, s) {
  add([[x - s, y + s * 0.2], [x - s * 0.5, y - s * 0.45], [x, y + s * 0.25], [x + s * 0.5, y - s * 0.45], [x + s, y + s * 0.2]], { width: 5.4, wobble: 0.2 });
}

function sail(add, rnd, x, horizon) {
  const h = rnd.range(16, 19), w = h * 0.6;
  add([[x, horizon - 1], [x + w * 0.4, horizon - h], [x + w, horizon - 1]], { width: 5.4, wobble: 0.2 });
}

function loop(x, y, r, rnd) {
  const pts = [], start = rnd.range(0, 360), over = rnd.range(15, 35);
  for (let a = 0; a <= 360 + over; a += 20) {
    const k = 1 + (a > 360 ? 0.07 : 0);
    pts.push(arcPoint(x, y, r * k, start + a));
  }
  return pts;
}
