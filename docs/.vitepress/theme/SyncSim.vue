<script setup lang="ts">
// Episode 11: a small deploy app on SSH file sync. Sign in with the
// passphrase (that loads the key into the server's agent for an hour), open
// a session to a host (good for 8 hours), diff the local folder against the
// remote one, and push the difference. Every request goes through api(),
// which adds the nonce and token, and answers a lapsed agent with a fresh
// passphrase prompt.
import { computed, reactive, ref } from 'vue'
import { useBeat, BrowserFrame } from './kit'

const local = reactive<Record<string, number>>({ 'index.html': 2, 'style.css': 1, 'about.html': 1 })
const remote = reactive<Record<string, number>>({ 'index.html': 1, 'style.css': 1, 'old.css': 1 })
const clock = ref(0)              // minutes since sign-in
const agentUntil = ref<number | null>(null)
const session = ref<{ id: string, until: number } | null>(null)
const diff = ref<{ upload: string[], delete: string[] } | null>(null)
const prompt = ref<null | ((ok: boolean) => void)>(null)
const log = ref<{ text: string, kind?: string }[]>([])
const seen = reactive({ open: false, mirrored: false, stepUp: false })
const beat = useBeat()

const agentLive = computed(() => agentUntil.value !== null && clock.value < agentUntil.value)
const fmt = (m: number) => `${Math.floor(m / 60)}h ${String(m % 60).padStart(2, '0')}m`
const hash = (path: string, v: number) => (path.length * 2654435761 + v * 40503).toString(16).slice(-8)

function note(text: string, kind?: string) {
  log.value.push({ text, kind })
  setTimeout(() => document.querySelector('.sync-sim .log')?.scrollTo({ top: 99999 }), 0)
}

function askPassphrase(why: string) {
  note(why, 'dim')
  return new Promise<boolean>((resolve) => (prompt.value = (ok) => { prompt.value = null; resolve(ok) }))
}

async function signIn() {
  if (!(await askPassphrase('login() → passphrase form'))) return
  agentUntil.value = clock.value + 60
  note('✓ key unsealed into the server’s agent for 1 hour. The app holds only a Fresh Breath token.', 'ok')
}

async function openSession() {
  if (agentUntil.value === null) { note('Sign in first: the key is sealed until your passphrase opens it.', 'bad'); return }
  note('api(session, "/api/ssh/sessions", { method: "POST", body: { host: "example.com", username: "deploy" } })')
  if (!agentLive.value) {
    note('← 401 insufficient_user_authentication: the agent key lapsed', 'bad')
    if (!(await askPassphrase('api() answers with a fresh passphrase login, then retries'))) return
    agentUntil.value = clock.value + 60
    seen.stepUp = true
  }
  session.value = { id: 's_' + Math.random().toString(36).slice(2, 8), until: clock.value + 480 }
  note(`← { sessionId: "${session.value.id}" }  open for 8 hours`, 'ok')
  seen.open = true
  check()
}

function runDiff() {
  if (!session.value) return
  const files = Object.entries(local).map(([p, v]) => ({ path: '/var/www/' + p, hash: hash(p, v) }))
  note(`api(session, "/api/sync/files/diff", { method: "POST", body: { sessionId, basePath: "/var/www", files: [${files.length} hashes] } })`)
  const upload = Object.keys(local).filter((p) => remote[p] === undefined || remote[p] !== local[p])
  const del = Object.keys(remote).filter((p) => local[p] === undefined)
  diff.value = { upload, delete: del }
  note(`← { upload: [${upload.join(', ')}], delete: [${del.join(', ')}] }`, 'ok')
}

function sync() {
  if (!diff.value) return
  for (const p of diff.value.upload) {
    remote[p] = local[p]
    note(`api(session, "/api/sync/files/var/www/${p}?sessionId=…", { method: "PUT", headers: { "X-Hash": "${hash(p, local[p])}" }, body: file })`)
  }
  for (const p of diff.value.delete) {
    delete remote[p]
    note(`api(session, "/api/sync/files/var/www/${p}?sessionId=…", { method: "DELETE" })`)
  }
  if (diff.value.upload.length && diff.value.delete.length) seen.mirrored = true
  note('✓ remote now matches the local folder. Unchanged files never moved.', 'ok')
  diff.value = null
  check()
}

function edit(p: string) { local[p] = (local[p] ?? 0) + 1; diff.value = null }
function skipHour() {
  clock.value += 61
  if (session.value && clock.value >= session.value.until) session.value = null
  note(`⏱ ${fmt(clock.value)} later. Agent key: ${agentLive.value ? 'loaded' : 'lapsed'}. Session: ${session.value ? 'still open' : 'none'}.`, 'dim')
}

