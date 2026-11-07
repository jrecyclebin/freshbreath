<script setup lang="ts">
// Episode 9: two pages watching one service. A call that changes something
// sends an event; pages showing that list refresh. Take the WATCH lines out
// and every call is reported, reads included, which is how a page that
// refreshes on events ends up refreshing forever.
import { reactive, ref } from 'vue'
import { useBeat } from './kit'

const LISTS: Record<number, string> = { 12: 'Groceries', 7: 'Hardware' }
const db = reactive<Record<number, string[]>>({ 12: ['Milk', 'Eggs'], 7: ['Nails'] })
const windows = reactive([
  { who: 'Your laptop', icon: '💻', list: 12, items: ['Milk', 'Eggs'], draft: '', flash: 0, watching: true },
  { who: 'Dana’s phone', icon: '📱', list: 12, items: ['Milk', 'Eggs'], draft: '', flash: 0, watching: true }
])
const directives = ref(true)
const events = ref<{ text: string, hot?: boolean }[]>([])
const burst = ref(0)
const looped = ref(false)
const seen = reactive({ live: false, ignored: false, loop: false })
const beat = useBeat()

const wait = (ms: number) => new Promise((r) => setTimeout(r, ms))

function check() {
  if (seen.live && seen.ignored && seen.loop) beat.pass('Live updates, understood')
}

function emit(ev: { tool: string, key?: number }, from: number) {
  events.value.push({ text: JSON.stringify(ev), hot: burst.value > 3 })
  if (events.value.length > 40) events.value.shift()
  setTimeout(() => document.querySelector('.watch-sim .log')?.scrollTo({ top: 99999 }), 0)
  windows.forEach((w, i) => {
    if (!w.watching) return
    if (ev.key !== undefined && ev.key !== w.list) {
      if (i === 0 && from === 1) seen.ignored = true
      return
    }
    refresh(i, from)
  })
  check()
}

// A tool call. Reads report themselves only when the file has no WATCH lines.
async function call(tool: 'add-item' | 'list-items', i: number) {
  await wait(260)
  const w = windows[i]
  if (tool === 'add-item') {
    db[w.list].push(w.draft.trim() || 'Thing ' + (db[w.list].length + 1))
    w.draft = ''
    w.items = [...db[w.list]]
    emit(directives.value ? { tool, key: w.list } : { tool }, i)
  } else {
    w.items = [...db[w.list]]
    if (!directives.value) {
      if (++burst.value > 14) {
        looped.value = true
        seen.loop = true
        check()
        return
      }
      emit({ tool }, i)
    }
  }
}

function refresh(i: number, from: number) {
  if (looped.value) return
  const w = windows[i]
  w.flash++
  if (i === 0 && from === 1) seen.live = true
  call('list-items', i)
}

function add(i: number) {
  burst.value = 0
  looped.value = false
  call('add-item', i)
}

function switchList(i: number) {
  windows[i].items = [...db[windows[i].list]]
}

function toggleDirectives() {
  directives.value = !directives.value
  looped.value = false
  burst.value = 0
}
</script>

<template>
  <div class="sim watch-sim">
    <div class="sim-head">
      <b>Try it: two pages, one service</b>
      <span class="spacer" />
      <label class="toggle"><input type="checkbox" :checked="directives" @change="toggleDirectives"> WATCH lines in the file</label>
    </div>
    <div class="sim-body">
      <div class="grid">
        <div v-for="(w, i) in windows" :key="w.who" class="win">
          <div class="win-top">
            <b>{{ w.icon }} {{ w.who }}</b>
            <label class="toggle small"><input v-model="w.watching" type="checkbox"> watch()</label>
          </div>
          <select v-model.number="w.list" @change="switchList(i)">
            <option v-for="(name, id) in LISTS" :key="id" :value="Number(id)">List {{ id }}: {{ name }}</option>
          </select>
          <ul :key="w.flash" class="items" :class="{ flash: w.flash }">
            <li v-for="(it, j) in w.items" :key="j">{{ it }}</li>
          </ul>
          <div class="row">
            <input v-model="w.draft" type="text" class="grow" placeholder="Add an item…" @keydown.enter="add(i)">
            <button class="btn small" @click="add(i)">Add</button>
          </div>
        </div>
        <div class="side-panel">
          <pre class="file mono"><span>[add-item] Add an item to a list.</span>
INSERT INTO items (list_id, name)
  VALUES ($list_id, $name)
<span v-if="directives" class="hl">WATCH $list_id</span><span v-else class="gone">WATCH $list_id</span>
---
[list-items] Read a list.
SELECT name FROM items
  WHERE list_id = $list_id</pre>
          <div class="log">
            <div class="dim">events from /service/watch</div>
            <div v-for="(e, k) in events" :key="k" :class="{ hl: e.hot }">{{ e.text }}</div>
          </div>
        </div>
      </div>
      <div v-if="looped" class="verdict bad pop">🔥 Feedback loop! Each refresh calls <code>list-items</code>, which is reported because the file has no WATCH lines, which makes every page refresh again… The sim pulled the plug. Put the WATCH line back.</div>
    </div>
    <ul class="quests">
      <li class="quests-head">Quests</li>
      <li :class="{ got: seen.live }">Add an item on Dana’s phone. Watch your laptop update by itself.</li>
      <li :class="{ got: seen.ignored }">Switch Dana to Hardware and add there. Your laptop shows Groceries, so it ignores the event.</li>
      <li :class="{ got: seen.loop }">Untick “WATCH lines in the file” and add an item. Uh-oh.</li>
    </ul>
  </div>
</template>

<style scoped>
.grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)) minmax(0, 1.1fr); gap: 10px }
.win { border: 1.5px solid var(--line); border-radius: 12px; padding: 12px; background: var(--bg); display: flex; flex-direction: column; gap: 8px }
.win-top { display: flex; justify-content: space-between; align-items: center; gap: 6px; flex-wrap: wrap }
.win-top b { font-family: var(--display) }
.items { list-style: none; padding: 8px !important; margin: 0; border-radius: 8px; background: var(--surface); min-height: 90px }
.items li { margin: 0; padding: 3px 0; border-bottom: 1px solid var(--line); font-size: 15px }
.items li:last-child { border: 0 }
.toggle { display: inline-flex; gap: 6px; align-items: center; font-family: var(--mono); font-size: 13px; cursor: pointer }
.toggle.small { font-size: 12px; color: var(--ink-soft) }
.side-panel { display: flex; flex-direction: column; gap: 8px; min-width: 0 }
.file { margin: 0; padding: 10px 12px; border-radius: 8px; background: var(--code-bg); color: var(--code-ink); font-size: 12px; line-height: 1.5; overflow-x: auto }
.file span:first-child { color: #9fb4f5 }
.file .hl { color: var(--ph); font-weight: 600 }
.file .gone { text-decoration: line-through; opacity: .35 }
.log { max-height: 150px }
@media (max-width: 860px) { .grid { grid-template-columns: minmax(0, 1fr) minmax(0, 1fr) } .side-panel { grid-column: 1 / -1 } }
@media (max-width: 520px) { .grid { grid-template-columns: minmax(0, 1fr) } }
</style>
