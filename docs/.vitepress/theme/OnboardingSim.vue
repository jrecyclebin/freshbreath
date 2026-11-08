<script setup lang="ts">
// Episode 2: the control panel's first-run onboarding, screen by screen, as
// the real one runs it (web/control/app.js OnboardingScreen): on plain HTTP
// it offers a certificate first, then "Welcome to Fresh Breath" creates the
// first Superuser, seals their SSH key with a passphrase, and locks the panel
// behind the built-in SSH record.
import { computed, reactive, ref } from 'vue'
import { useBeat, BrowserFrame } from './kit'

type Step = 'tls' | 'issued' | 'account' | 'done' | 'signin' | 'panel'
const step = ref<Step>('tls')
const https = ref(false)
const form = reactive({ name: '', email: '', passphrase: '', confirm: '' })
const signin = reactive({ email: '', passphrase: '', error: '' })
const seen = reactive({ cert: false, account: false, signedIn: false })
const beat = useBeat()

const url = computed(() => (https.value ? 'https' : 'http') + '://localhost:9009/control')
const ready = computed(() => form.name && form.email && form.passphrase.length >= 8 && form.passphrase === form.confirm)
const problem = computed(() => {
  if (form.passphrase && form.passphrase.length < 8) return 'Passphrase must be at least 8 characters'
  if (form.confirm && form.passphrase !== form.confirm) return 'Passphrases don’t match'
  return ''
})

function generate() { step.value = 'issued'; seen.cert = true }
function continueHttps() { https.value = true; step.value = 'account' }
function create() { if (ready.value) { step.value = 'done'; seen.account = true } }
function signIn() {
  if (signin.email.toLowerCase() === form.email.toLowerCase() && signin.passphrase === form.passphrase) {
    step.value = 'panel'
    seen.signedIn = true
    beat.pass('You’re the Superuser')
  } else {
    signin.error = 'That email and passphrase don’t match.'
  }
}
function restart() {
  step.value = 'tls'; https.value = false
  Object.assign(form, { name: '', email: '', passphrase: '', confirm: '' })
  Object.assign(signin, { email: '', passphrase: '', error: '' })
}
</script>

