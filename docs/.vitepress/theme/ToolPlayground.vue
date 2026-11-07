<script setup lang="ts">
// A small mirror of the server's tool-file reader (internal/formats/virtual.go):
// enough to show which tools a file makes, what arguments each one takes, and
// the slips the real parser refuses. The server stays the authority.
import { computed, ref } from 'vue'

const examples: Record<string, string> = {
  'A database': `[migrate] Create the tables if they don't exist.

CREATE TABLE IF NOT EXISTS tasks (
  id INTEGER PRIMARY KEY,
  title TEXT NOT NULL,
  done INT DEFAULT 0,
  owner INT)
---
[add-task] Add a task for the signed-in person.

INSERT INTO tasks (title, owner)
  VALUES ($title, $token_id)
WATCH
---
[list-tasks] List tasks, optionally only the open ones.
$done is boolean?
$limit is number?

SELECT id, title, done
  FROM tasks
  WHERE $done IS NULL OR done = $done
  ORDER BY id DESC
  LIMIT coalesce($limit, 50)`,
  'A web API': `[list-pull-requests] List pull requests for a repository.
$state is "open" | "closed" | "all"

GET https://api.github.com/repos/$owner/$repo/pulls?state=$state
Authorization: Bearer $token

HTTP 200
---
[get-site] Resolve a SharePoint site URL to its id.

$site = url($url)
$hostname = $site.host
$pathname = $site.path
GET https://graph.microsoft.com/v1.0/sites/$hostname:$pathname
Authorization: Bearer $token

HTTP 200`,
  'Something’s off': `[find-notes] Search notes by title.

SELECT id, title FROM notes
  WHERE title LIKE '%$term%'
ORDER BY title
---
[Find-Notes] The same name again, in different case.

SELECT * FROM notes
  WHERE owner = $app_nonce`
}

const source = ref(examples['A database'])
const picked = ref('A database')
function load(name: string) {
  picked.value = name
  source.value = examples[name]
}

const SPECIAL = new Set(['token', 'token_email', 'token_sub', 'token_id', 'elicitation_url', 'handoff_url'])
const SQL_VERB = /^(SELECT|INSERT|UPDATE|DELETE FROM|REPLACE|WITH|CREATE|DROP|ALTER) /i
const HTTP_VERB = /^(GET|POST|PUT|PATCH|DELETE) (.+)$/
const TYPES = 'string|object|number|boolean|array|encrypted|email|uri|date-time|date|path'
const ANNOTATION = new RegExp(`^\\$([a-zA-Z_]\\w*(?:\\s*,\\s*\\$[a-zA-Z_]\\w*)*)\\s+is\\s+(${TYPES})(\\?)?$`)
const ENUM = /^\$([a-zA-Z_]\w*(?:\s*,\s*\$[a-zA-Z_]\w*)*)\s+is\s+((?:"[^"]*"|'[^']*')(?:\s*\|\s*(?:"[^"]*"|'[^']*'))*)(\?)?$/
const CLAUSE = /^(FROM|WHERE|AND|OR|ORDER|GROUP|HAVING|LIMIT|OFFSET|VALUES|SET|JOIN|LEFT|INNER|ON|RETURNING|\))/i

type Param = { name: string; type: string; optional: boolean }
type Tool = { name: string; desc: string; line: number; appOnly: boolean; kinds: Set<string>; watched: boolean; vars: string[]; assigned: Set<string>; types: Map<string, { type: string; optional: boolean }>; params: Param[]; specials: string[] }

