<script setup lang="ts">
// Who gets through an app's door, for each "Protected by" choice.
// Mirrors the server's rules: an empty slot inherits the admin record, an SSO
// gate admits whoever the provider vouches for, "Fresh Breath users only"
// admits only provider logins whose email matches a user, and only the admin
// record (or a users-only record) turns a provider login into a Fresh Breath user.
import { computed, ref } from 'vue'

type Gate = 'anonymous' | 'ssh' | 'sso' | 'github' | 'github-users'

const gateNames: Record<Gate, string> = {
  anonymous: 'Anonymous',
  ssh: 'Built-in SSH key',
  sso: 'Company SSO',
  github: 'GitHub',
  'github-users': 'GitHub, Fresh Breath users only'
}

const protectedBy = ref<'inherit' | Gate>('inherit')
const adminAuth = ref<'ssh' | 'sso' | 'github'>('ssh')

const visitors = [
  { id: 'you', name: 'You', note: 'Superuser with an SSH key, plus company and GitHub accounts', fb: true, sso: true, github: true },
  { id: 'coworker', name: 'A coworker', note: 'Company account only. No Fresh Breath account.', fb: false, sso: true, github: false },
  { id: 'stranger', name: 'A stranger online', note: 'Has a GitHub account. That’s it.', fb: false, sso: false, github: true },
  { id: 'nobody', name: 'Someone on your Wi-Fi', note: 'No accounts anywhere', fb: false, sso: false, github: false }
]

const effective = computed<Gate>(() => (protectedBy.value === 'inherit' ? adminAuth.value : protectedBy.value))
const isAdminRecord = computed(() => protectedBy.value === 'inherit')

function verdict(v: (typeof visitors)[number]) {
  const g = effective.value
  const asUser = v.fb ? 'as a Fresh Breath user' : ''
  const asOutsider = 'as an outside identity — no Fresh Breath account needed'
  switch (g) {
    case 'anonymous':
      return { ok: true, why: 'No sign-in at all. Anyone who can reach the server walks in.' }
    case 'ssh':
      return v.fb
        ? { ok: true, why: 'Signs in with email and SSH key passphrase, as a Fresh Breath user.' }
        : { ok: false, why: 'Needs a Fresh Breath account with an SSH key.' }
    case 'sso':
      if (!v.sso) return { ok: false, why: 'The company SSO doesn’t know them.' }
      return { ok: true, why: `The company SSO vouches for them — ${isAdminRecord.value && v.fb ? asUser : asOutsider}.` }
    case 'github':
      if (!v.github) return { ok: false, why: 'No GitHub account to sign in with.' }
      return { ok: true, why: `GitHub vouches for them — ${isAdminRecord.value && v.fb ? asUser : asOutsider}.` }
    case 'github-users':
      if (!v.github) return { ok: false, why: 'No GitHub account to sign in with.' }
      return v.fb
        ? { ok: true, why: 'GitHub email matches a Fresh Breath user, so in they go, as that user.' }
        : { ok: false, why: 'GitHub signs them in, but no Fresh Breath user has that email. Refused.' }
  }
}

const results = computed(() => visitors.map((v) => ({ ...v, ...verdict(v) })))
const openToWorld = computed(() => effective.value === 'github')
</script>

<template>
  <div class="sim">
    <div class="sim-label">Try it · Who gets through the door?</div>

    <div class="sim-row">
      <span class="sim-q">This app’s <b>Protected by</b></span>
      <div class="chips" role="radiogroup" aria-label="Protected by">
        <button type="button" role="radio" :aria-checked="protectedBy === 'inherit'" @click="protectedBy = 'inherit'">(empty)</button>
        <button v-for="(n, g) in gateNames" :key="g" type="button" role="radio" :aria-checked="protectedBy === g" @click="protectedBy = g">{{ n }}</button>
      </div>
    </div>

    <div class="sim-row" :class="{ dim: protectedBy !== 'inherit' }">
      <span class="sim-q">Server’s <b>Admin auth</b> <small v-if="protectedBy !== 'inherit'">(not used — the app has its own gate)</small></span>
      <div class="chips" role="radiogroup" aria-label="Admin auth">
        <button v-for="g in (['ssh', 'sso', 'github'] as const)" :key="g" type="button" role="radio" :aria-checked="adminAuth === g" @click="adminAuth = g">{{ gateNames[g] }}</button>
      </div>
    </div>

    <p class="sim-status">
      <template v-if="protectedBy === 'inherit'">Empty doesn’t mean open: this app answers to the admin auth, <b>{{ gateNames[adminAuth] }}</b>.</template>
      <template v-else>This app answers to its own gate, <b>{{ gateNames[protectedBy] }}</b>.</template>
    </p>

    <div v-if="openToWorld" class="sim-alarm">
      🚨 Anyone on the internet with a GitHub account gets in{{ protectedBy === 'inherit' ? ' — here, and in every other app and service with an empty Protected by' : '' }}.
      Keep this to a LAN, or tick <b>Fresh Breath users only</b> on the record.
    </div>

    <div class="visitors">
      <div v-for="r in results" :key="r.id" class="visitor" :class="r.ok ? 'in' : 'out'">
        <div class="visitor-head"><b>{{ r.name }}</b><span class="verdict">{{ r.ok ? '✅ In' : '🚫 Stopped' }}</span></div>
        <small class="visitor-note">{{ r.note }}</small>
        <p>{{ r.why }}</p>
      </div>
    </div>
  </div>
</template>
