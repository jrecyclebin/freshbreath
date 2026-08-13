// Compiles the control panel JSX source (web/control/app.js) to plain JS
// (web/control/app.compiled.js) for distribution builds. In dev the page
// compiles app.js in-browser via the same vendored babel; scripts/build.sh
// runs this before packaging a dist and swaps the HTML to the compiled app.
// Run from the repo root: node scripts/compile-control.mjs
import { readFileSync, writeFileSync } from "node:fs";
import { createRequire } from "node:module";

const require = createRequire(import.meta.url);
const Babel = require("../web/control/vendor/babel-standalone-7.29.0.min.js");

const SRC = "web/control/app.js";
const OUT = "web/control/app.compiled.js";

const src = readFileSync(SRC, "utf8");
const header = "// Generated from app.js by scripts/compile-control.mjs — do not edit directly.\n";
const { code } = Babel.transform(src, {
  presets: ["react"], // classic runtime: JSX becomes React.createElement (React UMD global)
  sourceType: "script",
  filename: SRC,
});
writeFileSync(OUT, header + code);
console.log(`→ ${OUT} (${code.length} bytes)`);
