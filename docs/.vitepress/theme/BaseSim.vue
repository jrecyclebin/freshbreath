<script setup lang="ts">
// Episode 5: how Fresh Breath serves an app's pages, step by step, the way
// handler.go does it: a real file is served as is; otherwise the nearest
// index.html at or above the path answers; a folder asked for without its
// trailing slash is redirected; and a page answering for a deeper folder
// gets <base href> pointed at its own folder, slot and all (unless it
// brings its own <base>). Then it shows where the page's links land.
import { computed, reactive, ref, watch } from 'vue'
import { useBeat, BrowserFrame } from './kit'

const SLUG = 'tools'
const FILES = ['index.html', 'app.js', 'email/index.html', 'email/app.js', 'email/style.css']
const PRESETS = ['/tools/', '/tools/email/', '/tools/email/review/42', '/tools@staging/email/review/42', '/tools/email', '/tools/settings/billing']

const path = ref('/tools/email/review/42')
const ownBase = ref(false)
const seen = reactive({ base: false, plain: false, redirect: false, rootPath: false })
const beat = useBeat()

const dirname = (p: string) => (p.includes('/') ? p.slice(0, p.lastIndexOf('/')) : '.')

const trace = computed(() => {
  const steps: { text: string, kind?: string }[] = []
  const m = path.value.trim().match(/^\/([a-z0-9-]+)(@(dev|staging|prod))?(\/.*)?$/)
  if (!m || m[1] !== SLUG) return { steps: [{ text: `Only /${SLUG} lives here. Try one of the buttons.`, kind: 'bad' }] }
  const slot = m[3] ? '@' + m[3] : ''
  const mount = '/' + SLUG + slot
  const slotName = ({ '': 'the default slot', '@dev': 'Development', '@staging': 'Staging', '@prod': 'Production' } as any)[slot]
  steps.push({ text: `App “${SLUG}”, ${slotName}.` })
  if (!m[4]) return { steps: [...steps, { text: `No trailing slash on the app itself → redirect to ${mount}/`, kind: 'meh' }], redirect: mount + '/' }
  const raw = m[4]
  const trailing = raw.endsWith('/')
  const clean = raw.split('/').filter(Boolean).join('/') || '.'

  if (clean !== '.' && FILES.includes(clean)) {
    return { steps: [...steps, { text: `${clean} is a file → served as is.`, kind: 'ok' }], file: clean, mount }
  }
  steps.push({ text: clean === '.' ? 'The app’s root folder.' : `“${clean}” isn’t a file. Look for the nearest index.html at or above it:` })
  let dir = clean
  while (true) {
    const candidate = dir === '.' ? 'index.html' : dir + '/index.html'
    const found = FILES.includes(candidate)
    if (clean !== '.') steps.push({ text: `${found ? '✓' : '✗'} ${candidate}`, kind: found ? 'ok' : 'dim' })
    if (found) break
    if (dir === '.') break
    dir = dirname(dir)
  }
  if (dir === clean && dir !== '.' && !trailing) {
    return { steps: [...steps, { text: `That’s a folder with its own index.html, asked for without the slash → redirect to ${path.value}/ so its relative links resolve.`, kind: 'meh' }], redirect: path.value + '/' }
  }
  const page = dir === '.' ? 'index.html' : dir + '/index.html'
  const reqDir = trailing ? clean : dirname(clean)
  let base: string | null = null
  const own = ownBase.value && page === 'email/index.html' ? '/' + SLUG + '/email/' : null
  if (own) {
    steps.push({ text: `${page} brings its own <base href="${own}">, so Fresh Breath leaves it alone. Note it names no slot.`, kind: 'meh' })
  } else if (reqDir !== dir) {
    base = mount + '/' + (dir === '.' ? '' : dir + '/')
    steps.push({ text: `${page} is answering for “${reqDir}”, a deeper folder than its own → add <base href="${base}"> as the first thing in <head>.`, kind: 'ok' })
  } else {
    steps.push({ text: `${page} is answering for its own folder → served unchanged.`, kind: 'ok' })
  }
  return { steps, page, base, own, mount, slot, plain: reqDir === dir && !own }
})

// Where the page's links end up, resolved the way a browser would.
const links = computed(() => {
  const t: any = trace.value
  if (!t.page) return []
  const origin = 'https://fb.yourteam.dev'
  const against = origin + (t.base || t.own || path.value)
  const pageDir = dirname(t.page) === '.' ? '' : dirname(t.page) + '/'
  return [`app.js`, `/${SLUG}/${pageDir}app.js`, '/frbr.js'].map((href) => {
    const url = new URL(href, against).pathname
    if (href === '/frbr.js') return { href, url, ok: true, why: 'Fresh Breath’s own path: always absolute, always there.' }
    const inSlot = url.startsWith(t.mount + '/')
    const file = inSlot ? url.slice(t.mount.length + 1) : ''
    if (inSlot && FILES.includes(file)) return { href, url, ok: true, why: 'This slot’s copy. ✓' }
    if (!inSlot && url.startsWith('/' + SLUG + '/')) return { href, url, ok: false, why: 'Loads the default slot’s copy, not this one’s. A root path names one slot.' }
    return { href, url, ok: false, why: 'Nothing there: 404.' }
  })
})

