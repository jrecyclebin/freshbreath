<script setup lang="ts">
// Episode 6: one call to a service that talks to GitHub, traced from the
// caller to the upstream API. Two slots decide it: which gate the caller
// must clear (the app's, when reached through an app; otherwise the
// service's Protected by), and which credential goes upstream as $token
// (Acts as: empty means the caller's own).
import { computed, reactive, ref, watch } from 'vue'
import { useBeat } from './kit'

type Caller = 'dana' | 'claude' | 'cron'
type Gate = '' | 'sso' | 'anon'
type Acts = '' | 'oauth' | 'key'

const caller = ref<Caller>('dana')
const protectedBy = ref<Gate>('')
const actsAs = ref<Acts>('')
const danaSignedIn = ref(false)
const seen = reactive({ herself: false, unattended: false, doorOwns: false })
const beat = useBeat()

const CALLERS: Record<Caller, string> = {
  dana: '👩‍🔬 Dana, in the Tix app (Tix is behind Company SSO)',
  claude: '🤖 Claude, over MCP at /mcp/github-tools',
  cron: '⏰ A scheduled task at 3am, nobody present'
}
const GATES: Record<Gate, string> = { '': '(empty) → Admin auth: Built-in SSH key', sso: 'Company SSO', anon: 'Anonymous' }
const ACTS: Record<Acts, string> = { '': '(empty) → the caller’s own', oauth: 'GitHub sign-in (OAuth record)', key: 'GitHub API key (stored record)' }

const trace = computed(() => {
  const steps: { who: string, text: string, kind: string }[] = []
  // 1. The door.
  if (caller.value === 'dana') {
    steps.push({ who: 'Door', kind: 'ok', text: `Reached through Tix, so it answers to Tix’s gate (Company SSO), whatever its own Protected by says. Dana is already through it.` })
  } else {
    const gate = protectedBy.value
    const name = gate === '' ? 'the Admin auth (Built-in SSH key)' : GATES[gate]
    const who = caller.value === 'claude' ? 'Claude signed in with your passphrase' : 'The task holds the token it signed in with'
    if (gate === 'sso') steps.push({ who: 'Door', kind: 'ok', text: `Its own Protected by is Company SSO, so the MCP client signed in with a work account when it connected.` })
    else steps.push({ who: 'Door', kind: 'ok', text: gate === 'anon' ? 'Protected by Anonymous: anyone may call. (Anyone. Careful.)' : `Its own Protected by is empty, so it inherits ${name}. ${who}.` })
  }
  // What "the caller's own" credential is, given the gate they cleared.
  const own = caller.value === 'dana' || protectedBy.value === 'sso' ? 'microsoft' : protectedBy.value === 'anon' ? 'passthrough' : 'passphrase'

  // 2. What goes upstream as $token.
  const a = actsAs.value
  if (a === 'key') {
    steps.push({ who: '$token', kind: 'ok', text: 'The stored GitHub API key. The same key for every caller, present or not.' })
    steps.push({ who: 'GitHub', kind: 'ok', text: '200 OK, acting as whoever owns the key.' })
  } else if (a === 'oauth') {
    if (caller.value === 'cron') {
      steps.push({ who: '$token', kind: 'bad', text: 'An interactive record needs the caller’s own GitHub login, and nobody’s there to give one. The call fails, naming the record that needs a login. (Never silently sent without.)' })
    } else if (caller.value === 'dana' && !danaSignedIn.value) {
      steps.push({ who: 'login()', kind: 'meh', text: 'Dana hasn’t signed in to GitHub yet, so login() asks her once: a second leg after Tix’s gate.' })
    } else {
      steps.push({ who: '$token', kind: 'ok', text: `${caller.value === 'dana' ? 'Dana’s' : 'Your'} own GitHub token, unsealed from the Fresh Breath token on the way out.` })
      steps.push({ who: 'GitHub', kind: 'ok', text: `200 OK, as ${caller.value === 'dana' ? 'Dana' : 'you'}. GitHub sees a real person, with their own permissions.` })
    }
  } else {
    if (own === 'microsoft') {
      steps.push({ who: '$token', kind: 'meh', text: 'The caller’s own: whatever their gate gave them. Behind Company SSO, that’s a Microsoft token.' })
      steps.push({ who: 'GitHub', kind: 'bad', text: '401: GitHub doesn’t take Microsoft tokens. (Great for a service calling Microsoft Graph, though.)' })
    } else if (own === 'passthrough') {
      steps.push({ who: '$token', kind: 'meh', text: 'Behind an open gate, the caller’s own Authorization header rides through untouched. This caller sent none.' })
      steps.push({ who: 'GitHub', kind: 'bad', text: '401: no credential at all.' })
    } else {
      steps.push({ who: '$token', kind: 'bad', text: 'The caller’s own, but behind a passphrase gate there is no upstream credential, and a Fresh Breath token never leaves this server. $token is empty and the step fails.' })
    }
  }
  return steps
})

