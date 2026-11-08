<script setup lang="ts">
// Episode 1: the same little app, as a plain HTML file and then on Fresh
// Breath. Each button fails for an honest reason on its own, and works once
// the app is plugged in.
import { computed, reactive, ref } from 'vue'
import { useBeat, BrowserFrame } from './kit'

const plugged = ref(false)
const cards = ref<string[]>(['Water the ferns'])
const draft = ref('')
const user = ref('')
const issues = ref<string[]>([])
const message = ref<{ kind: string, text: string } | null>(null)
const worked = reactive({ signin: false, save: false, github: false })
const tried = reactive({ signin: false, save: false, github: false })
const beat = useBeat()

const quests = computed(() => [
  { got: tried.signin && tried.save && tried.github, text: 'Try all three buttons as a plain HTML file' },
  { got: worked.signin && worked.save && worked.github, text: 'Plug into Fresh Breath and get all three working' }
])

function check() {
  if (quests.value.every((q) => q.got)) beat.pass('Plugged in!')
}

function signIn() {
  if (!plugged.value) {
    tried.signin = true
    message.value = { kind: 'bad', text: '🔐 A plain HTML file has nobody to ask who you are. There is no server to check a password, and no place to keep a login.' }
  } else {
    user.value = 'Dana (Company SSO)'
    worked.signin = true
    message.value = { kind: 'ok', text: '✅ Fresh Breath sent you through your work login and back. The token lives in your browser, sealed.' }
  }
  check()
}

function save() {
  const text = draft.value.trim() || 'Buy more ferns'
  cards.value.push(text)
  draft.value = ''
  if (!plugged.value) {
    tried.save = true
    message.value = { kind: 'meh', text: '💾 Saved… to this tab. Hit “Refresh page” and watch it disappear.' }
  } else {
    worked.save = true
    message.value = { kind: 'ok', text: '✅ Saved through a tool in the app’s own database. Refresh away.' }
  }
  check()
}

function refresh() {
  if (!plugged.value) {
    cards.value = ['Water the ferns']
    message.value = { kind: 'bad', text: '🫥 Gone. A browser tab forgets everything when it reloads (and nobody else ever saw it).' }
  } else {
    message.value = { kind: 'ok', text: '🔄 Reloaded from the database. Everything’s still here, for everyone who opens the app.' }
  }
}

function github() {
  if (!plugged.value) {
    tried.github = true
    issues.value = []
    message.value = { kind: 'bad', text: '🚧 Blocked: CORS, plus there is nowhere safe to keep a GitHub token or client secret in a file anyone can open.' }
  } else {
    issues.value = ['#41 Ferns need more light', '#42 Add a watering schedule']
    worked.github = true
    message.value = { kind: 'ok', text: '✅ Called through the Fresh Breath proxy as you. The client secret never left the server.' }
  }
  check()
}

function flip() {
  plugged.value = !plugged.value
  message.value = plugged.value
    ? { kind: 'ok', text: '🔌 Plugged in. Same HTML file, now hosted by Fresh Breath with frbr.js loaded.' }
    : null
  if (!plugged.value) { user.value = ''; issues.value = [] }
}
</script>

<template>
  <div class="sim plug-sim">
    <div class="sim-head">
      <b>Try it: one app, two lives</b>
      <span class="spacer" />
      <div class="mode" role="group" aria-label="Where the app runs">
        <button :aria-pressed="!plugged" @click="plugged && flip()">📄 Plain HTML file</button>
        <button :aria-pressed="plugged" @click="!plugged && flip()">🌬️ On Fresh Breath</button>
      </div>
    </div>
    <div class="sim-body">
      <BrowserFrame
        :url="plugged ? 'https://fb.yourteam.dev/ferns' : 'file:///Users/you/Desktop/ferns.html'"
        title="Fern Board" icon="🌿" accent="#2e8b57"
        :class="{ plugged }"
      >
        <div class="wa-head">
          <b>🌿 Fern Board</b>
          <small>{{ user ? 'Hi, ' + user : 'Not signed in' }}</small>
          <button class="wa-btn light" @click="signIn">Sign in</button>
        </div>
        <div class="wa-body">
          <ul class="mini-cards">
            <li v-for="(c, i) in cards" :key="i + c">{{ c }}</li>
            <li v-for="iss in issues" :key="iss" class="gh">🐙 {{ iss }}</li>
          </ul>
          <div class="wa-row">
            <input v-model="draft" type="text" class="wa-input grow" placeholder="New card…" @keydown.enter="save">
            <button class="wa-btn" @click="save">Save card</button>
            <button class="wa-btn ghost" @click="github">Pull GitHub issues</button>
            <button class="wa-btn plain" @click="refresh">↻ Refresh page</button>
          </div>
        </div>
      </BrowserFrame>
      <div v-if="message" :key="message.text" class="verdict pop" :class="message.kind">{{ message.text }}</div>
    </div>
    <ul class="quests">
      <li v-for="q in quests" :key="q.text" :class="{ got: q.got }">{{ q.text }}</li>
    </ul>
  </div>
</template>

<style scoped>
.bframe { margin-top: 2px; transition: box-shadow .3s }
.bframe.plugged { box-shadow: 0 0 0 2px var(--mint), 0 10px 28px -12px rgba(20, 24, 40, .35) }
.grow { flex: 1 1 160px }
.mini-cards { list-style: none; padding: 0 !important; margin: 0 0 12px; display: flex; flex-wrap: wrap; gap: 8px }
.mini-cards li { background: #fff8e6; color: #3a3220; border-radius: 4px; padding: 8px 12px; font-size: 14px; margin: 0; box-shadow: 0 1px 3px rgba(0,0,0,.15); transform: rotate(-.6deg); animation: pop .3s }
.mini-cards li:nth-child(even) { transform: rotate(.8deg); background: #e9f7ee }
.mini-cards li.gh { background: #efeaff }
</style>
