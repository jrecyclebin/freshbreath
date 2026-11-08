<script setup lang="ts">
// The whole site shell. Episodes play one beat at a time (Play) or read as a
// normal page (Read); either way every beat has an anchor, so links and the
// Jump search can land anywhere.
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { useData, useRoute, useRouter, withBase } from 'vitepress'
import { data as pages } from './episodes.data'
import { store, loadStore, providePlay, resetProgress, toast, say, slugProblem } from './kit'

const { frontmatter, isDark, page } = useData()
const route = useRoute()
const router = useRouter()

const episodes = pages.filter((p) => p.episode)
const references = pages.filter((p) => p.reference)

const normalize = (path: string) => {
  let p = path.replace(/\.html$/, '').replace(/\/index$/, '/')
  return p === '' ? '/' : p
}
const here = computed(() => pages.find((p) => normalize(p.url) === normalize(route.path)))
const isHome = computed(() => frontmatter.value.layout === 'home')
const isEpisode = computed(() => !!here.value?.episode)
const beats = computed(() => here.value?.beats || [])
const count = computed(() => (frontmatter.value.beats === false ? 0 : beats.value.length))
const play = providePlay(count)
const playing = computed(() => isEpisode.value && count.value > 0 && store.mode === 'play')
const nextEpisode = computed(() => {
  const i = episodes.findIndex((e) => e === here.value)
  return i >= 0 ? episodes[i + 1] : undefined
})
const doneCount = computed(() => episodes.filter((e) => store.done[normalize(e.url)]).length)
const finished = ref(false)

// ---- Beat navigation -------------------------------------------------------

function showBeat(i: number, scroll = true) {
  play.current.value = Math.max(0, Math.min(i, count.value - 1))
  finished.value = false
  const id = beats.value[play.current.value]?.id
  if (id) history.replaceState(history.state, '', '#' + id)
  if (scroll) nextTick(() => document.querySelector('.episode-head')?.scrollIntoView({ block: 'start' }))
}

// Finishing an episode moves straight on: a shimmer sweeps the page, it
// slides away, and the next episode slides in. Only the last episode stops
// for a celebration, since there's nowhere left to go.
const leaving = ref(false)
const arriving = ref(false)

function finish() {
  if (!here.value) return
  store.done[normalize(here.value.url)] = true
  const next = nextEpisode.value
  if (!next) {
    finished.value = true
    nextTick(() => document.querySelector('.finish')?.scrollIntoView({ block: 'center', behavior: 'smooth' }))
    return
  }
  say(`✓ Episode ${here.value.episode} done`)
  leaving.value = true
  setTimeout(async () => {
    await router.go(withBase(next.url))
    leaving.value = false
    arriving.value = true
    window.scrollTo(0, 0)
    setTimeout(() => (arriving.value = false), 500)
  }, 780)
}

const slugIssue = computed(() => slugProblem(store.setup.slug))

function followHash() {
  finished.value = false
  const id = decodeURIComponent(location.hash.slice(1))
  const el = id && document.getElementById(id)
  const beat = el && (el.closest('[data-beat]') as HTMLElement | null)
  play.current.value = beat ? Number(beat.dataset.beat) : 0
  if (el && playing.value) nextTick(() => el.scrollIntoView({ block: 'start' }))
}

watch(() => route.path, () => {
  menuOpen.value = false
  nextTick(followHash)
})

// ---- Jump search -----------------------------------------------------------

const jumpOpen = ref(false)
const query = ref('')
const picked = ref(0)
const jumpInput = ref<HTMLInputElement>()

