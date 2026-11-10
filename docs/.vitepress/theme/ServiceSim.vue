<script setup lang="ts">
// Episode 3: register a service, link it to an app, and call it from the
// app's page. An app can only reach services linked to it; an OAuth MCP
// server (Notion, say) only works proxied. Each step shows the MCP call an
// agent would make to do the same thing.
import { computed, reactive, ref } from 'vue'
import { useBeat, BrowserFrame, SAMPLE_NONCE } from './kit'

const PRESETS = {
  github: { name: 'GitHub', type: 'api', url: 'https://api.github.com', oauth: false, icon: '🐙' },
  notion: { name: 'Notion', type: 'mcp', url: 'https://mcp.notion.com/mcp', oauth: true, icon: '📝' }
} as const
type Key = keyof typeof PRESETS

const pick = ref<Key>('github')
const proxied = ref(true)
const services = reactive<{ id: number, key: Key, proxied: boolean, linked: boolean }[]>([])
const result = ref<{ kind: string, text: string } | null>(null)
const log = ref<string[]>([])
const seen = reactive({ created: false, refused: false, worked: false, notion: false })
const beat = useBeat()

const preset = computed(() => PRESETS[pick.value])
const existing = computed(() => services.find((s) => s.key === pick.value))

function note(line: string) {
  log.value.push(line)
  setTimeout(() => document.querySelector('.service-sim .log')?.scrollTo({ top: 99999 }), 0)
}

function create() {
  if (existing.value) { existing.value.proxied = proxied.value; note(`update_service {"name":"${preset.value.name}","descriptor":{"proxied":${proxied.value}}}`); return }
  const id = 4 + services.length
  services.push({ id, key: pick.value, proxied: proxied.value, linked: false })
  note(`create_service {"name":"${preset.value.name}","url":"${preset.value.url}","descriptor":{"type":"${preset.value.type}","proxied":${proxied.value}}}`)
  seen.created = true
  check()
}

function toggleLink(s: typeof services[number]) {
  s.linked = !s.linked
  note(`set_app_services {"nonce":"${SAMPLE_NONCE}","services":[${services.filter((x) => x.linked).map((x) => x.id).join(',')}]}`)
}

function call(s: typeof services[number]) {
  const p = PRESETS[s.key]
  if (!s.linked) {
    result.value = { kind: 'bad', text: `🚫 login("${p.url}") refused: that service isn’t linked to this app. Apps only reach the services you link to them.` }
    seen.refused = true
  } else if (p.oauth && !s.proxied) {
    result.value = { kind: 'bad', text: `🚫 Notion signs people in with OAuth, and an OAuth MCP server only works when the service is proxied. Tick “Proxied” and save.` }
  } else if (p.type === 'mcp') {
    result.value = { kind: 'ok', text: `✅ Signed in to Notion (one popup), then listTools() → notion-search, notion-fetch, notion-create-pages…` }
    seen.worked = true
    seen.notion = true
  } else if (!s.proxied) {
    result.value = { kind: 'meh', text: `⚠️ Straight from the browser to api.github.com. That only works for an API that allows CORS, and no credential gets added. Proxy it and Fresh Breath sends the right one for you.` }
  } else {
    result.value = { kind: 'ok', text: `✅ svc.fetch("/user/repos") → 200, through /service/${s.id}/user/repos. Fresh Breath added the credential on the way out.` }
    seen.worked = true
  }
  check()
}

function check() {
  if (seen.created && seen.refused && seen.worked && seen.notion) beat.pass('Service connected!')
}
</script>

<template>
  <div class="sim service-sim">
    <div class="sim-head"><b>Try it: give an app a service</b></div>
    <div class="sim-body grid">
      <BrowserFrame url="https://localhost:9009/control/services" title="Services · Fresh Breath" icon="🌬️" accent="#3f5bd8">
        <div class="wa-head"><b>🔌 Services</b><small>control panel</small></div>
        <div class="wa-body form">
          <label>Start from
            <select v-model="pick">
              <option value="github">GitHub API</option>
              <option value="notion">Notion MCP</option>
            </select>
          </label>
          <div class="kv"><span>Type</span><code>{{ preset.type }}</code></div>
          <div class="kv"><span>URL</span><code>{{ preset.url }}</code></div>
          <label class="check"><input v-model="proxied" type="checkbox"> Proxied <span class="wa-muted">— route calls through Fresh Breath</span></label>
          <button class="wa-btn" @click="create">{{ existing ? 'Save' : 'Create service' }}</button>
          <ul v-if="services.length" class="wa-list">
            <li v-for="s in services" :key="s.id" class="svc">
              <span>{{ PRESETS[s.key].icon }} {{ PRESETS[s.key].name }} <span class="wa-muted">#{{ s.id }}{{ s.proxied ? ' · proxied' : '' }}</span></span>
              <button class="wa-btn small" :class="s.linked ? 'plain' : 'ghost'" @click="toggleLink(s)">{{ s.linked ? '✓ Linked to Tix' : 'Link to Tix' }}</button>
            </li>
          </ul>
        </div>
      </BrowserFrame>

      <BrowserFrame url="https://localhost:9009/tix/" title="Tix" icon="🎫" accent="#6d4bd8">
        <div class="wa-head"><b>🎫 Tix</b><small>your app</small></div>
        <div class="wa-body">
          <p v-if="!services.length" class="wa-muted" style="margin:0">No services on the server yet. Create one on the left.</p>
          <div v-for="s in services" :key="s.id" class="wa-row" style="margin-bottom:8px">
            <button class="wa-btn" @click="call(s)">Use {{ PRESETS[s.key].name }}</button>
            <code class="code">login("{{ PRESETS[s.key].url }}")</code>
          </div>
          <div v-if="result" :key="result.text" class="verdict pop" :class="result.kind">{{ result.text }}</div>
        </div>
      </BrowserFrame>
    </div>
    <div class="sim-body" style="padding-top:0">
      <div class="log">
        <div class="dim">What your agent would call over MCP to do the same:</div>
        <div v-for="(l, i) in log" :key="i" class="hl">{{ l }}</div>
      </div>
    </div>
    <ul class="quests">
      <li class="quests-head">Quests</li>
      <li :class="{ got: seen.created }">Create a service</li>
      <li :class="{ got: seen.refused }">Use it from Tix before it’s linked</li>
      <li :class="{ got: seen.worked }">Link it, and use it for real</li>
      <li :class="{ got: seen.notion }">Get Notion working too (hint: it signs in with OAuth)</li>
    </ul>
  </div>
</template>

<style scoped>
.grid { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); gap: 12px; align-items: start }
.form { display: flex; flex-direction: column; gap: 9px }
.form label select { display: block; width: 100%; margin-top: 3px }
.kv { display: flex; gap: 8px; align-items: baseline; font-size: 13px }
.kv span { width: 40px; color: var(--wa-soft); font-weight: 600; font-size: 12.5px }
.kv code { font-size: 12px; overflow-wrap: anywhere }
.check { display: flex !important; gap: 6px; align-items: center; color: var(--wa-ink) !important; font-size: 13.5px !important }
.svc { display: flex; justify-content: space-between; align-items: center; gap: 8px; flex-wrap: wrap }
.wa-btn.small { padding: 3px 9px; font-size: 12px }
.code { font-size: 11.5px; overflow-wrap: anywhere }
.verdict { font-size: 13.5px; margin-top: 4px }
@media (max-width: 760px) { .grid { grid-template-columns: minmax(0, 1fr) } }
</style>
