<script setup lang="ts">
// Turns a Markdown bullet list into tick boxes. Ticking them all counts as
// finishing the beat.
import { onMounted, ref } from 'vue'
import { useBeat } from './kit'

const props = defineProps<{ title?: string }>()
const box = ref<HTMLElement>()
const ticked = ref(0)
const total = ref(0)
const beat = useBeat()

onMounted(() => {
  const lis = Array.from(box.value!.querySelectorAll(':scope > ul > li')) as HTMLElement[]
  total.value = lis.length
  for (const li of lis) {
    const mark = document.createElement('span')
    mark.className = 'box'
    mark.textContent = '✓'
    li.prepend(mark)
    li.tabIndex = 0
    li.setAttribute('role', 'checkbox')
    li.setAttribute('aria-checked', 'false')
    const toggle = (e: Event) => {
      if ((e.target as HTMLElement).closest('a, button, code')) return
      const on = li.classList.toggle('ticked')
      li.setAttribute('aria-checked', String(on))
      ticked.value += on ? 1 : -1
      if (ticked.value === total.value) beat.pass('All set!')
    }
    li.addEventListener('click', toggle)
    li.addEventListener('keydown', (e) => { if (e.key === ' ' || e.key === 'Enter') { e.preventDefault(); toggle(e) } })
  }
})
</script>

<template>
  <div class="sim checklist">
    <div class="sim-head">
      <b>{{ props.title || 'Checklist' }}</b>
      <span class="spacer" />
      <span class="note" style="margin:0">{{ ticked }} of {{ total }}</span>
    </div>
    <div ref="box" class="sim-body"><slot /></div>
  </div>
</template>
