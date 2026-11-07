<script setup lang="ts">
// Episode 6: pick the admin auth and an app's "Protected by", then watch four
// visitors try the door. The point: empty means "inherit", never "open".
import { computed, reactive, ref, watch } from 'vue'
import { useBeat } from './kit'

type Gate = 'ssh' | 'sso' | 'github' | 'anon'
const GATES: Record<Gate, string> = {
  ssh: 'Built-in: SSH key passphrase',
  sso: 'Company SSO (Microsoft Entra)',
  github: 'GitHub',
  anon: 'Anonymous'
}
const admin = ref<Gate>('ssh')
const protectedBy = ref<'' | Gate>('')
const beat = useBeat()

const visitors = [
  { name: 'You', icon: '🧑‍💻', about: 'Fresh Breath superuser, work account, GitHub', has: ['ssh', 'sso', 'github'] },
  { name: 'Dana', icon: '👩‍🔬', about: 'Coworker. Work account, no Fresh Breath account', has: ['sso'] },
  { name: 'Sam', icon: '🕵️', about: 'Stranger on the internet with a GitHub account', has: ['github'] },
  { name: 'Passerby', icon: '🚶', about: 'No accounts at all', has: [] as string[] }
]

const effective = computed<Gate>(() => protectedBy.value || admin.value)

function reason(v: typeof visitors[number]) {
  const g = effective.value
  if (g === 'anon') return { in: true, why: 'Anonymous means open. Anybody.' }
  if (v.has.includes(g)) {
    if (g === 'ssh') return { in: true, why: 'Has a Fresh Breath account and passphrase.' }
    if (g === 'sso') return { in: true, why: v.name === 'You' ? 'Signs in with work.' : 'Work SSO vouches for Dana. No Fresh Breath account needed.' }
    return { in: true, why: v.name === 'Sam' ? 'GitHub vouches for anyone with a GitHub account. Including Sam.' : 'Signs in with GitHub.' }
  }
  return { in: false, why: g === 'ssh' ? 'Not a Fresh Breath user.' : `No ${g === 'sso' ? 'work' : 'GitHub'} account to sign in with.` }
}

const seen = reactive({ trap: false, coworkers: false, open: false, locked: false })
watch([admin, protectedBy], () => {
  if (!protectedBy.value && admin.value === 'github') seen.trap = true
  if (effective.value === 'sso') seen.coworkers = true
  if (protectedBy.value === 'anon') seen.open = true
  if (effective.value === 'ssh' && seen.trap) seen.locked = true
  if (Object.values(seen).every(Boolean)) beat.pass('You’re the bouncer now')
})
</script>

<template>
  <div class="sim gate-sim">
    <div class="sim-head"><b>Try it: who gets through the door?</b></div>
    <div class="sim-body">
      <div class="row">
        <label class="inline grow">Settings → Admin auth
          <select v-model="admin">
            <option v-for="g in (['ssh', 'sso', 'github'] as const)" :key="g" :value="g">{{ GATES[g] }}</option>
          </select>
        </label>
        <label class="inline grow">App → Protected by
          <select v-model="protectedBy">
            <option value="">(empty)</option>
            <option v-for="(label, g) in GATES" :key="g" :value="g">{{ label }}</option>
          </select>
        </label>
      </div>
      <p class="effective">
        The door uses: <strong>{{ GATES[effective] }}</strong>
        <span v-if="!protectedBy" class="note"> ← inherited from Admin auth, because Protected by is empty</span>
      </p>
      <div class="visitors">
        <div v-for="v in visitors" :key="v.name + effective" class="visitor pop" :class="reason(v).in ? 'in' : 'out'">
          <span class="face">{{ v.icon }}</span>
          <b>{{ v.name }} <span>{{ reason(v).in ? 'gets in' : 'stopped' }}</span></b>
          <small>{{ v.about }}</small>
          <p>{{ reason(v).why }}</p>
        </div>
      </div>
      <div v-if="!protectedBy && admin === 'github'" class="verdict bad">⚠️ This is the trap: a generic GitHub login as admin auth, and an empty Protected by. Every app and service that inherits it is open to anyone on Earth with a GitHub account. Keep that to a LAN, or give the app a gate of its own.</div>
    </div>
    <ul class="quests">
      <li class="quests-head">Quests</li>
      <li :class="{ got: seen.trap }">Set Admin auth to GitHub and leave Protected by empty. Who walks in?</li>
      <li :class="{ got: seen.locked }">Now lock it back down to Fresh Breath users only</li>
      <li :class="{ got: seen.coworkers }">Let coworkers in without making them Fresh Breath accounts</li>
      <li :class="{ got: seen.open }">Open the app to everyone, on purpose</li>
    </ul>
  </div>
</template>

<style scoped>
.effective { margin: 14px 0 12px }
.visitors { display: grid; grid-template-columns: repeat(auto-fit, minmax(190px, 1fr)); gap: 10px }
.visitor { border: 1.5px solid var(--line); border-radius: 10px; padding: 12px; background: var(--bg) }
.visitor .face { font-size: 30px; display: block }
.visitor b { display: block; font-family: var(--display); font-size: 18px }
.visitor b span { font-family: var(--mono); font-size: 12px; padding: 1px 7px; border-radius: 4px; vertical-align: 3px; margin-left: 4px }
.visitor.in { border-color: var(--mint) }
.visitor.in b span { background: var(--mint); color: var(--surface) }
.visitor.out { opacity: .8 }
.visitor.out b span { background: var(--red-soft); color: var(--red) }
.visitor small { display: block; color: var(--ink-soft); font-size: 13px; line-height: 1.4; margin-bottom: 6px }
.visitor p { margin: 0; font-size: 14.5px }
</style>
