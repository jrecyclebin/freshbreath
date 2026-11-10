// Shared state and the small building blocks every episode uses.
//
// * `store` is everything we remember in this browser: which episodes are
//   finished, Play vs Read, and "Your setup" (server, app name, web path)
//   which fills in every <Say> box. Prompts name the app; the MCP looks up
//   its nonce by name, so readers never need to copy one.
// * `Play` is the per-page tutorial state: which beat is on screen and which
//   beats hold a challenge the reader has finished. The Layout provides it,
//   <Beat> reads it, and simulations report into it through useBeat().
import {
  defineComponent, h, inject, provide, reactive, watch, ref, onMounted,
  watchEffect, type InjectionKey, type Ref
} from 'vue'

const KEY = 'frbr-docs-v1'

export const store = reactive({
  ready: false,
  mode: 'play' as 'play' | 'read',
  done: {} as Record<string, boolean>,
  setup: {
    server: 'http://localhost:9009',
    app: 'Tix',
    slug: 'tix'
  }
})

// Called once on the client. Reading storage after mount keeps the server
// render and the first client render identical.
export function loadStore() {
  if (store.ready) return
  try {
    const saved = JSON.parse(localStorage.getItem(KEY) || '{}')
    if (saved.mode) store.mode = saved.mode
    if (saved.done) store.done = saved.done
    if (saved.setup) {
      store.setup.server = saved.setup.server ?? store.setup.server
      store.setup.app = saved.setup.app ?? store.setup.app
      store.setup.slug = saved.setup.slug ?? store.setup.slug
    }
  } catch { /* private window or blocked storage: defaults are fine */ }
  store.ready = true
  watch(() => [store.mode, store.done, store.setup], () => {
    try {
      localStorage.setItem(KEY, JSON.stringify({ mode: store.mode, done: store.done, setup: store.setup }))
    } catch { /* nothing to do */ }
  }, { deep: true })
}

export function resetProgress() {
  store.done = {}
}

// A tiny toast, shared by everything.
export const toast = reactive({ text: '', shown: false, timer: 0 as any })
export function say(text: string) {
  toast.text = text
  toast.shown = true
  clearTimeout(toast.timer)
  toast.timer = setTimeout(() => (toast.shown = false), 2200)
}

// ---- Play state -----------------------------------------------------------

export interface Play {
  current: Ref<number>
  count: Ref<number>
  needs: Record<number, boolean>
  passed: Record<number, boolean>
}

export const PLAY: InjectionKey<Play> = Symbol('play')
const BEAT: InjectionKey<number> = Symbol('beat')

export function providePlay(count: Ref<number>): Play {
  const play: Play = { current: ref(0), count, needs: reactive({}), passed: reactive({}) }
  provide(PLAY, play)
  return play
}

// For simulations: `need()` says "this beat has a challenge", `pass()` says
// the reader finished it. Neither blocks anybody; Next just changes its tune.
export function useBeat() {
  const play = inject(PLAY, null)
  const index = inject(BEAT, -1)
  const passed = ref(false)
  onMounted(() => { if (play && index >= 0) play.needs[index] = true })
  return {
    passed,
    pass(message = 'Nailed it!') {
      if (passed.value) return
      passed.value = true
      if (play && index >= 0) play.passed[index] = true
      say('✨ ' + message)
    }
  }
}

// One screen of an episode. The markdown config wraps every `## ` section in
// one of these, so authors never write it by hand.
export const Beat = defineComponent({
  name: 'Beat',
  props: { index: { type: Number, required: true }, title: String },
  setup(props, { slots }) {
    provide(BEAT, props.index)
    const play = inject(PLAY, null)
    return () => {
      const visible = !play || store.mode === 'read' || play.current.value === props.index
      return h('section', {
        class: ['beat', { passed: play?.passed[props.index] }],
        'data-beat': props.index,
        style: visible ? undefined : 'display:none'
      }, slots.default?.())
    }
  }
})

// Copies text. The Clipboard API only works on HTTPS (or localhost) and in
// frames allowed to write the clipboard, so a hidden textarea and
// execCommand cover everywhere else.
export async function copyText(words: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(words)
    return true
  } catch { /* fall through */ }
  const area = document.createElement('textarea')
  area.value = words
  area.setAttribute('readonly', '')
  area.style.cssText = 'position:fixed;top:0;left:0;opacity:0;pointer-events:none'
  document.body.append(area)
  area.select()
  let ok = false
  try { ok = document.execCommand('copy') } catch { ok = false }
  area.remove()
  return ok
}