const results = computed(() => {
  const words = query.value.toLowerCase().split(/\s+/).filter(Boolean)
  const all = pages.flatMap((p) => [
    { url: p.url, label: p.title, where: p.episode ? `Episode ${p.episode}` : 'Reference', icon: p.icon, hay: (p.title + ' ' + p.blurb + ' ' + p.short).toLowerCase(), weight: 3 },
    ...p.beats.map((b) => ({ url: p.url + '#' + b.id, label: b.title, where: p.title, icon: '↳', hay: (b.title + ' ' + b.title + ' ' + b.text).toLowerCase(), weight: 1 }))
  ])
  if (!words.length) return all.filter((r) => r.weight === 3)
  return all
    .map((r) => {
      if (!words.every((w) => r.hay.includes(w))) return null
      const inTitle = words.filter((w) => r.label.toLowerCase().includes(w)).length
      return { ...r, score: inTitle * 10 + r.weight }
    })
    .filter(Boolean)
    .sort((a: any, b: any) => b.score - a.score)
    .slice(0, 12) as any[]
})

function openJump() {
  jumpOpen.value = true
  query.value = ''
  picked.value = 0
  nextTick(() => jumpInput.value?.focus())
}

function go(url: string) {
  jumpOpen.value = false
  menuOpen.value = false
  const [path, hash] = url.split('#')
  if (normalize(path) === normalize(route.path) && hash) {
    history.replaceState(history.state, '', '#' + hash)
    followHash()
    if (!playing.value) document.getElementById(hash)?.scrollIntoView({ block: 'start' })
  } else {
    router.go(withBase(url))
  }
}

function onJumpKey(e: KeyboardEvent) {
  if (e.key === 'ArrowDown') { picked.value = Math.min(picked.value + 1, results.value.length - 1); e.preventDefault() }
  if (e.key === 'ArrowUp') { picked.value = Math.max(picked.value - 1, 0); e.preventDefault() }
  if (e.key === 'Enter' && results.value[picked.value]) go(results.value[picked.value].url)
  if (e.key === 'Escape') jumpOpen.value = false
}
watch(query, () => (picked.value = 0))

// ---- Keys, menu, setup -----------------------------------------------------

const menuOpen = ref(false)
const setupOpen = ref(false)

function onKey(e: KeyboardEvent) {
  const typing = (e.target as HTMLElement)?.closest?.('input, textarea, select, [contenteditable]')
  if ((e.key === 'k' && (e.metaKey || e.ctrlKey)) || (e.key === '/' && !typing)) {
    e.preventDefault()
    openJump()
    return
  }
  if (typing || jumpOpen.value || setupOpen.value || !playing.value) return
  if (e.key === 'ArrowRight' && play.current.value < count.value - 1) showBeat(play.current.value + 1)
  if (e.key === 'ArrowLeft' && play.current.value > 0) showBeat(play.current.value - 1)
}

function reset() {
  if (confirm('Forget which episodes you finished?')) {
    resetProgress()
    say('Progress cleared')
  }
}

onMounted(() => {
  loadStore()
  window.addEventListener('keydown', onKey)
  window.addEventListener('hashchange', followHash)
  nextTick(followHash)
})
onUnmounted(() => {
  window.removeEventListener('keydown', onKey)
  window.removeEventListener('hashchange', followHash)
})

const nextLabel = computed(() => {
  const i = play.current.value
  if (play.needs[i] && !play.passed[i]) return 'Skip ahead'
  return beats.value[i + 1] ? 'Next: ' + beats.value[i + 1].title : 'Next'
})
</script>

