// Shared state and the small building blocks every episode uses.
//
// * `store` is everything we remember in this browser: which episodes are
//   finished, Play vs Read, and "Your setup" (server, app name, nonce, slug)
//   which fills in every <Say> box.
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
    nonce: '',
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
    if (saved.setup) Object.assign(store.setup, saved.setup)
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

// "Say this to Claude". The text is written straight into the markdown;
// [app], [nonce], [slug] and [server] fill in from Your setup, and any other
// [bracketed words] become a highlighted blank for the reader to fill.
const FILLS: Record<string, string> = { app: 'app name', nonce: 'app ID', slug: 'web path', server: 'server' }

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
      try {
        await navigator.clipboard.writeText(text.value!.innerText.trim())
        say('Copied. Paste it to ' + props.to + '.')
      } catch {
        say('Copy blocked: select the text instead')
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
