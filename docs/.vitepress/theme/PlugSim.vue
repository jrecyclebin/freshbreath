<script setup lang="ts">
// Episode 1: the same little app, as a plain HTML file and then on Fresh
// Breath. Each button fails for an honest reason on its own, and works once
// the app is plugged in.
import { computed, reactive, ref } from 'vue'
import { useBeat } from './kit'

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
      <div class="urlbar"><span :class="{ lock: plugged }">{{ plugged ? '🔒' : '📄' }}</span>{{ plugged ? 'https://fb.yourteam.dev/ferns' : 'file:///Users/you/Desktop/ferns.html' }}</div>
      <div class="mini-app" :class="{ plugged }">
        <div class="mini-top">
          <b>🌿 Fern Board</b>
          <span class="note" style="margin:0">{{ user ? 'Hi, ' + user : 'Not signed in' }}</span>
          <button class="btn small ghost" @click="signIn">Sign in</button>
        </div>
        <ul class="mini-cards">
          <li v-for="(c, i) in cards" :key="i + c">{{ c }}</li>
          <li v-for="iss in issues" :key="iss" class="gh">🐙 {{ iss }}</li>
        </ul>
        <div class="row">
          <input v-model="draft" type="text" class="grow" placeholder="New card…" @keydown.enter="save">
          <button class="btn small" @click="save">Save card</button>
          <button class="btn small ghost" @click="github">Pull GitHub issues</button>
          <button class="btn small quiet" @click="refresh">↻ Refresh page</button>
        </div>
      </div>
      <div v-if="message" :key="message.text" class="verdict pop" :class="message.kind">{{ message.text }}</div>
    </div>
    <ul class="quests">
      <li v-for="q in quests" :key="q.text" :class="{ got: q.got }">{{ q.text }}</li>
    </ul>
  </div>
</template>

<style scoped>
.mini-app { margin-top: 12px; border: 1.5px dashed var(--line); border-radius: 10px; padding: 14px; background: var(--bg); transition: border-color .3s }
.mini-app.plugged { border-style: solid; border-color: var(--mint) }
.mini-top { display: flex; align-items: center; gap: 12px; margin-bottom: 10px; flex-wrap: wrap }
.mini-top b { font-family: var(--display); font-size: 18px; flex: 1 }
.mini-cards { list-style: none; padding: 0 !important; margin: 0 0 12px; display: flex; flex-wrap: wrap; gap: 8px }
.mini-cards li { background: var(--surface); border: 1.5px solid var(--line); border-radius: 8px; padding: 6px 12px; font-size: 15px; margin: 0; animation: pop .3s }
.mini-cards li.gh { border-color: var(--amber) }
</style>
