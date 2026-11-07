<script setup lang="ts">
// Episode 8: a working tool script, run against a real SQLite database in the
// browser (sql.js). It reads the same format Fresh Breath does, for SQL steps:
// `[name] description` headers, `---` between tools, a SQL verb at the start
// of a line with indented continuation lines, and `$vars` as bound
// parameters. It also flags the mistakes the real parser rejects.
import { computed, onMounted, reactive, ref, shallowRef, watch } from 'vue'
import { store, useBeat } from './kit'

const START = `[migrate] Create the tables if they don't exist.

CREATE TABLE IF NOT EXISTS issues (
  id INTEGER PRIMARY KEY,
  title TEXT NOT NULL,
  status TEXT DEFAULT 'open',
  added_by TEXT)
---
[add-issue] File a new issue.

INSERT INTO issues (title, added_by)
  VALUES ($title, $token_email)
---
[list-issues] List the issues with a status: open or closed.

SELECT id, title, status, added_by
  FROM issues
  WHERE status = $status
  ORDER BY id DESC
---
[close-issue] Close an issue.

UPDATE issues SET status = 'closed'
  WHERE id = $id`

const SQL_VERB = /^(SELECT|INSERT|UPDATE|DELETE FROM|REPLACE|WITH|CREATE|DROP|ALTER)\b/i
const HTTP_VERB = /^(GET|POST|PUT|PATCH|DELETE|HEAD)\s+\S/
const SIGNED_IN: Record<string, string | number> = { token_id: 7, token_email: 'dana@yourteam.dev', token_sub: 'ext:entra:4f2a' }

interface Tool { name: string, description: string, steps: string[], vars: string[], line: number }

const script = ref(START)
const db = shallowRef<any>(null)
const loadError = ref('')
const chosen = ref('migrate')
const args = reactive<Record<string, string>>({})
const output = ref<{ ok: boolean, columns?: string[], rows?: any[][], text?: string } | null>(null)
const tables = ref<{ name: string, count: number }[]>([])
const seen = reactive({ migrate: false, added: 0, listed: false, closed: false, broke: false })
const beat = useBeat()

// ---- Parse -------------------------------------------------------------------

const parsed = computed(() => {
  const tools: Tool[] = []
  const errors: string[] = []
  const lines = script.value.split('\n')
  let tool: Tool | null = null
  let step: string[] | null = null

  const endStep = () => { if (tool && step) tool.steps.push(step.join('\n')); step = null }
  const endTool = () => { endStep(); if (tool) tools.push(tool); tool = null }

  lines.forEach((line, i) => {
    const n = i + 1
    if (line.trim() === '---') { endTool(); return }
    if (!tool) {
      if (!line.trim()) return
      const head = line.match(/^\[(!?)([A-Za-z0-9-]+)\]\s*(.*)$/)
      if (!head) { errors.push(`Line ${n}: a tool starts with [tool-name] and a description.`); tool = { name: '?', description: '', steps: [], vars: [], line: n }; return }
      tool = { name: head[2], description: head[3], steps: [], vars: [], line: n }
      return
    }
    if (!line.trim()) return
    if (/^\s/.test(line)) {
      if (step) step.push(line)
      else if (!line.trim().startsWith('#')) errors.push(`Line ${n}: indented, but there's no SQL step above it to continue.`)
      return
    }
    if (line.startsWith('#') || /^\$\w+\s+is\s+\w+\??\s*$/.test(line) || /^WATCH\b/.test(line)) { endStep(); return }
    if (SQL_VERB.test(line)) { endStep(); step = [line]; return }
    if (HTTP_VERB.test(line)) { endStep(); errors.push(`Line ${n}: HTTP steps work on a real Fresh Breath, but this sandbox only runs SQL.`); return }
    errors.push(`Line ${n}: “${line.trim().slice(0, 40)}” isn't indented, so it isn't part of the query above. Indent every line after the first, closing parentheses too.`)
  })
  endTool()

  for (const t of tools) {
    if (!t.steps.length && t.name !== '?') errors.push(`[${t.name}] has no SQL step.`)
    const vars = new Set<string>()
    for (const s of t.steps) {
      for (const lit of s.match(/'(?:[^']|'')*'/g) || []) {
        if (/(^|[^$])\$\w/.test(lit)) errors.push(`[${t.name}]: ${lit} puts a $variable inside quotes. It can't be bound there. Pass the whole value instead, like LIKE $pattern.`)
      }
      for (const m of s.matchAll(/(?<!\$)\$([A-Za-z_]\w*)/g)) vars.add(m[1])
    }
    t.vars = [...vars]
  }
  return { tools, errors }
})