<template>
  <div class="sim onboarding-sim">
    <div class="sim-head">
      <b>Try it: first run</b>
      <span class="spacer" />
      <button class="chip" @click="restart">↺ Start over</button>
    </div>
    <div class="sim-body">
      <BrowserFrame :url="url" title="Fresh Breath" icon="🌬️" accent="#3f5bd8">
        <div class="ob">
          <div class="logo">🌬️ <b>Fresh Breath</b></div>

          <div v-if="step === 'tls'" class="card pop">
            <h4>Secure the connection</h4>
            <p>This panel is on plain HTTP. Fresh Breath can make its own certificate authority and a certificate for this server, so browsers and MCP clients can connect over HTTPS once you trust it.</p>
            <button class="wa-btn" @click="generate">🔒 Generate a certificate</button>
            <button class="wa-btn plain" @click="step = 'account'">Skip — stay on HTTP</button>
          </div>

          <div v-else-if="step === 'issued'" class="card pop">
            <h4>Certificate ready</h4>
            <p>Issued for localhost, 127.0.0.1 and this machine, and recorded in <code>.env</code>.</p>
            <ol>
              <li><b>Download the certificate authority.</b> <span class="file">⬇ freshbreath-ca.pem</span></li>
              <li><b>Trust it on each device you’ll use.</b> Node tools like Claude Code also want <code>NODE_EXTRA_CA_CERTS</code>.</li>
              <li><b>Restart Fresh Breath.</b></li>
            </ol>
            <button class="wa-btn" @click="continueHttps">🔒 Continue at https://localhost:9009</button>
          </div>

          <form v-else-if="step === 'account'" class="card pop" @submit.prevent="create">
            <h4>Welcome to Fresh Breath</h4>
            <p>Nobody’s signed up yet, so this panel is open to anyone who can reach it. Make yourself the Superuser and lock it with an SSH passphrase.</p>
            <label>Name<input v-model="form.name" class="wa-input" autocomplete="off"></label>
            <label>Email<input v-model="form.email" class="wa-input" type="email" autocomplete="off" @input="form.email = form.email.toLowerCase()"></label>
            <label>Passphrase<input v-model="form.passphrase" class="wa-input" type="password" placeholder="Min 8 characters" autocomplete="new-password"></label>
            <label>Confirm passphrase<input v-model="form.confirm" class="wa-input" type="password" placeholder="Re-enter passphrase" autocomplete="new-password"></label>
            <p v-if="problem" class="err">{{ problem }}</p>
            <button class="wa-btn" type="submit" :disabled="!ready">🔒 Create account and lock the panel</button>
            <button class="wa-btn plain" type="button" @click="step = 'panel'">Skip — I’ll set up auth myself</button>
          </form>

          <div v-else-if="step === 'done'" class="card pop">
            <h4>You’re all set</h4>
            <p>The control panel is locked now. Sign in with {{ form.email }} and the passphrase you just chose.</p>
            <button class="wa-btn" @click="step = 'signin'; signin.email = form.email">🔒 Continue to sign in</button>
          </div>

          <form v-else-if="step === 'signin'" class="card pop" @submit.prevent="signIn">
            <h4>Sign in</h4>
            <label>Email<input v-model="signin.email" class="wa-input" autocomplete="off"></label>
            <label>Passphrase<input v-model="signin.passphrase" class="wa-input" type="password" autocomplete="current-password"></label>
            <p v-if="signin.error" class="err">{{ signin.error }}</p>
            <button class="wa-btn" type="submit">Sign in</button>
          </form>

          <div v-else class="card pop panel">
            <h4>{{ seen.signedIn ? 'Hi, ' + (form.name || 'there') + ' 👋' : 'Control panel' }}</h4>
            <p v-if="seen.signedIn">You’re the Superuser, and the panel answers only to your passphrase. Apps, Services, Users and Settings are all yours.</p>
            <p v-else class="err">Skipped: the panel is still open to anyone who can reach it, until you set an Admin auth in Settings.</p>
            <div class="tiles"><span>📦 Apps</span><span>🔌 Services</span><span>👥 Users</span><span>⚙ Settings</span></div>
          </div>
        </div>
      </BrowserFrame>
    </div>
    <ul class="quests">
      <li class="quests-head">Quests</li>
      <li :class="{ got: seen.cert }">Make a certificate for HTTPS (or skip it; it’s optional)</li>
      <li :class="{ got: seen.account }">Create your Superuser account</li>
      <li :class="{ got: seen.signedIn }">Sign in with your new passphrase</li>
    </ul>
  </div>
</template>

<style scoped>
.ob { padding: 18px; background: var(--wa-tint); min-height: 340px }
.logo { text-align: center; margin-bottom: 12px; font-size: 18px }
.card { max-width: 400px; margin: 0 auto; background: var(--wa-bg); border: 1px solid var(--wa-line); border-radius: 12px; padding: 18px; display: flex; flex-direction: column; gap: 10px; box-shadow: 0 4px 16px -8px rgba(0,0,0,.2) }
.card h4 { margin: 0; font-size: 19px }
.card p { margin: 0; font-size: 13.5px; color: var(--wa-soft) }
.card label { margin: 0 }
.card label .wa-input { display: block; width: 100%; margin-top: 3px; font-weight: 400 }
.card ol { margin: 0; padding-left: 20px; font-size: 13.5px; display: grid; gap: 6px }
.card ol li { margin: 0 }
.file { font-family: var(--mono); font-size: 12px; background: var(--wa-tint); padding: 1px 6px; border-radius: 4px }
.err { color: #c23b4b !important }
.tiles { display: grid; grid-template-columns: 1fr 1fr; gap: 8px }
.tiles span { padding: 10px; border-radius: 8px; background: var(--wa-tint); font-size: 13.5px; font-weight: 600 }
</style>
