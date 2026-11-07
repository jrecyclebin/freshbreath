<script setup lang="ts">
// Development, Staging and Production: uploads land in Development, deploys
// copy a slot forward, the bare URL serves the default environment, and the
// database is shared by all three.
import { computed, ref } from 'vue'

type Slot = 'dev' | 'staging' | 'prod'
const names: Record<Slot, string> = { dev: 'Development', staging: 'Staging', prod: 'Production' }

const versions = ref<Record<Slot, number | null>>({ dev: 1, staging: null, prod: null })
const defaultEnv = ref<Slot>('dev')
const rows = ref(3)
const log = ref<string[]>(['Created the app and uploaded v1 to Development.'])

function say(m: string) {
  log.value = [m, ...log.value].slice(0, 5)
}
function publish() {
  versions.value.dev = (versions.value.dev || 0) + 1
  say(`Your agent wrote v${versions.value.dev} to Development. Nothing else moved.`)
}
function deploy(source: Slot, target: Slot) {
  if (versions.value[source] == null) return
  versions.value[target] = versions.value[source]
  say(`Deployed ${names[source]} → ${names[target]} (v${versions.value[target]}).`)
}
function addRow() {
  rows.value++
  say('Someone saved a row. Every slot sees it right away — the database is shared.')
}
function reset() {
  versions.value = { dev: 1, staging: null, prod: null }
  defaultEnv.value = 'dev'
  rows.value = 3
  log.value = ['Created the app and uploaded v1 to Development.']
}

const urls = computed(() => {
  const list: { url: string; slot: Slot }[] = [
    { url: '/tix', slot: defaultEnv.value },
    { url: '/tix@dev', slot: 'dev' },
    { url: '/tix@staging', slot: 'staging' },
    { url: '/tix@prod', slot: 'prod' }
  ]
  return list.map((u) => ({ ...u, v: versions.value[u.slot] }))
})
const hue = (v: number | null) => (v == null ? 0 : (v * 67) % 360)
</script>

<template>
  <div class="sim">
    <div class="sim-label">Try it · Three slots, one database</div>

    <div class="slots">
      <div v-for="s in (['dev', 'staging', 'prod'] as const)" :key="s" class="slot" :class="{ empty: versions[s] == null, isdefault: defaultEnv === s }">
        <div class="slot-head">
          <b>{{ names[s] }}</b>
          <small v-if="defaultEnv === s">default</small>
        </div>
        <div class="slot-version" :style="versions[s] != null ? { '--v-hue': hue(versions[s]) } : {}">
          {{ versions[s] == null ? 'not deployed' : 'v' + versions[s] }}
        </div>
        <div class="slot-actions">
          <button v-if="s === 'dev'" class="fb-btn small" type="button" @click="publish">✏️ Agent edits a file</button>
          <button v-if="s === 'dev'" class="fb-btn ghost small" type="button" :disabled="versions.staging == null" @click="deploy('staging', 'dev')">Restore from Staging</button>
          <button v-if="s === 'staging'" class="fb-btn small" type="button" @click="deploy('dev', 'staging')">Deploy Development →</button>
          <button v-if="s === 'prod'" class="fb-btn small" type="button" :disabled="versions.staging == null" @click="deploy('staging', 'prod')">Promote Staging →</button>
          <button v-if="defaultEnv !== s" class="fb-btn ghost small" type="button" @click="defaultEnv = s; say(`Default environment is now ${names[s]}.`)">Make default</button>
        </div>
      </div>
    </div>

    <div class="db-strip">
      <span>🗄️ <b>app.db</b> — {{ rows }} rows, shared by every slot</span>
      <button class="fb-btn ghost small" type="button" @click="addRow">Save a row</button>
    </div>

    <table class="sim-table">
      <thead><tr><th>Visit</th><th>Serves</th></tr></thead>
      <tbody>
        <tr v-for="u in urls" :key="u.url">
          <td><code>{{ u.url }}</code></td>
          <td>
            <span v-if="u.v != null" class="vchip" :style="{ '--v-hue': hue(u.v) }">v{{ u.v }}</span>
            <span v-else class="vchip none">404 — honest, no fallback</span>
            <small> {{ names[u.slot] }}</small>
          </td>
        </tr>
      </tbody>
    </table>

    <ul class="sim-log" aria-live="polite">
      <li v-for="(l, i) in log" :key="i + l">{{ l }}</li>
    </ul>
    <p class="sim-foot"><button class="fb-btn ghost small" type="button" @click="reset">Start over</button></p>
  </div>
</template>