watch([trace, links], () => {
  const t: any = trace.value
  if (t.base) seen.base = true
  if (t.plain && t.page) seen.plain = true
  if (t.redirect) seen.redirect = true
  if (t.slot && links.value.some((l) => !l.ok)) seen.rootPath = true
  if (Object.values(seen).every(Boolean)) beat.pass('Base tags, understood')
}, { immediate: true })
</script>

<template>
  <div class="sim base-sim">
    <div class="sim-head">
      <b>Try it: which page answers?</b>
      <span class="spacer" />
      <label class="toggle"><input v-model="ownBase" type="checkbox"> email/index.html has its own &lt;base&gt;</label>
    </div>
    <div class="sim-body grid">
      <div>
        <div class="tree mono">
          <div class="dim">apps/…/web/  <span>(the “{{ SLUG }}” app)</span></div>
          <div v-for="f in FILES" :key="f" :class="{ on: (trace as any).page === f || (trace as any).file === f }">{{ f.includes('/') ? '  ' + f : f }}</div>
        </div>
        <div class="presets">
          <button v-for="p in PRESETS" :key="p" class="chip" :class="{ on: path === p }" @click="path = p">{{ p }}</button>
        </div>
        <input v-model="path" type="text" class="mono" spellcheck="false" style="width:100%;margin-top:8px">
      </div>
      <div>
        <ol class="trace">
          <li v-for="(s, i) in trace.steps" :key="i + s.text" :class="s.kind">{{ s.text }}</li>
        </ol>
        <BrowserFrame v-if="(trace as any).page" :url="'https://fb.yourteam.dev' + path" title="Tools" icon="🧰" accent="#0f8b8d">
          <div class="src mono">
            <div class="dim">&lt;head&gt;</div>
            <div v-if="(trace as any).base" class="added">  &lt;base href="{{ (trace as any).base }}"&gt;  <em>← added</em></div>
            <div v-else-if="(trace as any).own">  &lt;base href="{{ (trace as any).own }}"&gt;  <em>← its own</em></div>
            <div v-for="l in links" :key="l.href" :class="l.ok ? 'ok' : 'bad'">  &lt;script src="{{ l.href }}"&gt; → {{ l.url }} <em>{{ l.ok ? '✓' : '✗' }}</em></div>
          </div>
          <ul class="why">
            <li v-for="l in links" :key="l.href" :class="l.ok ? 'ok' : 'bad'"><code>{{ l.href }}</code> {{ l.why }}</li>
          </ul>
        </BrowserFrame>
      </div>
    </div>
    <ul class="quests">
      <li class="quests-head">Quests</li>
      <li :class="{ got: seen.base }">Find a URL that gets a <code>&lt;base&gt;</code> added</li>
      <li :class="{ got: seen.plain }">Find one served for its own folder, with nothing added</li>
      <li :class="{ got: seen.redirect }">Find one that gets redirected</li>
      <li :class="{ got: seen.rootPath }">Visit a slot and watch a root path like <code>/tools/email/app.js</code> fetch the wrong copy</li>
    </ul>
  </div>
</template>

<style scoped>
.grid { display: grid; grid-template-columns: minmax(0, .8fr) minmax(0, 1.2fr); gap: 14px; align-items: start }
.tree { background: var(--code-bg); color: var(--code-ink); border-radius: 8px; padding: 10px 12px; font-size: 12.5px; line-height: 1.7; white-space: pre }
.tree .dim span { color: #7d8696 }
.tree .on { color: var(--ph); font-weight: 600 }
.tree .on::after { content: "  ◀" }
.presets { display: flex; flex-wrap: wrap; gap: 5px; margin-top: 10px }
.presets .chip { font-family: var(--mono); font-size: 11.5px }
.chip.on { background: var(--cobalt); border-color: var(--cobalt); color: var(--surface) }
.toggle { display: inline-flex; gap: 6px; align-items: center; font-family: var(--mono); font-size: 12.5px; cursor: pointer }
.trace { margin: 0 0 12px !important; font-size: 14px }
.trace li { padding: 0 0 6px 36px !important; min-height: 0 !important }
.trace li::before { width: 24px !important; height: 24px !important; font-size: 11px !important }
.trace li::after { left: 11px !important; top: 26px !important }
.trace li.ok { color: var(--mint) }
.trace li.meh { color: var(--plum) }
.trace li.bad { color: var(--red) }
.trace li.dim { color: var(--ink-soft) }
.src { padding: 10px 12px; font-size: 12px; line-height: 1.7; white-space: pre; overflow-x: auto; background: var(--wa-tint); border-bottom: 1px solid var(--wa-line) }
.src .dim { color: var(--wa-soft) }
.src .added { color: #0f8b8d; font-weight: 600 }
.src em { font-style: normal; color: var(--wa-soft) }
.src .ok em { color: #1f8a5b }
.src .bad { color: #c23b4b }
.why { list-style: none; margin: 0; padding: 10px 12px !important; font-size: 13px; display: grid; gap: 4px }
.why li { margin: 0 }
.why li.bad { color: #c23b4b }
@media (max-width: 760px) { .grid { grid-template-columns: minmax(0, 1fr) } }
</style>