<template>
  <div class="topbar">
    <a class="brand" :href="withBase('/')">FRESH BREATH</a>
    <div class="topbar-actions">
      <button class="chip" @click="openJump">Jump</button>
      <button class="chip" :aria-expanded="menuOpen" @click="menuOpen = !menuOpen">Menu</button>
    </div>
  </div>
  <div class="scrim" :class="{ show: menuOpen }" @click="menuOpen = false" />

  <div class="shell">
    <aside class="side" :class="{ open: menuOpen }" aria-label="Episodes">
      <a class="brand" :href="withBase('/')"><span>a guide to</span>FRESH BREATH</a>

      <div class="progress">
        <div class="bar" aria-hidden="true"><i :style="{ width: (doneCount / episodes.length) * 100 + '%' }" /></div>
        <span>{{ doneCount }}/{{ episodes.length }}</span>
      </div>

      <button class="jump-btn" @click="openJump">
        <span>Jump to anything…</span><kbd>/</kbd>
      </button>

      <nav class="navgroup">
        <h2>Episodes</h2>
        <ul class="nav">
          <li v-for="e in episodes" :key="e.url" :class="{ done: store.done[normalize(e.url)] }">
            <a :href="withBase(e.url)" :aria-current="here === e ? 'page' : undefined">
              <span class="dot">{{ store.done[normalize(e.url)] ? '✓' : e.episode }}</span>
              <span>{{ e.title }}</span>
            </a>
          </li>
        </ul>
      </nav>

      <nav v-if="references.length" class="navgroup">
        <h2>Reference</h2>
        <ul class="nav">
          <li v-for="r in references" :key="r.url">
            <a :href="withBase(r.url)" :aria-current="here === r ? 'page' : undefined">
              <span class="dot">{{ r.icon }}</span><span>{{ r.title }}</span>
            </a>
          </li>
        </ul>
      </nav>

      <div class="side-foot">
        <button class="chip" @click="setupOpen = true">⚙ Your setup</button>
        <button class="chip" @click="isDark = !isDark">{{ isDark ? '☀ Light' : '☾ Dark' }}</button>
        <button class="chip" @click="reset">Reset</button>
        <a class="chip" href="https://github.com/jrecyclebin/freshbreath" target="_blank" rel="noopener">GitHub ↗</a>
      </div>
    </aside>

    <main :class="{ leaving, arriving }">
      <!-- Home -->
      <template v-if="isHome">
        <p class="hero-word"><span>a guide to</span>FRESH<br>BREATH</p>
        <p class="lede">{{ frontmatter.tagline }}</p>
        <div class="hero-actions">
          <a class="btn big" :href="withBase(episodes[0]?.url || '/')">▶ Start episode 1</a>
          <button class="btn big ghost" @click="openJump">Jump to anything <kbd>/</kbd></button>
        </div>
        <Content class="home-content" />
        <h3>The episodes</h3>
        <p class="note">Each one takes a few minutes and has something to poke at. Finish one and it gets a check.</p>
        <ul class="path">
          <li v-for="e in episodes" :key="e.url">
            <a :href="withBase(e.url)" :class="{ done: store.done[normalize(e.url)] }">
              <span class="ic">{{ e.icon }}</span>
              <span class="n">{{ String(e.episode).padStart(2, '0') }}</span>
              <span class="t">{{ e.title }}<small>{{ e.blurb }}</small></span>
              <span class="tag">{{ store.done[normalize(e.url)] ? '✓ done' : e.minutes + ' min' }}</span>
            </a>
          </li>
        </ul>
      </template>

      <!-- Episode or reference page -->
      <article v-else-if="here" class="page" :class="{ playing }">
        <header class="episode-head">
          <div class="kicker">
            <span v-if="here.episode" class="ep">Episode {{ here.episode }}</span>
            <span v-else class="ep">Reference</span>
            <span v-if="here.minutes" class="mins">{{ here.minutes }} min</span>
            <span class="spacer" />
            <div v-if="isEpisode && count" class="mode" role="group" aria-label="View">
              <button :aria-pressed="store.mode === 'play'" @click="store.mode = 'play'">▶ Play</button>
              <button :aria-pressed="store.mode === 'read'" @click="store.mode = 'read'">☰ Read all</button>
            </div>
          </div>
          <h1>{{ here.icon }} {{ here.title }}</h1>
          <div v-if="here.short" class="short"><b>The short version</b><p>{{ here.short }}</p></div>
          <ol v-if="playing" class="beat-dots" aria-label="Beats in this episode">
            <li v-for="(b, i) in beats" :key="b.id">
              <button
                :class="{ on: i === play.current.value, passed: play.passed[i], seen: i < play.current.value }"
                :title="b.title"
                @click="showBeat(i)"
              ><span>{{ i + 1 }}</span><em>{{ b.title }}</em></button>
            </li>
          </ol>
        </header>

        <Content class="content" />

        <footer v-if="playing && !finished" class="beat-foot">
          <button class="btn ghost" :disabled="play.current.value === 0" @click="showBeat(play.current.value - 1)">← Back</button>
          <span class="count">{{ play.current.value + 1 }} / {{ count }}</span>
          <button
            v-if="play.current.value < count - 1"
            class="btn"
            :class="{ quiet: play.needs[play.current.value] && !play.passed[play.current.value], pulse: play.passed[play.current.value] }"
            @click="showBeat(play.current.value + 1)"
          >{{ nextLabel }} →</button>
          <button v-else class="btn finish-btn" :disabled="leaving" @click="finish">{{ nextEpisode ? 'Next episode: ' + nextEpisode.title : 'Finish 🎉' }} →</button>
        </footer>

        <div v-if="finished" class="finish">
          <div class="confetti" aria-hidden="true"><i v-for="n in 18" :key="n" /></div>
          <b>That's the whole set!</b>
          <p>You know Fresh Breath now. Every episode stays here as reference: hit <kbd>/</kbd> to jump to anything.</p>
          <div class="finish-actions">
            <a class="btn" :href="withBase('/')">Back to the start</a>
            <button class="btn ghost" @click="showBeat(0)">Replay this one</button>
          </div>
        </div>

        <footer v-if="isEpisode && !playing && nextEpisode" class="read-foot">
          <button v-if="!store.done[normalize(here.url)]" class="btn ghost" @click="store.done[normalize(here.url)] = true; say('Marked as done')">✓ Mark as done</button>
          <a class="btn" :href="withBase(nextEpisode.url)">Next: {{ nextEpisode.title }} →</a>
        </footer>
      </article>

      <!-- Plain page or 404 -->
      <article v-else class="page">
        <template v-if="page.isNotFound">
          <p class="hero-word">404</p>
          <p class="lede">Nothing lives at this address. Try <button class="btn small ghost" @click="openJump">Jump</button>.</p>
        </template>
        <Content v-else class="content" />
      </article>
    </main>
  </div>

  <!-- Jump search -->
  <div v-if="jumpOpen" class="modal-wrap" @click.self="jumpOpen = false">
    <div class="modal jump" role="dialog" aria-label="Jump to a topic">
      <input ref="jumpInput" v-model="query" type="search" placeholder="What do you need? Try “staging”, “database”, “login”…" @keydown="onJumpKey">
      <ul>
        <li v-for="(r, i) in results" :key="r.url">
          <button :class="{ on: i === picked }" @mouseenter="picked = i" @click="go(r.url)">
            <span class="ic">{{ r.icon }}</span>
            <span class="t">{{ r.label }}<small>{{ r.where }}</small></span>
          </button>
        </li>
        <li v-if="!results.length" class="none">Nothing matches. Try a shorter word.</li>
      </ul>
      <p class="note"><kbd>↑</kbd><kbd>↓</kbd> to pick, <kbd>Enter</kbd> to go, <kbd>Esc</kbd> to close</p>
    </div>
  </div>

  <!-- Your setup -->
  <div v-if="setupOpen" class="modal-wrap" @click.self="setupOpen = false">
    <div class="modal" role="dialog" aria-label="Your setup">
      <h3>Your setup</h3>
      <p class="note">Every “Say this to Claude” box fills itself in from here. It stays in this browser. (No app ID needed: your agent looks the app up by name.)</p>
      <div class="setup-grid">
        <label class="field">Server address<input v-model="store.setup.server" spellcheck="false"><small>Where your Fresh Breath runs</small></label>
        <label class="field">App name<input v-model="store.setup.app"><small>What you call it</small></label>
        <label class="field">Web path<input v-model="store.setup.slug" spellcheck="false" :aria-invalid="!!slugIssue"><small :class="{ bad: slugIssue }">{{ slugIssue || 'The end of the app’s address, like tix' }}</small></label>
      </div>
      <p style="margin-top:16px"><button class="btn" @click="setupOpen = false">Done</button></p>
    </div>
  </div>

  <div class="toast" :class="{ show: toast.shown }" role="status">{{ toast.text }}</div>
</template>
