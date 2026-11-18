// window-avatar.js — Fresh Breath avatars
// A small flat-color "window with a view" avatar, generated as an SVG string
// from any string (typically a user's e-mail address). Chillwave palette:
// teal, hot pink and violet. Drawn to read at 30px.
//
//   import { windowAvatar } from './fresh-breath-avatar.js';
//   el.innerHTML = windowAvatar('someone@example.com', { size: 30 });
//
// The same input always gives the same picture. No dependencies.

const COLORS = {
  frame: '#1e3446', reveal: '#3b8796',
  // A few skies for each time of day; the hash picks one. The last day sky is
  // the palest, so white clouds and sails skip it.
  sky: {
    day: ['#8edbd4', '#9cc6f0', '#c4b3ef'],
    dusk: ['#ffa3a8', '#ffb685', '#e791c8'],
    night: ['#2a2259', '#152f50'],
  },
  sun: '#ff5f8a', duskSun: '#ffe07a', moon: '#fde8f0', cloud: '#ffffff',
  land: '#4d4890', sea: '#2fa3b6',
};

export function windowAvatar(email, options = {}) {
  const size = options.size ?? 30;
  const key = String(email).trim().toLowerCase();
  const seed = hashString(key);
  const rnd = random(seed);
  const uid = 'fb' + seed.toString(36);

  const frame = makeFrame(rnd);
  const scene = composeScene(rnd, frame.bounds);

  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100" width="${size}" height="${size}" role="img">`
    + `<defs><clipPath id="${uid}c"><path d="${poly(frame.inner)}"/></clipPath></defs>`
    + `<g clip-path="url(#${uid}c)">${scene}</g>`
    + `<path d="${poly(frame.reveal)}" fill="${COLORS.reveal}"/>`
    + frame.draw(COLORS.frame)
    + `</svg>`;
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
// Geometry helpers

const f = n => n.toFixed(1);
const poly = pts => 'M' + pts.map(p => f(p[0]) + ' ' + f(p[1])).join('L') + 'Z';
const line = pts => 'M' + pts.map(p => f(p[0]) + ' ' + f(p[1])).join('L');

function arcPoint(cx, cy, r, deg) {
  const a = (deg * Math.PI) / 180;
  return [cx + r * Math.cos(a), cy + r * Math.sin(a)];
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

// Smooth curve through points (Catmull-Rom), as path commands after an M.
function smooth(pts) {
  let d = '';
  for (let i = 0; i < pts.length - 1; i++) {
    const p0 = pts[Math.max(0, i - 1)], p1 = pts[i], p2 = pts[i + 1], p3 = pts[Math.min(pts.length - 1, i + 2)];
    d += `C${f(p1[0] + (p2[0] - p0[0]) / 6)} ${f(p1[1] + (p2[1] - p0[1]) / 6)} ${f(p2[0] - (p3[0] - p1[0]) / 6)} ${f(p2[1] - (p3[1] - p1[1]) / 6)} ${f(p2[0])} ${f(p2[1])}`;
  }
  return d;
}

// ---------------------------------------------------------------------------
// Frames: arch, pointed arch, porthole or square, traced clockwise from the
// bottom-left. The reveal (the window's depth) is a lighter wedge down the
// left side that tapers away over the top, as in the ink version.

function makeFrame(rnd) {
  const kind = rnd.weighted({ arch: 40, round: 22, square: 24, pointed: 14 });
  let outline, closed = false;

  if (kind === 'arch') {
    const w = 39, cx = 50, bottom = 90, spring = bottom - rnd.range(40, 44);
    outline = [[cx - w, bottom], [cx - w, spring]];
    for (let a = 180; a <= 360; a += 6) outline.push(arcPoint(cx, spring, w, a));
    outline.push([cx + w, bottom]);
  } else if (kind === 'pointed') {
    const w = 38, cx = 50, bottom = 90, R = w * 1.12, spring = bottom - rnd.range(38, 42);
    const top = (Math.acos((w - R) / R) * 180) / Math.PI;
    const left = [];
    for (let a = 180; a <= 360 - top; a += 6) left.push(arcPoint(cx - w + R, spring, R, a));
    left.push([cx, spring - Math.sqrt(R * R - (R - w) * (R - w))]);
    const right = left.slice(0, -1).reverse().map(([x, y]) => [2 * cx - x, y]);
    outline = [[cx - w, bottom], ...left, ...right, [cx + w, bottom]];
  } else if (kind === 'round') {
    const start = rnd.range(115, 130);
    outline = [];
    for (let a = start; a < start + 360; a += 6) outline.push(arcPoint(50, 50, 42, a));
    closed = true;
  } else {
    const r = rnd.range(4, 8), x0 = 10, x1 = 90, y0 = 10, y1 = 89;
    outline = [[x0, y1]];
    for (let a = 180; a <= 270; a += 15) outline.push(arcPoint(x0 + r, y0 + r, r, a));
    for (let a = 270; a <= 360; a += 15) outline.push(arcPoint(x1 - r, y0 + r, r, a));
    outline.push([x1, y1]);
  }

  outline = resample(outline, 2);
  const depth = rnd.range(9, 11);
  const revealEnd = Math.floor(outline.length * rnd.range(0.5, 0.6));
  // A porthole has no sill to hide where the wedge starts, so it fades in.
  const fadeIn = closed ? Math.floor(revealEnd * 0.3) : 0;
  const d = i => {
    if (i >= revealEnd) return 0;
    const taper = depth * Math.pow(1 - i / revealEnd, 0.7);
    return i < fadeIn ? taper * Math.sin((i / fadeIn) * Math.PI / 2) : taper;
  };
  const inner = outline.map((p, i) => {
    const a = outline[Math.max(0, i - 1)], b = outline[Math.min(outline.length - 1, i + 1)];
    const len = Math.hypot(b[0] - a[0], b[1] - a[1]) || 1;
    const nx = -(b[1] - a[1]) / len, ny = (b[0] - a[0]) / len;
    return [p[0] + nx * d(i), p[1] + ny * d(i)];
  });
  const reveal = outline.slice(0, revealEnd + 1).concat(inner.slice(0, revealEnd + 1).reverse());

  const draw = color => {
    const stroke = `fill="none" stroke="${color}" stroke-width="7.5" stroke-linejoin="round" stroke-linecap="round"`;
    if (closed) return `<path d="${poly(outline)}" ${stroke}/>`;
    const sillY = outline[0][1] + 1;
    return `<path d="${line(outline)}" ${stroke}/>`
      + `<rect x="${f(outline[0][0] - 6)}" y="${f(sillY - 3)}" width="${f(outline[outline.length - 1][0] - outline[0][0] + 12)}" height="7" rx="3" fill="${color}"/>`;
  };

  const xs = inner.map(p => p[0]), ys = inner.map(p => p[1]);
  const bounds = { x0: Math.min(...xs), x1: Math.max(...xs), y0: Math.min(...ys), y1: Math.max(...ys) };
  bounds.w = bounds.x1 - bounds.x0;
  bounds.h = bounds.y1 - bounds.y0;
  bounds.cx = (bounds.x0 + bounds.x1) / 2;
  bounds.skyTop = bounds.y0 + (kind === 'square' ? 8 : kind === 'pointed' ? 22 : 18);
  return { inner, reveal, draw, bounds };
}

// ---------------------------------------------------------------------------
// Scenes, in the spirit of the ink version: one flat sky color, one thing in
// the sky, a simple piece of ground, and sometimes one silhouette in front.

const SKY = { sun: 18, moon: 14, cloud: 14, sunset: 12, birds: 10, rain: 8, stars: 7, twinmoons: 1.5 };

function composeScene(rnd, b) {
  let item = rnd.weighted(SKY);
  let ground = rnd.weighted({ sea: 20, shimmer: 13, hills: 18, peaks: 9, ranges: 11, mesa: 9, desert: 8, none: 12 });
  // About a third of windows get a foreground silhouette. It shares the
  // window only with a plain sun or moon, so the picture stays sparse.
  const front = rnd.weighted({ none: 64, palm: 12, saguaro: 12, sail: 12 });
  if (front !== 'none') {
    if (!['sun', 'sunset', 'moon', 'twinmoons'].includes(item)) item = rnd.pick(['sun', 'sunset', 'moon']);
    ground = front === 'saguaro' ? rnd.pick(['hills', 'mesa', 'desert']) : rnd.pick(['sea', 'shimmer']);
  }
  if (item === 'sunset' && !['sea', 'shimmer', 'desert'].includes(ground)) ground = front === 'saguaro' ? 'desert' : 'sea';
  // Tall land crowds the sky, so it only goes with small sky things.
  if (['peaks', 'ranges', 'mesa'].includes(ground) && !['sun', 'moon', 'stars', 'twinmoons'].includes(item)) ground = rnd.pick(['sea', 'shimmer', 'hills']);
  // A round thing floating in an empty window reads as an eye; give it a horizon.
  if (ground === 'none' && ['sun', 'moon', 'stars', 'twinmoons'].includes(item)) ground = rnd.pick(['sea', 'shimmer', 'hills']);

  const time = item === 'moon' || item === 'stars' || item === 'twinmoons' ? 'night'
    : item === 'sunset' ? 'dusk'
    : item === 'rain' ? 'day'
    : rnd.chance(0.35) ? 'dusk' : 'day';

  const L = b.x0 - 10, R = b.x1 + 10, B = b.y1 + 10;
  const horizon = ground === 'none' ? B : b.y0 + b.h * rnd.range(0.66, 0.74);
  const rise = { sea: 0, shimmer: 0, hills: 10, peaks: 20, ranges: 22, mesa: 16, desert: 0, none: 0 }[ground];
  const skyMid = (b.skyTop + horizon - rise - 6) / 2;
  const side = rnd.pick([-1, 1, 1]);
  // A sun or moon straight above a hump reads as a head and shoulders, so
  // those sit further out, over the low side of the land.
  const lamp = item === 'sun' || item === 'moon' || item === 'twinmoons';
  const sx = b.cx + side * b.w * (lamp && ground !== 'sea' ? rnd.range(0.12, 0.16) : rnd.range(0.04, 0.1));
  const sunColor = time === 'dusk' ? COLORS.duskSun : COLORS.sun;

  const skies = COLORS.sky[time];
  const sky = rnd.pick(['cloud', 'rain'].includes(item) || front === 'sail' ? skies.slice(0, 2) : skies);
  let out = `<rect x="${f(L)}" y="${f(b.y0 - 10)}" width="${f(R - L)}" height="${f(B - b.y0 + 10)}" fill="${sky}"/>`;
  const fill = (d, color) => { out += `<path d="${d}" fill="${color}"/>`; };
  const stroke = (d, color, width) => { out += `<path d="${d}" fill="none" stroke="${color}" stroke-width="${width}" stroke-linecap="round" stroke-linejoin="round"/>`; };
  const circle = (x, y, r, color) => { out += `<circle cx="${f(x)}" cy="${f(y)}" r="${f(r)}" fill="${color}"/>`; };

  // --- sky -----------------------------------------------------------------
  if (item === 'sun') {
    circle(sx, skyMid + rnd.range(0, 3), rnd.range(11, 13), sunColor);
  } else if (item === 'sunset') {
    // Drawn as a half disc so it also sits right on an unfilled horizon.
    const x = b.cx + rnd.range(-3, 8), r = rnd.range(15, 18);
    fill(`M${f(x - r)} ${f(horizon)}A${f(r)} ${f(r)} 0 0 1 ${f(x + r)} ${f(horizon)}Z`, sunColor);
  } else if (item === 'moon') {
    moon(fill, rnd.pick([-1, 1]), sx, skyMid + 1, rnd.range(11, 13), COLORS.moon);
    if (rnd.chance(0.5)) circle(sx - side * 24, skyMid - rnd.range(8, 11), 2.6, COLORS.moon);
  } else if (item === 'twinmoons') {
    // Rare: a second, smaller moon in the same phase, higher and further over.
    const flip = rnd.pick([-1, 1]);
    moon(fill, flip, sx, skyMid + 3, rnd.range(10.5, 12), COLORS.moon);
    moon(fill, flip, sx - side * rnd.range(20, 24), skyMid - rnd.range(9, 12), rnd.range(5.5, 6.5), COLORS.moon);
  } else if (item === 'stars') {
    // On a diagonal: two marks side by side read as eyes.
    const x = b.cx + side * 12, y = skyMid - 7, s = 6.5;
    stroke(`M${f(x - s)} ${f(y)}H${f(x + s)}M${f(x)} ${f(y - s)}V${f(y + s)}`, COLORS.moon, 4);
    circle(b.cx - side * 6, skyMid + 9, 3.2, COLORS.moon);
  } else if (item === 'cloud' || item === 'rain') {
    const x = item === 'rain' ? b.cx + 2 : sx, y = item === 'rain' ? b.skyTop + 18 : skyMid + 3;
    cloud(fill, rnd, x, y, item === 'rain' ? 44 : rnd.range(44, 50), COLORS.cloud);
    if (item === 'rain') {
      const d = [-12, 2, 16].map(dx => `M${f(x + dx - 2)} ${f(y + 8)}l-3 12`).join('');
      stroke(d, COLORS.frame, 4.2);
    }
  } else if (item === 'birds') {
    const gull = (x, y, s) => `M${f(x - s)} ${f(y)}Q${f(x - s / 2)} ${f(y - s * 0.7)} ${f(x)} ${f(y + s * 0.25)}Q${f(x + s / 2)} ${f(y - s * 0.7)} ${f(x + s)} ${f(y)}`;
    stroke(gull(sx + side * 4, skyMid - 3, rnd.range(11, 13)) + gull(sx - side * 18, skyMid + rnd.range(8, 11), rnd.range(7.5, 9)), COLORS.frame, 4.2);
  }

  // --- ground --------------------------------------------------------------
  if (ground === 'sea') {
    fill(`M${f(L)} ${f(horizon)}H${f(R)}V${f(B)}H${f(L)}Z`, COLORS.sea);
    // One glint under a sun or moon.
    if (['sun', 'sunset', 'moon', 'twinmoons'].includes(item)) {
      const gx = item === 'sunset' ? b.cx + 2 : sx, w = time === 'night' ? 10 : 16;
      stroke(`M${f(gx - w / 2)} ${f(horizon + 8)}h${f(w)}`, time === 'night' ? COLORS.moon : sunColor, 3.6);
    }
  } else if (ground === 'shimmer') {
    // Open water drawn with two strokes: the horizon and a shorter line below.
    stroke(`M${f(L)} ${f(horizon)}H${f(R)}`, COLORS.sea, 4.6);
    const x = b.x0 + b.w * rnd.range(0.3, 0.45);
    stroke(`M${f(x)} ${f(horizon + rnd.range(9, 12))}h${f(b.w * rnd.range(0.25, 0.35))}`, COLORS.sea, 4.6);
  } else if (ground === 'ranges') {
    // Two mountains, the far one paler, as if seen through haze.
    const far = b.cx - side * b.w * rnd.range(0.05, 0.15), near = b.cx + side * b.w * rnd.range(0.2, 0.3);
    fill(`M${f(far - 42)} ${f(horizon + 4)}L${f(far)} ${f(horizon - rise)}L${f(far + 42)} ${f(horizon + 4)}V${f(B)}H${f(far - 42)}Z`, mix(COLORS.land, sky, 0.5));
    fill(`M${f(near - 36)} ${f(horizon + 12)}L${f(near)} ${f(horizon - rise * 0.45)}L${f(near + 36)} ${f(horizon + 12)}V${f(B)}H${f(near - 36)}Z`, COLORS.land);
    fill(`M${f(L)} ${f(horizon + 8)}H${f(R)}V${f(B)}H${f(L)}Z`, COLORS.land);
  } else if (ground === 'hills') {
    const peak = lamp ? b.cx - side * b.w * 0.3 : b.x0 + b.w * rnd.range(0.3, 0.7);
    fill(swell(L, R, B, peak, horizon - rise, horizon + 2, b.w * 0.6), COLORS.land);
  } else if (ground === 'peaks') {
    const high = b.cx - side * b.w * rnd.range(0.12, 0.2), low = b.cx + side * b.w * rnd.range(0.14, 0.2);
    const pts = [[high, horizon - rise], [(high + low) / 2, horizon - rise * 0.35], [low, horizon - rise * 0.6]].sort((a, c) => a[0] - c[0]);
    fill(`M${f(L)} ${f(horizon + 6)}` + pts.map(p => `L${f(p[0])} ${f(p[1])}`).join('') + `L${f(R)} ${f(horizon + 4)}V${f(B)}H${f(L)}Z`, COLORS.land);
  } else if (ground === 'mesa') {
    const mx = b.cx - side * b.w * rnd.range(0.1, 0.18);
    fill(mesa(mx, horizon + 1, rnd.range(22, 28), rise), COLORS.land);
    fill(`M${f(L)} ${f(horizon)}H${f(R)}V${f(B)}H${f(L)}Z`, COLORS.land);
  } else if (ground === 'desert') {
    fill(`M${f(L)} ${f(horizon)}H${f(R)}V${f(B)}H${f(L)}Z`, COLORS.land);
  }

  // --- foreground silhouette, on the side away from the sun or moon ---------
  // Against a night sky a dark silhouette vanishes, so it is lifted a little.
  const shade = time === 'night' ? mix(COLORS.frame, COLORS.moon, 0.4) : COLORS.frame;
  const fx = b.cx - side * b.w * (item === 'sunset' ? 0.26 : 0.2);
  if (front === 'palm') {
    out += palm(fx, B, horizon - rnd.range(26, 32), -side * rnd.range(6, 9), shade);
  } else if (front === 'saguaro') {
    out += saguaro(fx, horizon + rnd.range(12, 15), rnd.range(24, 28), side, time === 'night' ? shade : mix(COLORS.land, COLORS.frame, 0.6));
  } else if (front === 'sail') {
    const x = fx + side * 4, h = rnd.range(15, 18), w = h * 0.68;
    fill(`M${f(x - w / 2)} ${f(horizon - 3)}L${f(x + w * 0.1)} ${f(horizon - 3 - h)}L${f(x + w / 2)} ${f(horizon - 3)}Z`, COLORS.cloud);
    fill(`M${f(x - w * 0.75)} ${f(horizon - 2)}H${f(x + w * 0.75)}L${f(x + w * 0.5)} ${f(horizon + 2.5)}H${f(x - w * 0.5)}Z`, COLORS.frame);
  }
  return out;
}

// ---------------------------------------------------------------------------
// Scene pieces

// Blend two hex colors; t = 0 gives a, t = 1 gives b.
function mix(a, b, t) {
  const c = [1, 3, 5].map(i => Math.round(parseInt(a.substr(i, 2), 16) * (1 - t) + parseInt(b.substr(i, 2), 16) * t));
  return '#' + c.map(v => v.toString(16).padStart(2, '0')).join('');
}

function moon(fill, flip, x, y, r, color) {
  const pts = [];
  for (let a = 50; a <= 310; a += 10) {
    const p = arcPoint(0, 0, r, a);
    pts.push([x + p[0] * flip, y + p[1]]);
  }
  const h1 = arcPoint(0, 0, r, 310), h2 = arcPoint(0, 0, r, 50), mx = -r * 0.15;
  for (let t = 0.1; t < 1; t += 0.1) {
    const px = (1 - t) * (1 - t) * h1[0] + 2 * (1 - t) * t * mx + t * t * h2[0];
    const py = (1 - t) * (1 - t) * h1[1] + t * t * h2[1];
    pts.push([x + px * flip, y + py]);
  }
  fill(poly(pts), color);
}

function cloud(fill, rnd, x, y, w, color) {
  // Flat base with two bumps, the second taller.
  const r1 = w * rnd.range(0.2, 0.24), r2 = w / 2 - r1, left = x - w / 2, pts = [];
  for (let a = 180; a <= 330; a += 15) pts.push(arcPoint(left + r1, y - 1, r1, a));
  for (let a = 200; a <= 360; a += 15) pts.push(arcPoint(left + 2 * r1 + r2 - 2, y - 1, r2, a));
  pts.push([x + w / 2, y + 1], [left, y + 1]);
  fill(poly(pts), color);
}

function mesa(cx, base, w, h) {
  const top = base - h, slope = h * 0.45;
  return `M${f(cx - w / 2 - slope)} ${f(base)}L${f(cx - w / 2)} ${f(top + 1.5)}Q${f(cx - w / 2)} ${f(top)} ${f(cx - w / 2 + 2)} ${f(top)}`
    + `H${f(cx + w / 2 - 2)}Q${f(cx + w / 2)} ${f(top)} ${f(cx + w / 2)} ${f(top + 1.5)}L${f(cx + w / 2 + slope)} ${f(base)}Z`;
}

function swell(L, R, B, peakX, peakY, edgeY, halfWidth) {
  const pts = [];
  for (let x = L; x <= R + 0.1; x += 5) {
    const t = (x - peakX) / halfWidth;
    pts.push([x, edgeY - (edgeY - peakY) * Math.exp(-t * t * 1.6)]);
  }
  return `M${f(pts[0][0])} ${f(pts[0][1])}${smooth(pts)}V${f(B)}H${f(L)}Z`;
}

// A palm leaning over the scene: a curved trunk rising from below the sill
// and five drooping fronds.
function palm(x, base, top, lean, color) {
  const s = `stroke="${color}" stroke-linecap="round" fill="none"`;
  const tx = x + lean, ty = top;
  let out = `<path d="M${f(x)} ${f(base)}Q${f(x - lean * 0.3)} ${f((base + ty) / 2)} ${f(tx)} ${f(ty)}" stroke-width="4.6" ${s}/>`;
  for (const [ang, len] of [[195, 17], [230, 15], [270, 10], [310, 15], [345, 17]]) {
    const end = arcPoint(tx, ty, len, ang), ctrl = arcPoint(tx, ty - 5, len * 0.65, ang);
    out += `<path d="M${f(tx)} ${f(ty)}Q${f(ctrl[0])} ${f(ctrl[1])} ${f(end[0])} ${f(end[1] + 5)}" stroke-width="4" ${s}/>`;
  }
  return out;
}

// A saguaro: a thick trunk with one arm on each side, at different heights.
function saguaro(x, base, h, side, color) {
  const s = `stroke="${color}" stroke-linecap="round" stroke-linejoin="round" fill="none"`;
  return `<path d="M${f(x)} ${f(base)}V${f(base - h)}" stroke-width="6.5" ${s}/>`
    + `<path d="M${f(x)} ${f(base - h * 0.38)}H${f(x + side * 6)}V${f(base - h * 0.72)}" stroke-width="4.6" ${s}/>`
    + `<path d="M${f(x)} ${f(base - h * 0.52)}H${f(x - side * 5.5)}V${f(base - h * 0.8)}" stroke-width="4.6" ${s}/>`;
}
