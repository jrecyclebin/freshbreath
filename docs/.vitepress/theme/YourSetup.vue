<script setup lang="ts">
import { onMounted } from 'vue'
import { setup, loadSetup } from './setup'

const fields = [
  { key: 'server', label: 'Server address', placeholder: 'http://localhost:9009', hint: 'Where your Fresh Breath runs' },
  { key: 'app', label: 'App name', placeholder: 'Tix', hint: 'What you call it' },
  { key: 'nonce', label: 'App nonce', placeholder: 'aB3xK9mQ2p', hint: '10 characters, under the app’s name in /control' },
  { key: 'slug', label: 'Web path', placeholder: 'tix', hint: 'The end of the app’s address' }
] as const

function clear() {
  for (const f of fields) (setup as any)[f.key] = ''
}

onMounted(loadSetup)
</script>

<template>
  <div class="sim your-setup">
    <div class="sim-label">Your setup</div>
    <p class="sim-intro">
      Fill this in for the app you’re working on. Every “Say this” prompt on every page fills itself in from here.
      It’s saved in this browser only.
    </p>
    <div class="setup-grid">
      <label v-for="f in fields" :key="f.key" class="field">
        <span>{{ f.label }}</span>
        <input v-model="(setup as any)[f.key]" :placeholder="f.placeholder" spellcheck="false" autocomplete="off" />
        <small>{{ f.hint }}</small>
      </label>
    </div>
    <p class="sim-foot"><button class="fb-btn ghost small" type="button" @click="clear">Clear</button></p>
  </div>
</template>