const current = computed(() => parsed.value.tools.find((t) => t.name === chosen.value) || parsed.value.tools[0])
watch(() => parsed.value.errors.length, (n) => { if (n) seen.broke = true; check() })

// ---- Run -----------------------------------------------------------------------

onMounted(async () => {
  try {
    // @ts-ignore: sql.js ships no types
    const [{ default: initSqlJs }, { default: wasm }] = await Promise.all([import('sql.js'), import('sql.js/dist/sql-wasm.wasm?url')])
    const SQL = await initSqlJs({ locateFile: () => wasm })
    db.value = new SQL.Database()
  } catch (e: any) {
    loadError.value = 'Couldn’t start the in-browser database: ' + e.message
  }
})

function refreshTables() {
  const res = db.value.exec("SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name")
  tables.value = (res[0]?.values || []).map(([name]: [string]) => ({
    name, count: db.value.exec(`SELECT count(*) FROM "${name}"`)[0].values[0][0]
  }))
}

function coerce(v: string | undefined) {
  if (v === undefined || v === '') return null
  return /^-?\d+(\.\d+)?$/.test(v.trim()) ? Number(v) : v
}

function run() {
  const tool = current.value
  if (!tool || !db.value) return
  if (parsed.value.errors.length) {
    output.value = { ok: false, text: 'Fix the script first. Fresh Breath refuses to load a file with errors, and so does this sandbox.' }
    return
  }
  try {
    let last: any[] = []
    for (const step of tool.steps) {
      const binds: Record<string, any> = {}
      for (const m of step.matchAll(/(?<!\$)\$([A-Za-z_]\w*)/g)) {
        binds['$' + m[1]] = m[1] in SIGNED_IN ? SIGNED_IN[m[1]] : coerce(args[m[1]])
      }
      last = db.value.exec(step, binds)
    }
    if (last.length) {
      output.value = { ok: true, columns: last[0].columns, rows: last[0].values }
    } else {
      output.value = { ok: true, text: `Done. ${db.value.getRowsModified()} row(s) changed.` }
    }
    if (tool.name === 'migrate') seen.migrate = true
    if (tool.name === 'add-issue') seen.added++
    if (tool.name === 'list-issues' && (last[0]?.values.length || 0) >= 2) seen.listed = true
    if (tool.name === 'close-issue' && db.value.getRowsModified() > 0) seen.closed = true
    refreshTables()
    check()
  } catch (e: any) {
    const hint = /no such table/.test(e.message) ? ' Run migrate first: it creates the tables.' : ''
    output.value = { ok: false, text: e.message + hint }
  }
}

function check() {
  if (seen.migrate && seen.added >= 2 && seen.listed && seen.closed && seen.broke) beat.pass('You wrote a database!')
}

function resetScript() { script.value = START }
</script>