const parsed = computed(() => {
  const lines = source.value.split('\n')
  const tools: Tool[] = []
  const problems: { line: number; text: string }[] = []
  let cur: Tool | null = null

  const varsIn = (s: string) => [...s.replace(/\$\$/g, '').matchAll(/\$([a-zA-Z_]\w*)/g)].map((m) => m[1])
  const use = (names: string[]) => names.forEach((n) => cur && !cur.vars.includes(n) && cur.vars.push(n))

  for (let i = 0; i < lines.length; i++) {
    const raw = lines[i]
    const t = raw.trim()
    if (t === '---') { cur = null; continue }

    const header = t.match(/^\[([^\]]+)\]\s*(.*)$/)
    if (header && /^[a-zA-Z][a-zA-Z0-9_-]*!?$/.test(header[1])) {
      const name = header[1].replace(/!$/, '')
      if (tools.some((x) => x.name.toLowerCase() === name.toLowerCase())) {
        problems.push({ line: i + 1, text: `Duplicate tool name “${name}”. Names are unique ignoring case, and [foo] and [foo!] collide.` })
      }
      cur = { name, desc: header[2], line: i + 1, appOnly: header[1].endsWith('!'), kinds: new Set(), watched: false, vars: [], assigned: new Set(), types: new Map(), params: [], specials: [] }
      tools.push(cur)
      continue
    }
    if (!cur || !t || t.startsWith('#')) continue

    const ann = t.match(ANNOTATION) || t.match(ENUM)
    if (ann) {
      const type = ANNOTATION.test(t) ? ann[2] : 'one of ' + ann[2].replace(/\s*\|\s*/g, ' | ')
      ann[1].split(',').forEach((n) => cur!.types.set(n.trim().replace(/^\$/, ''), { type, optional: !!ann[3] }))
      continue
    }
    const assign = t.match(/^\$([a-zA-Z_]\w*) = (.+)$/)
    if (assign) { cur.assigned.add(assign[1]); use(varsIn(assign[2])); continue }
    if (t === 'WATCH' || t.startsWith('WATCH ')) { cur.watched = true; use(varsIn(t.slice(5))); continue }

    if (SQL_VERB.test(t)) {
      cur.kinds.add('SQL')
      const sql = [t]
      let j = i + 1
      for (; j < lines.length; j++) {
        const r = lines[j]
        if (!r.trim()) continue
        if (r[0] !== ' ' && r[0] !== '\t') break
        if (!r.trim().startsWith('#')) sql.push(r.trim())
      }
      const next = lines[j]?.trim() || ''
      if (next && CLAUSE.test(next)) {
        problems.push({ line: j + 1, text: `“${next}” isn’t indented, so it ends the SQL step — the server would read it as a new step and the query stops short. Indent every line after the first.` })
      }
      const text = sql.join('\n')
      const quoted = text.match(/'[^']*\$([a-zA-Z_]\w*)[^']*'/)
      if (quoted) {
        problems.push({ line: i + 1, text: `$${quoted[1]} is inside a quoted string, where it can’t be a binding. Write LIKE $pattern and let the caller pass the wildcards.` })
      }
      use(varsIn(text.replace(/'[^']*'/g, '')))
      i = j - 1
      continue
    }
    const req = t.match(HTTP_VERB)
    if (req) { cur.kinds.add(req[2].startsWith('/') ? 'Fresh Breath API' : 'HTTP'); use(varsIn(req[2])); continue }
    use(varsIn(t))
  }

  for (const tool of tools) {
    if (tool.vars.includes('app_nonce')) {
      problems.push({ line: tool.line, text: `[${tool.name}] uses $app_nonce, which is reserved — the server supplies it for database tools. Rename it.` })
    }
    tool.specials = tool.vars.filter((v) => SPECIAL.has(v))
    const names = [...new Set([...tool.vars, ...tool.types.keys()])].filter((v) => !SPECIAL.has(v) && !tool.assigned.has(v))
    tool.params = names.map((n) => ({ name: n, type: tool.types.get(n)?.type || 'string', optional: !!tool.types.get(n)?.optional }))
  }
  return { tools, problems: problems.sort((a, b) => a.line - b.line) }
})

const callFor = (tool: Tool) =>
  `await svc.callTool("${tool.name}", { ${tool.params.map((p) => p.name).join(', ')} })`.replace('{  }', '{}')
</script>

<template>
  <div class="sim">
    <div class="sim-label">Try it · What does this tool file make?</div>
    <div class="sim-row">
      <span class="sim-q">Start from</span>
      <div class="chips" role="radiogroup" aria-label="Example">
        <button v-for="(_, name) in examples" :key="name" type="button" role="radio" :aria-checked="picked === name" @click="load(name)">{{ name }}</button>
      </div>
    </div>

    <div class="play-grid">
      <textarea v-model="source" class="play-src" spellcheck="false" aria-label="Tool file" @input="picked = ''"></textarea>
      <div class="play-out">
        <div v-if="parsed.problems.length" class="play-problems">
          <b>The server would refuse this:</b>
          <ul><li v-for="(p, i) in parsed.problems" :key="i"><code>line {{ p.line }}</code> {{ p.text }}</li></ul>
        </div>
        <div v-for="t in parsed.tools" :key="t.line" class="play-tool">
          <div class="play-tool-head">
            <b>{{ t.name }}</b>
            <span v-for="k in t.kinds" :key="k" class="tag">{{ k }}</span>
            <span v-if="t.watched" class="tag">watched</span>
            <span v-if="t.appOnly" class="tag warn">app-only</span>
          </div>
          <p>{{ t.desc || '(no description)' }}</p>
          <ul v-if="t.params.length" class="play-params">
            <li v-for="p in t.params" :key="p.name"><code>{{ p.name }}</code> {{ p.type }}<span v-if="p.optional" class="opt"> · optional</span></li>
          </ul>
          <p v-else class="opt">No arguments.</p>
          <p v-if="t.specials.length" class="opt">Filled in by the server: {{ t.specials.map((s) => '$' + s).join(', ') }}<template v-if="t.specials.some((s) => s !== 'token')"> — the caller has to be signed in.</template></p>
          <pre class="sim-code"><code>{{ callFor(t) }}</code></pre>
        </div>
        <p v-if="!parsed.tools.length" class="opt">No tools yet. Start one with a line like <code>[tool-name] What it does.</code></p>
      </div>
    </div>
    <p class="sim-foot opt">A simplified reader for learning the format. The server’s own parser has the final word.</p>
  </div>
</template>