const works = computed(() => trace.value[trace.value.length - 1]?.who === 'GitHub' && trace.value[trace.value.length - 1].kind === 'ok')

let lastGate = protectedBy.value
watch([caller, protectedBy, actsAs, danaSignedIn, works], () => {
  if (works.value && caller.value === 'dana' && actsAs.value === 'oauth') seen.herself = true
  if (works.value && caller.value === 'cron') seen.unattended = true
  if (caller.value === 'dana' && protectedBy.value !== lastGate) seen.doorOwns = true
  lastGate = protectedBy.value
  if (Object.values(seen).every(Boolean)) beat.pass('Service auth, sorted')
})
</script>

<template>
  <div class="sim sauth-sim">
    <div class="sim-head"><b>Try it: follow one call</b></div>
    <div class="sim-body">
      <div class="row">
        <label class="inline grow">Who’s calling
          <select v-model="caller"><option v-for="(l, k) in CALLERS" :key="k" :value="k">{{ l }}</option></select>
        </label>
      </div>
      <div class="svc">
        <div class="svc-name"><b>github-tools</b> <span class="mono">virtual service · GET https://api.github.com/user/repos with $token</span></div>
        <div class="row">
          <label class="inline grow">Protected by
            <select v-model="protectedBy"><option v-for="(l, k) in GATES" :key="k" :value="k">{{ l }}</option></select>
          </label>
          <label class="inline grow">Acts as
            <select v-model="actsAs"><option v-for="(l, k) in ACTS" :key="k" :value="k">{{ l }}</option></select>
          </label>
        </div>
      </div>
      <ol class="trace">
        <li v-for="(s, i) in trace" :key="i + s.text" class="pop" :class="s.kind"><b>{{ s.who }}</b> {{ s.text }}</li>
      </ol>
      <button v-if="caller === 'dana' && actsAs === 'oauth' && !danaSignedIn" class="btn small" @click="danaSignedIn = true">🐙 Sign Dana in to GitHub</button>
      <div class="verdict" :class="works ? 'ok' : 'bad'">{{ works ? '✅ The call works.' : '✗ The call doesn’t get through.' }}</div>
    </div>
    <ul class="quests">
      <li class="quests-head">Quests</li>
      <li :class="{ got: seen.herself }">Make it work for Dana, as herself on GitHub</li>
      <li :class="{ got: seen.unattended }">Make it work for the 3am scheduled task</li>
      <li :class="{ got: seen.doorOwns }">While Dana calls through Tix, change the service’s Protected by. Does it matter?</li>
    </ul>
  </div>
</template>

<style scoped>
.svc { margin: 14px 0; padding: 12px; border: 1.5px solid var(--line); border-radius: 10px; background: var(--bg) }
.svc-name { margin-bottom: 8px; font-size: 15px }
.svc-name .mono { font-size: 12px; color: var(--ink-soft) }
.trace { margin: 0 0 10px !important }
.trace li { font-size: 15px }
.trace li b { font-family: var(--mono); font-size: 12px; padding: 1px 7px; border-radius: 4px; background: var(--surface-2); margin-right: 6px }
.trace li.ok b { background: var(--mint-soft); color: var(--mint) }
.trace li.bad b { background: var(--red-soft); color: var(--red) }
.trace li.meh b { background: var(--plum-soft); color: var(--plum) }
</style>
