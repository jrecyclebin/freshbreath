<script setup lang="ts">
// Episode 7: two apps, one browser store. login() only prompts when the store
// has nothing for that app's gate, so a second app behind the same gate
// signs in with zero prompts.
import { computed, reactive, ref } from 'vue'
import { useBeat } from './kit'

type Gate = 'Company SSO' | 'GitHub'
const apps = reactive([
  { name: 'Tix', icon: '🎫', gate: 'Company SSO' as Gate, user: '', last: '' },
  { name: 'Dash', icon: '📊', gate: 'Company SSO' as Gate, user: '', last: '' }
])
const storeRows = reactive<Record<string, { token: string, sealed: string }>>({})
const prompts = ref(0)
const popup = ref<{ app: typeof apps[number], resolve: (ok: boolean) => void } | null>(null)
const seen = reactive({ zero: false, silent: false, separate: false })
const beat = useBeat()

const rand = () => Math.random().toString(36).slice(2, 10)

function check() {
  if (seen.zero && seen.silent && seen.separate) beat.pass('Logins, mastered')
}

async function login(app: typeof apps[number], mode: 'normal' | 'silent') {
  const row = storeRows[app.gate]
  if (row) {
    app.user = 'Dana'
    app.last = `→ resolved from the store. 0 prompts.`
    if (mode === 'normal' && apps.every((a) => a.user) && apps[0].gate === apps[1].gate && prompts.value === 1) seen.zero = true
    check()
    return
  }
  if (mode === 'silent') {
    app.last = '→ null. Nothing stored for this gate, and silent never prompts.'
    seen.silent = true
    check()
    return
  }
  const ok = await new Promise<boolean>((resolve) => (popup.value = { app, resolve }))
  popup.value = null
  if (!ok) {
    app.last = '✗ throws "Login window closed"'
    return
  }
  prompts.value++
  storeRows[app.gate] = { token: 'frbr_' + rand() + rand(), sealed: app.gate === 'GitHub' ? 'gho_••••••••' : 'eyJ0eXAi••••' }
  app.user = 'Dana'
  app.last = '→ signed in after 1 prompt'
  if (Object.keys(storeRows).length === 2) seen.separate = true
  check()
}

function changeGate(app: typeof apps[number]) {
  app.user = ''
  app.last = ''
}

function clearSite() {
  for (const k of Object.keys(storeRows)) delete storeRows[k]
  for (const a of apps) { a.user = ''; a.last = '' }
  prompts.value = 0
}

const rows = computed(() => Object.entries(storeRows))
</script>

<template>
  <div class="sim login-sim">
    <div class="sim-head">
      <b>Try it: two apps, one browser</b>
      <span class="spacer" />
      <span class="note" style="margin:0">Prompts so far: <strong>{{ prompts }}</strong></span>
      <button class="chip" @click="clearSite">🧹 Clear site data</button>
    </div>
    <div class="sim-body">
      <div class="apps">
        <div v-for="app in apps" :key="app.name" class="app" :class="{ in: app.user }">
          <div class="app-top"><b>{{ app.icon }} {{ app.name }}</b><span class="note" style="margin:0">{{ app.user ? 'Hi, ' + app.user : 'signed out' }}</span></div>
          <label class="inline">Protected by
            <select v-model="app.gate" @change="changeGate(app)">
              <option>Company SSO</option>
              <option>GitHub</option>
            </select>
          </label>
          <div class="row" style="margin-top:10px">
            <button class="btn small" @click="login(app, 'normal')">login()</button>
            <button class="btn small ghost" @click="login(app, 'silent')">login(url, "silent")</button>
          </div>
          <div v-if="app.last" :key="app.last" class="result pop mono">{{ app.last }}</div>
        </div>
      </div>

      <div class="storage">
        <div class="storage-head mono">localStorage · Fresh Breath's origin</div>
        <div v-if="!rows.length" class="note" style="margin:0">Empty.</div>
        <div v-for="[gate, row] in rows" :key="gate" class="srow pop mono">
          <span>{{ gate }}</span>
          <span>{{ row.token }}</span>
          <span class="sealed" title="Sealed: only the server can open it">🔒 {{ row.sealed }}</span>
        </div>
        <p class="note" style="margin:8px 0 0">What's stored is a Fresh Breath token. The provider's token rides inside it, sealed. Client secrets never reach the browser.</p>
      </div>

      <div v-if="popup" class="popup-wrap">
        <div class="popup pop">
          <b>Sign in to {{ popup.app.name }}</b>
          <p>{{ popup.app.gate === 'GitHub' ? '🐙 GitHub wants to know it’s you.' : '🏢 Your work account, please.' }}</p>
          <div class="row">
            <button class="btn" @click="popup.resolve(true)">Continue as Dana</button>
            <button class="btn ghost" @click="popup.resolve(false)">Close window</button>
          </div>
        </div>
      </div>
    </div>
    <ul class="quests">
      <li class="quests-head">Quests</li>
      <li :class="{ got: seen.zero }">Sign in to both apps with only one prompt</li>
      <li :class="{ got: seen.silent }">Ask silently with nothing stored, and get <code>null</code></li>
      <li :class="{ got: seen.separate }">Put the apps behind different gates and sign in to each</li>
    </ul>
  </div>
</template>

<style scoped>
.sim-body { position: relative }
.apps { display: grid; grid-template-columns: repeat(auto-fit, minmax(230px, 1fr)); gap: 10px }
.app { border: 1.5px solid var(--line); border-radius: 10px; padding: 12px; background: var(--bg) }
.app.in { border-color: var(--mint) }
.app-top { display: flex; justify-content: space-between; align-items: baseline; margin-bottom: 8px }
.app-top b { font-family: var(--display); font-size: 18px }
.result { margin-top: 10px; font-size: 13px; color: var(--ink-soft) }
.storage { margin-top: 14px; padding: 12px; border-radius: 10px; background: var(--code-bg); color: var(--code-ink) }
.storage .note { color: #8b93a3 }
.storage-head { font-size: 12px; color: var(--ph); margin-bottom: 6px }
.srow { display: grid; grid-template-columns: 110px 1fr auto; gap: 10px; font-size: 13px; padding: 4px 0; border-top: 1px solid #2a2f3a; overflow-wrap: anywhere }
.sealed { color: var(--ph) }
.popup-wrap { position: absolute; inset: 0; background: rgba(10, 12, 18, .45); display: grid; place-items: center; padding: 16px; border-radius: 0 0 12px 12px }
.popup { background: var(--surface); border-radius: 12px; padding: 18px 20px; box-shadow: 0 20px 50px rgba(0,0,0,.4); max-width: 340px }
.popup b { font-family: var(--display); font-size: 20px }
@media (max-width: 520px) { .srow { grid-template-columns: 1fr } }
</style>