<template>
  <div class="sim tool-sim">
    <div class="sim-head">
      <b>Try it: a real tool script, a real database</b>
      <span class="spacer" />
      <span class="note" style="margin:0">Signed in as dana@yourteam.dev</span>
      <button class="chip" @click="resetScript">↺ Reset script</button>
    </div>
    <div class="sim-body grid">
      <div>
        <label class="inline">service file · app:{{ store.setup.nonce || 'aB3xK9mQ2p' }}
          <textarea v-model="script" class="editor mono" spellcheck="false" rows="22" />
        </label>
        <ul v-if="parsed.errors.length" class="errors">
          <li v-for="e in parsed.errors" :key="e">🚫 {{ e }}</li>
        </ul>
        <p v-else class="ok-line">✅ Script loads. {{ parsed.tools.length }} tools.</p>
      </div>
      <div>
        <div class="tools">
          <button v-for="t in parsed.tools" :key="t.name" class="chip" :class="{ on: current?.name === t.name }" @click="chosen = t.name; output = null">{{ t.name }}</button>
        </div>
        <template v-if="current">
          <p class="desc">{{ current.description }}</p>
          <div class="args">
            <template v-for="v in current.vars" :key="v">
              <label v-if="v in SIGNED_IN" class="inline">${{ v }} <input type="text" :value="SIGNED_IN[v] + ' (filled in by Fresh Breath)'" disabled></label>
              <label v-else class="inline">${{ v }} <input v-model="args[v]" type="text" :placeholder="v === 'status' ? 'open' : ''" @keydown.enter="run"></label>
            </template>
            <p v-if="!current.vars.length" class="note" style="margin:0">No arguments.</p>
          </div>
          <button class="btn" :disabled="!db" @click="run">{{ db ? '▶ callTool("' + current.name + '")' : 'Loading SQLite…' }}</button>
        </template>
        <div v-if="loadError" class="verdict bad">{{ loadError }}</div>
        <div v-if="output" :key="JSON.stringify(output)" class="out pop">
          <div v-if="!output.ok" class="verdict bad">{{ output.text }}</div>
          <div v-else-if="output.text" class="verdict ok">{{ output.text }}</div>
          <table v-else>
            <thead><tr><th v-for="c in output.columns" :key="c">{{ c }}</th></tr></thead>
            <tbody>
              <tr v-for="(r, i) in output.rows" :key="i"><td v-for="(c, j) in r" :key="j">{{ c }}</td></tr>
              <tr v-if="!output.rows?.length"><td :colspan="output.columns?.length">No rows.</td></tr>
            </tbody>
          </table>
        </div>
        <p v-if="tables.length" class="note">app.db: <span v-for="t in tables" :key="t.name"><code>{{ t.name }}</code> ({{ t.count }} rows) </span></p>
      </div>
    </div>
    <ul class="quests">
      <li class="quests-head">Quests</li>
      <li :class="{ got: seen.migrate }">Run <code>migrate</code> to create the table</li>
      <li :class="{ got: seen.added >= 2 }">File two issues with <code>add-issue</code> ({{ Math.min(seen.added, 2) }}/2)</li>
      <li :class="{ got: seen.listed }">List the open ones</li>
      <li :class="{ got: seen.closed }">Close one by its id</li>
      <li :class="{ got: seen.broke }">Break a rule on purpose: un-indent a <code>WHERE</code> line, or put <code>$title</code> in quotes</li>
    </ul>
  </div>
</template>

<style scoped>
.grid { display: grid; grid-template-columns: minmax(0, 1.1fr) minmax(0, 1fr); gap: 16px }
.editor { width: 100%; font-size: 13px !important; line-height: 1.5; font-family: var(--mono) !important; resize: vertical; white-space: pre; tab-size: 2 }
.errors { list-style: none; padding: 0 !important; margin: 8px 0 0; font-size: 14px; color: var(--red) }
.errors li { margin-bottom: 4px }
.ok-line { margin: 8px 0 0; font-size: 14px; color: var(--mint) }
.tools { display: flex; flex-wrap: wrap; gap: 6px }
.chip.on { background: var(--cobalt); border-color: var(--cobalt); color: var(--surface) }
.desc { margin: 10px 0; font-size: 15px; color: var(--ink-soft) }
.args { display: grid; gap: 8px; margin-bottom: 12px }
.args input:disabled { opacity: .7 }
.out { margin-top: 12px }
.out table { display: table !important; margin: 0 !important; font-size: 14px !important }
@media (max-width: 760px) { .grid { grid-template-columns: minmax(0, 1fr) } }
</style>
