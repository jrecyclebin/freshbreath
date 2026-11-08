<script setup lang="ts">
// Plays a Markdown list of @you / @claude lines as a conversation: the reader
// sends each "You" line, and Claude's replies type themselves in. Without
// JavaScript (and in llms.txt) it is just the list, which reads fine.
//
// With a `file` name, it also shows that file growing beside the chat: a
// code block inside a Claude line is the file as Claude left it at that
// point. The block moves into the side panel, and lines that weren't there
// before light up.
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { useBeat } from './kit'

const props = defineProps<{ title?: string, file?: string }>()
const box = ref<HTMLElement>()
const turns = ref<{ el: HTMLElement, who: string, text: string, script?: string }[]>([])
const shown = ref(0)
const typing = ref(false)
const all = ref(false)
const beat = useBeat()

const next = computed(() => turns.value[shown.value])
const over = computed(() => shown.value >= turns.value.length)

// The file as of the last revealed turn that changed it, and the version
// before that, for highlighting.
const versions = computed(() => turns.value.slice(0, shown.value).map((t) => t.script).filter((s): s is string => s !== undefined))
const scriptNow = computed(() => versions.value[versions.value.length - 1] ?? '')
const scriptLines = computed(() => {
  const before = new Set((versions.value[versions.value.length - 2] ?? '').split('\n'))
  return scriptNow.value.split('\n').map((text) => ({ text, fresh: versions.value.length > 0 && text.trim() !== '' && !before.has(text) }))
})

// Keep the newest change in view as the file grows.
const scriptBox = ref<HTMLElement>()
watch(scriptNow, () => nextTick(() => {
  const fresh = scriptBox.value?.querySelector('.sl.fresh') as HTMLElement | null
  if (fresh && scriptBox.value) scriptBox.value.scrollTo({ top: fresh.offsetTop - 24, behavior: 'smooth' })
}))

function paint() {
  turns.value.forEach((t, i) => { t.el.hidden = !all.value && i >= shown.value })
}

onMounted(() => {
  const lis = Array.from(box.value!.querySelectorAll(':scope > ul > li')) as HTMLElement[]
  turns.value = lis.map((el) => {
    const who = el.querySelector('.who')?.classList.contains('you') ? 'you' : 'claude'
    let script: string | undefined
    const code = props.file ? el.querySelector('div[class*="language-"]') : null
    if (code) {
      script = (code.querySelector('pre code') as HTMLElement)?.innerText.replace(/\n$/, '') ?? ''
      const chip = document.createElement('span')
      chip.className = 'file-chip'
      chip.textContent = '✎ updated ' + props.file
      code.replaceWith(chip)
    }
    const clone = el.cloneNode(true) as HTMLElement
    clone.querySelector('.who')?.remove()
    return { el, who, text: clone.innerText.trim(), script }
  })
  paint()
  if (next.value && next.value.who === 'claude') replies()
})

const wait = (ms: number) => new Promise((r) => setTimeout(r, ms))

async function replies() {
  while (next.value && next.value.who === 'claude') {
    typing.value = true
    await wait(Math.min(1400, 500 + next.value.text.length * 6))
    typing.value = false
    shown.value++
    paint()
    await nextTick()
    turns.value[shown.value - 1].el.classList.add('pop')
  }
  if (over.value) beat.pass('Conversation complete')
}

async function send() {
  if (!next.value || typing.value) return
  shown.value++
  paint()
  await replies()
}

function restart() {
  all.value = false
  shown.value = 0
  paint()
  if (next.value?.who === 'claude') replies()
}

function showAll() {
  all.value = true
  shown.value = turns.value.length
  paint()
}
</script>

<template>
  <div class="sim chat-sim" :class="{ 'with-file': file }">
    <div class="sim-head">
      <b>{{ props.title || 'Try it: a chat with Claude' }}</b>
      <span class="spacer" />
      <button class="chip" @click="restart">↺ Restart</button>
      <button v-if="!over" class="chip" @click="showAll">Show all</button>
    </div>
    <div class="sim-body chat-grid">
      <div class="chat">
        <div ref="box"><slot /></div>
        <div v-if="typing" class="typing" aria-label="Claude is typing"><i /><i /><i /></div>
        <div v-if="!over && next?.who === 'you'" class="composer">
          <p>{{ next.text }}</p>
          <button class="btn small" @click="send">Send ↵</button>
        </div>
        <div v-else-if="over" class="composer idle">
          <p>That's the whole exchange. In real life, you'd keep going from here.</p>
        </div>
      </div>
      <div v-if="file" class="script-panel">
        <div class="script-name mono">{{ file }}</div>
        <pre v-if="scriptNow" ref="scriptBox" class="script mono"><code><span v-for="(l, i) in scriptLines" :key="i + l.text" class="sl" :class="{ fresh: l.fresh }">{{ l.text || ' ' }}</span></code></pre>
        <p v-else class="script-empty">Empty. Claude hasn't written anything yet.</p>
      </div>
    </div>
  </div>
</template>

<style scoped>
.chat-grid { display: block }
.with-file .chat-grid { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); gap: 14px; align-items: start }
.script-panel { position: sticky; top: 12px; border-radius: 10px; background: var(--code-bg); overflow: hidden }
.script-name { font-size: 12px; color: var(--ph); padding: 8px 12px; border-bottom: 1px solid #2a2f3a }
.script { position: relative; margin: 0; padding: 10px 0; font-size: 12.5px; line-height: 1.55; color: var(--code-ink); overflow: auto; max-height: 560px }
.sl { display: block; padding: 0 12px; white-space: pre; transition: background 1.5s }
.sl.fresh { background: color-mix(in srgb, var(--mint) 28%, transparent); box-shadow: inset 3px 0 0 var(--mint) }
.script-empty { margin: 0; padding: 18px 12px; color: #8b93a3; font-size: 14px; font-style: italic }
.chat :deep(.file-chip) { display: inline-block; margin-top: 6px; font-family: var(--mono); font-size: 12px; padding: 2px 8px; border-radius: 5px; background: var(--code-bg); color: var(--ph) }
@media (max-width: 860px) { .with-file .chat-grid { grid-template-columns: minmax(0, 1fr) } .script-panel { position: static } }
</style>