function check() {
  if (seen.open && seen.mirrored && seen.stepUp) beat.pass('Files synced!')
}
</script>

<template>
  <div class="sim sync-sim">
    <div class="sim-head">
      <b>Try it: deploy a folder over SSH</b>
      <span class="spacer" />
      <span class="clock mono">⏱ {{ fmt(clock) }}</span>
      <button class="chip" @click="skipHour">Skip ahead an hour</button>
    </div>
    <div class="sim-body">
      <BrowserFrame url="https://fb.yourteam.dev/deployer/" title="Deployer" icon="🚚" accent="#1f6f8b">
        <div class="wa-head">
          <b>🚚 Deployer</b>
          <small>agent: {{ agentUntil === null ? 'sealed' : agentLive ? 'loaded' : 'lapsed' }} · session: {{ session ? session.id : 'none' }}</small>
        </div>
        <div class="wa-body">
          <div class="wa-row" style="margin-bottom:12px">
            <button class="wa-btn" @click="signIn">🔑 Sign in</button>
            <button class="wa-btn" @click="openSession">Connect to deploy@example.com</button>
            <button class="wa-btn ghost" :disabled="!session" @click="runDiff">Compare</button>
            <button class="wa-btn ghost" :disabled="!diff" @click="sync">Sync</button>
          </div>
          <div class="cols">
            <div>
              <div class="wa-muted">Your folder</div>
              <ul class="wa-list">
                <li v-for="(v, p) in local" :key="p" :class="{ up: diff?.upload.includes(p as string) }">
                  <span>{{ p }} <small>v{{ v }}</small></span>
                  <button class="tiny" @click="edit(p as string)">edit</button>
                </li>
              </ul>
            </div>
            <div>
              <div class="wa-muted">example.com:/var/www</div>
              <ul class="wa-list">
                <li v-for="(v, p) in remote" :key="p" :class="{ del: diff?.delete.includes(p as string) }"><span>{{ p }} <small>v{{ v }}</small></span></li>
              </ul>
            </div>
          </div>
        </div>
        <div v-if="prompt" class="popup-wrap">
          <div class="popup pop">
            <b>Fresh Breath · sign in</b>
            <p>Email and SSH key passphrase, for dana@yourteam.dev.</p>
            <div class="wa-row">
              <button class="wa-btn" @click="prompt(true)">Unlock</button>
              <button class="wa-btn plain" @click="prompt(false)">Cancel</button>
            </div>
          </div>
        </div>
      </BrowserFrame>
      <div class="log" style="margin-top:12px">
        <div class="dim">requests from the page</div>
        <div v-for="(l, i) in log" :key="i" :class="l.kind === 'ok' ? 'hl' : l.kind === 'dim' || l.kind === 'bad' ? 'dim' : ''">{{ l.text }}</div>
      </div>
    </div>
    <ul class="quests">
      <li class="quests-head">Quests</li>
      <li :class="{ got: seen.open }">Sign in and open a session</li>
      <li :class="{ got: seen.mirrored }">Mirror the folder: compare, then sync an upload and a delete</li>
      <li :class="{ got: seen.stepUp }">Skip ahead an hour, connect again, and let api() handle the lapsed key</li>
    </ul>
  </div>
</template>

<style scoped>
.clock { font-size: 13px; color: var(--ink-soft) }
.bframe { position: relative }
.cols { display: grid; grid-template-columns: 1fr 1fr; gap: 10px }
.cols .wa-muted { margin-bottom: 4px }
.cols li { display: flex; justify-content: space-between; align-items: center; font-size: 13.5px }
.cols li small { color: var(--wa-soft) }
.cols li.up { background: #e9f7ee; color: #1f6f3b }
.cols li.del { background: #fdecee; color: #a3283a; text-decoration: line-through }
.tiny { font-weight: 600; font-size: 11px; font-family: inherit; border: 0; background: var(--wa-tint); color: var(--wa-soft); border-radius: 4px; padding: 2px 6px; cursor: pointer }
.popup-wrap { position: absolute; inset: 0; background: rgba(10, 12, 18, .4); display: grid; place-items: center; padding: 16px }
.popup { background: var(--wa-bg); border-radius: 12px; padding: 16px 18px; box-shadow: 0 20px 50px rgba(0,0,0,.4); max-width: 320px }
.popup p { margin: 6px 0 12px; font-size: 13.5px; color: var(--wa-soft) }
@media (max-width: 520px) { .cols { grid-template-columns: 1fr } }
</style>