// "Say this to Claude". The text is written straight into the markdown;
// [app], [nonce], [slug] and [server] fill in from Your setup, and any other
// [bracketed words] become a highlighted blank for the reader to fill.
const FILLS: Record<string, string> = { app: 'app name', slug: 'web path', server: 'server' }

// The nonce shown in simulations. Real ones come from the control panel.
export const SAMPLE_NONCE = 'aB3xK9mQ2p'

// Fresh Breath's rule for an app slug (normalizeAppURL in the server): it's
// lowercased and trimmed of slashes, then must read as letters and digits
// joined by single dashes. Returns what's wrong, or '' when it's fine.
export const SLUG_RE = /^[a-z0-9]+(-[a-z0-9]+)*$/
export const RESERVED_SLUGS = ['api', 'control', 'mcp', 'service', 'oauth']
export function slugProblem(raw: string): string {
  const slug = raw.trim().toLowerCase().replace(/^\/+|\/+$/g, '')
  if (!slug) return ''
  if (slug.includes('/')) {
    const [first, ...rest] = slug.split('/')
    return `Apps can’t sit in subdirectories. Make a “${first}” app and publish into its “${rest.join('/')}” folder instead.`
  }
  if (!SLUG_RE.test(slug)) return 'A slug is letters, digits and single dashes, like “blog” or “team-tix”.'
  if (RESERVED_SLUGS.includes(slug)) return `“${slug}” is reserved by Fresh Breath. Pick another path for this app.`
  return ''
}

export const Say = defineComponent({
  name: 'Say',
  props: { to: { type: String, default: 'Claude' } },
  setup(props, { slots }) {
    const text = ref<HTMLElement>()
    const fills: { el: HTMLElement, key: string }[] = []

    onMounted(() => {
      const walker = document.createTreeWalker(text.value!, NodeFilter.SHOW_TEXT)
      const nodes: Text[] = []
      while (walker.nextNode()) nodes.push(walker.currentNode as Text)
      for (const node of nodes) {
        const parts = node.data.split(/(\[[^\]\n]{1,80}\])/)
        if (parts.length === 1) continue
        const frag = document.createDocumentFragment()
        for (const part of parts) {
          const m = part.match(/^\[(.+)\]$/)
          if (!m) { frag.append(part); continue }
          const span = document.createElement('span')
          span.className = 'ph'
          if (FILLS[m[1]]) fills.push({ el: span, key: m[1] })
          else span.textContent = m[1]
          frag.append(span)
        }
        node.replaceWith(frag)
      }
      watchEffect(() => {
        for (const f of fills) {
          const value = (store.setup as any)[f.key]
          f.el.textContent = value || FILLS[f.key]
          f.el.classList.toggle('filled', !!value)
        }
      })
    })

    async function copy() {
      const words = text.value!.innerText.trim()
      if (await copyText(words)) {
        say('Copied. Paste it to ' + props.to + '.')
      } else {
        // Last resort: select it, so one Ctrl/Cmd-C does the job.
        const range = document.createRange()
        range.selectNodeContents(text.value!)
        getSelection()?.removeAllRanges()
        getSelection()?.addRange(range)
        say('Selected. Press Ctrl/Cmd-C to copy.')
      }
    }

    return () => h('div', { class: 'say' }, [
      h('div', { class: 'say-head' }, [
        h('span', `Say this to ${props.to}`),
        h('button', { class: 'say-copy', onClick: copy }, 'Copy')
      ]),
      h('div', { class: 'say-text', ref: text }, slots.default?.())
    ])
  }
})

// A little browser window around a simulated web app: a title tab and an
// address field, no buttons. The slot is the page; give it the `webapp`
// look (or its own) so it reads as a real site inside the guide.
export const BrowserFrame = defineComponent({
  name: 'BrowserFrame',
  props: { url: { type: String, default: '' }, title: String, icon: { type: String, default: '🌐' }, accent: String },
  setup(props, { slots }) {
    return () => {
      const secure = props.url.startsWith('https://')
      const [, scheme, rest] = props.url.match(/^(\w+:\/\/)?(.*)$/) || []
      return h('div', { class: 'bframe', style: props.accent ? `--app: ${props.accent}` : undefined }, [
        h('div', { class: 'bframe-bar' }, [
          h('div', { class: 'bframe-tab' }, [h('span', { class: 'bframe-ic' }, props.icon), h('span', props.title || rest)]),
          h('div', { class: 'bframe-url' }, [
            h('span', { class: secure ? 'bframe-lock on' : 'bframe-lock' }, secure ? '🔒' : 'ⓘ'),
            h('span', { class: 'bframe-scheme' }, scheme || ''),
            h('span', rest)
          ])
        ]),
        h('div', { class: 'bframe-page webapp' }, slots.default?.())
      ])
    }
  }
})
