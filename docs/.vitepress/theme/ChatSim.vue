<script setup lang="ts">
// Plays a Markdown list of @you / @claude lines as a conversation: the reader
// sends each "You" line, and Claude's replies type themselves in. Without
// JavaScript (and in llms.txt) it is just the list, which reads fine.
import { computed, nextTick, onMounted, ref } from 'vue'
import { useBeat } from './kit'

const props = defineProps<{ title?: string }>()
const box = ref<HTMLElement>()
const turns = ref<{ el: HTMLElement, who: string, text: string }[]>([])
const shown = ref(0)
const typing = ref(false)
const all = ref(false)
const beat = useBeat()

const next = computed(() => turns.value[shown.value])
const over = computed(() => shown.value >= turns.value.length)

function paint() {
  turns.value.forEach((t, i) => { t.el.hidden = !all.value && i >= shown.value })
}

onMounted(() => {
  const lis = Array.from(box.value!.querySelectorAll(':scope > ul > li')) as HTMLElement[]
  turns.value = lis.map((el) => {
    const who = el.querySelector('.who')?.classList.contains('you') ? 'you' : 'claude'
    const clone = el.cloneNode(true) as HTMLElement
    clone.querySelector('.who')?.remove()
    return { el, who, text: clone.innerText.trim() }
  })
  paint()
  // Start with Claude talking if the script opens with Claude.
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
  <div class="sim chat-sim">
    <div class="sim-head">
      <b>{{ props.title || 'Try it: a chat with Claude' }}</b>
      <span class="spacer" />
      <button class="chip" @click="restart">↺ Restart</button>
      <button v-if="!over" class="chip" @click="showAll">Show all</button>
    </div>
    <div class="sim-body chat">
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
  </div>
</template>
