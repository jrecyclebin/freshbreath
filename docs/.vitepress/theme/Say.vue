<script setup lang="ts">
// A prompt to hand an agent, written in markdown as a ```say fence.
// {server} {app} {nonce} {slug} fill in from the reader's setup;
// [bracketed words] are left for the reader to fill in themselves.
import { computed, onMounted, ref } from 'vue'
import { loadSetup, setupValue } from './setup'

const props = defineProps<{ text: string; label?: string }>()
const copied = ref(false)

const parts = computed(() =>
  decodeURIComponent(props.text)
    .split(/(\{(?:server|app|nonce|slug)\}|\[[^\]\n]+\])/)
    .filter(Boolean)
    .map((p) => {
      const key = p.match(/^\{(server|app|nonce|slug)\}$/)?.[1]
      if (key) {
        const v = setupValue(key)
        return { text: v.text, kind: v.filled ? 'filled' : 'unfilled' }
      }
      if (/^\[.+\]$/.test(p)) return { text: p, kind: 'yours' }
      return { text: p, kind: 'plain' }
    })
)
const needsSetup = computed(() => parts.value.some((p) => p.kind === 'unfilled'))

async function copy() {
  const text = parts.value.map((p) => p.text).join('')
  try {
    await navigator.clipboard.writeText(text)
  } catch {
    const t = document.createElement('textarea')
    t.value = text
    document.body.appendChild(t)
    t.select()
    document.execCommand('copy')
    t.remove()
  }
  copied.value = true
  setTimeout(() => (copied.value = false), 1600)
}

onMounted(loadSetup)
</script>

<template>
  <div class="say">
    <div class="say-label">{{ label || 'Say this to your agent' }}</div>
    <div class="say-text"><span v-for="(p, i) in parts" :key="i" :class="p.kind">{{ p.text }}</span></div>
    <div class="say-bar">
      <button class="fb-btn small" type="button" @click="copy">{{ copied ? 'Copied!' : 'Copy prompt' }}</button>
      <a v-if="needsSetup" class="say-setup" href="/agents#your-setup">Fill in your app's details</a>
    </div>
  </div>
</template>
