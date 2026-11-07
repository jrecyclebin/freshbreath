<script setup lang="ts">
// svc.watch(): every watcher hears every caller's finished calls, with the
// WATCH key riding along so a page can ignore what it isn't showing.
import { computed, reactive, ref } from 'vue'

const useKey = ref(true)
const items = reactive<Record<number, string[]>>({ 7: ['Milk'], 12: ['Frog food', 'Lily pads'] })
const people = reactive([
  { name: 'Ana', list: 12, shown: ['Frog food', 'Lily pads'], flash: '' },
  { name: 'Ben', list: 12, shown: ['Frog food', 'Lily pads'], flash: '' }
])
const events = ref<string[]>([])
let n = 0

const toolFile = computed(() =>
  useKey.value
    ? '[add-item] Add an item to a list.\n\nINSERT INTO items (list_id, name)\n  VALUES ($list_id, $name)\nWATCH $list_id'
    : '[add-item] Add an item to a list.\n\nINSERT INTO items (list_id, name)\n  VALUES ($list_id, $name)\nWATCH'
)

function view(p: (typeof people)[number], list: number) {
  p.list = list
  p.shown = [...items[list]]
  p.flash = ''
}

function add(who: (typeof people)[number]) {
  const thing = ['Snails', 'Pond salt', 'Net', 'Bug jar', 'Duckweed'][n++ % 5]
  items[who.list].push(thing)
  const ev = useKey.value ? `{ tool: "add-item", key: ${who.list} }` : '{ tool: "add-item" }'
  events.value = [`${who.name} added “${thing}” → every watcher gets ${ev}`, ...events.value].slice(0, 4)
  for (const p of people) {
    const mine = !useKey.value || p.list === who.list
    if (mine) {
      p.shown = [...items[p.list]]
      p.flash = p === who ? 'saved' : 'refreshed'
    } else {
      p.flash = 'ignored'
    }
    setTimeout(() => (p.flash = ''), 1400)
  }
}
</script>

<template>
  <div class="sim">
    <div class="sim-label">Try it · Two browsers watching one service</div>
    <div class="sim-row toggles">
      <label><input type="checkbox" v-model="useKey" /> Key the event with <code>WATCH $list_id</code></label>
    </div>

    <div class="watch-grid">
      <pre class="sim-code"><code>{{ toolFile }}</code></pre>
      <div v-for="p in people" :key="p.name" class="browser" :class="p.flash">
        <div class="browser-bar"><span class="dots" aria-hidden="true">●●●</span> {{ p.name }}’s browser</div>
        <div class="browser-body">
          <div class="chips small" role="radiogroup" :aria-label="p.name + '’s list'">
            <button v-for="l in [7, 12]" :key="l" type="button" role="radio" :aria-checked="p.list === l" @click="view(p, l)">List {{ l }}</button>
          </div>
          <ul><li v-for="it in p.shown" :key="it">{{ it }}</li></ul>
          <button class="fb-btn small" type="button" @click="add(p)">Add an item</button>
          <span class="browser-flash">{{ p.flash === 'refreshed' ? '↻ refreshed' : p.flash === 'ignored' ? 'heard it, not my list' : p.flash === 'saved' ? '✓ saved' : '' }}</span>
        </div>
      </div>
    </div>

    <ul class="sim-log" aria-live="polite">
      <li v-for="(e, i) in events" :key="i + e">{{ e }}</li>
      <li v-if="!events.length">Add an item in either browser.</li>
    </ul>
  </div>
</template>
