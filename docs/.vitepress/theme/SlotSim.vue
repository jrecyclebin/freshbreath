<script setup lang="ts">
// Episode 5: three slots, a default environment, and the address bar that
// shows which copy each URL serves. Every button logs the API call behind it.
import { computed, reactive, ref } from 'vue'
import { store, useBeat } from './kit'

type Slot = 'dev' | 'staging' | 'prod'
const slots = reactive<Record<Slot, number | null>>({ dev: 1, staging: null, prod: null })
const defaultEnv = ref<Slot>('dev')
const version = ref(1)
const view = ref<'' | '@dev' | '@staging' | '@prod'>('')
const log = ref<{ text: string, api?: string }[]>([{ text: 'v1 uploaded to Development' }])
const seen = reactive({ honest404: false, staged: false, live: false, restored: false })
const beat = useBeat()

const slug = computed(() => store.setup.slug || 'tix')
const nonce = computed(() => store.setup.nonce || 'aB3xK9mQ2p')
const label: Record<Slot, string> = { dev: 'Development', staging: 'Staging', prod: 'Production' }

const serving = computed<Slot>(() => (view.value ? (view.value.slice(1) as Slot) : defaultEnv.value))
const page = computed(() => slots[serving.value])

function note(text: string, body?: object) {
  log.value.push({ text, api: body ? `POST /api/apps/${nonce.value}/deploy ${JSON.stringify(body)}` : undefined })
  setTimeout(() => document.querySelector('.slot-sim .log')?.scrollTo({ top: 99999 }), 0)
}

function upload() {
  version.value++
  slots.dev = version.value
  note(`v${version.value} uploaded to Development (uploads always land here)`)
}
function deployStaging() {
  slots.staging = slots.dev
  seen.staged = true
  note(`Development (v${slots.dev}) copied to Staging`, { target: 'staging' })
  check()
}
function promote() {
  if (slots.staging == null) return
  slots.prod = slots.staging
  note(`Staging (v${slots.staging}) promoted to Production`, { source: 'staging', target: 'prod' })
  check()
}
function restore() {
  if (slots.staging == null) return
  slots.dev = slots.staging
  seen.restored = true
  note(`Staging (v${slots.staging}) restored over Development`, { source: 'staging', target: 'dev' })
  check()
}
function setDefault(s: Slot) {
  defaultEnv.value = s
  note(`Default environment set to ${label[s]}: /${slug.value} now serves it`)
  check()
}
function look(v: typeof view.value) {
  view.value = v
  if (slots[serving.value] == null) seen.honest404 = true
  check()
}

function check() {
  seen.live = defaultEnv.value === 'prod' && (slots.prod ?? 0) >= 2
  if (slots[serving.value] == null) seen.honest404 = true
  if (seen.honest404 && seen.staged && seen.live && seen.restored) beat.pass('You shipped it!')
}
</script>

<template>
  <div class="sim slot-sim">
    <div class="sim-head">
      <b>Try it: ship a version</b>
      <span class="spacer" />
      <button class="btn small" @click="upload">⬆ Upload v{{ version + 1 }}</button>
    </div>
    <div class="sim-body">
      <div class="slots">
        <div v-for="s in (['dev', 'staging', 'prod'] as const)" :key="s + slots[s]" class="slot pop" :class="[s, { empty: slots[s] == null, default: defaultEnv === s }]">
          <div class="slot-name">{{ label[s] }}<span v-if="defaultEnv === s" class="badge">default</span></div>
          <div class="slot-ver">{{ slots[s] == null ? '—' : 'v' + slots[s] }}</div>
          <div class="slot-url mono">/{{ slug }}@{{ s }}</div>
          <div class="slot-actions">
            <button v-if="s === 'dev'" class="btn small" @click="deployStaging">Deploy → Staging</button>
            <button v-if="s === 'dev'" class="btn small ghost" :disabled="slots.staging == null" @click="restore">↺ Restore from Staging</button>
            <button v-if="s === 'staging'" class="btn small" :disabled="slots.staging == null" @click="promote">Deploy → Production</button>
            <button class="btn small quiet" :disabled="defaultEnv === s" @click="setDefault(s)">Make default</button>
          </div>
        </div>
      </div>

      <div class="browser">
        <div class="row">
          <span class="note" style="margin:0">Visit:</span>
          <button v-for="v in (['', '@dev', '@staging', '@prod'] as const)" :key="v" class="chip" :class="{ on: view === v }" @click="look(v)">/{{ slug }}{{ v }}</button>
        </div>
        <div class="urlbar" style="margin-top:8px"><span class="lock">●</span>{{ (store.setup.server || 'http://localhost:9009').replace(/\/$/, '') }}/{{ slug }}{{ view }}/</div>
        <div :key="serving + page" class="viewport pop" :class="{ missing: page == null }">
          <template v-if="page != null">
            <b>{{ store.setup.app || 'Tix' }} <span>v{{ page }}</span></b>
            <small>served from {{ label[serving] }}{{ view ? '' : ' (the default environment)' }}</small>
          </template>
          <template v-else>
            <b>404</b>
            <small>{{ label[serving] }} hasn’t been deployed yet. Fresh Breath won’t quietly serve a different slot instead.</small>
          </template>
        </div>
      </div>

      <div class="log">
        <div v-for="(l, i) in log" :key="i"><span class="dim">›</span> {{ l.text }}<div v-if="l.api" class="hl">  {{ l.api }}</div></div>
      </div>
    </div>
    <ul class="quests">
      <li class="quests-head">Quests</li>
      <li :class="{ got: seen.honest404 }">Visit a slot that hasn’t been deployed</li>
      <li :class="{ got: seen.staged }">Freeze a candidate in Staging</li>
      <li :class="{ got: seen.live }">Get v2 or later live at the bare <code>/{{ slug }}</code> address, served from Production</li>
      <li :class="{ got: seen.restored }">Rescue Development from a bad upload with Restore</li>
    </ul>
  </div>
</template>

<style scoped>
.slots { display: grid; grid-template-columns: repeat(auto-fit, minmax(190px, 1fr)); gap: 10px }
.slot { border: 1.5px solid var(--line); border-radius: 10px; padding: 12px; background: var(--bg); display: flex; flex-direction: column; gap: 6px }
.slot.dev { border-top: 4px solid var(--plum) }
.slot.staging { border-top: 4px solid var(--amber) }
.slot.prod { border-top: 4px solid var(--mint) }
.slot.default { box-shadow: 0 0 0 2px var(--cobalt) }
.slot-name { font-weight: 700; display: flex; justify-content: space-between; align-items: center }
.badge { font-family: var(--mono); font-size: 11px; background: var(--cobalt); color: var(--surface); padding: 1px 7px; border-radius: 4px }
.slot-ver { font-family: var(--display); font-size: 40px; font-weight: 800; line-height: 1 }
.slot.empty .slot-ver { color: var(--ink-soft) }
.slot-url { font-size: 12px; color: var(--ink-soft) }
.slot-actions { display: flex; flex-direction: column; gap: 6px; margin-top: auto }
.slot-actions .btn { justify-content: center }
.browser { margin: 16px 0 12px; padding: 12px; border: 1.5px solid var(--line); border-radius: 10px }
.chip.on { background: var(--cobalt); border-color: var(--cobalt); color: var(--surface) }
.viewport { margin-top: 10px; padding: 22px; border-radius: 8px; background: var(--mint-soft); text-align: center }
.viewport b { display: block; font-family: var(--display); font-size: 30px; font-weight: 800 }
.viewport b span { color: var(--mint) }
.viewport.missing { background: var(--red-soft); color: var(--red) }
</style>
