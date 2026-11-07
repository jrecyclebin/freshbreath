<script setup lang="ts">
// What login() does: ask the door what it needs, look in the store, and only
// open a window for a credential that's genuinely missing.
import { computed, ref } from 'vue'

const service = ref<'anon' | 'gated' | 'oauth'>('gated')
const gateCleared = ref(false)
const notionConnected = ref(false)
const fromClick = ref(true)
const mode = ref<'normal' | 'silent' | 'fresh'>('normal')

const url = computed(() =>
  service.value === 'anon' ? '/mcp/weather' : service.value === 'gated' ? '/mcp/app:' : 'https://mcp.notion.com/mcp'
)
const code = computed(() =>
  `const svc = await login("${url.value}"${mode.value === 'normal' ? '' : `, "${mode.value}"`});`
)

type Step = { icon: string; title: string; text: string; tone?: 'good' | 'bad' | 'wait' }

const run = computed(() => {
  const steps: Step[] = []
  if (service.value === 'anon') {
    steps.push({ icon: '🚪', title: 'Ask the door', text: 'resolveDoor() → { type: "anonymous" }. Nothing to clear.' })
    return { steps, result: { tone: 'good', text: 'Resolves instantly with a proxy. svc.session is null. No popup, no session.' } }
  }
  const legs = service.value === 'oauth' ? ['the app’s gate', 'Notion’s own OAuth'] : ['the app’s gate']
  steps.push({ icon: '🚪', title: 'Ask the door', text: `resolveDoor() → legs: ${legs.join(', then ')}.` })

  const missing: string[] = []
  if (mode.value === 'fresh') missing.push(...legs)
  else {
    if (!gateCleared.value) missing.push('the app’s gate')
    if (service.value === 'oauth' && !notionConnected.value) missing.push('Notion’s own OAuth')
  }

  if (mode.value === 'fresh') {
    steps.push({ icon: '🧹', title: 'Skip the store', text: '"fresh" forces a new login, whatever is stored.' })
  } else {
    const found = legs.filter((l) => !missing.includes(l))
    steps.push({
      icon: '🗄️',
      title: 'Look in the store',
      text: found.length
        ? `Found a credential for ${found.join(' and ')}${missing.length ? `. Still missing ${missing.join(' and ')}.` : '.'}`
        : `Nothing stored for ${missing.join(' or ')}.`,
      tone: missing.length ? 'wait' : 'good'
    })
  }

  if (!missing.length) {
    return { steps, result: { tone: 'good', text: 'Resolves with a proxy, no popup. Shared gates mean a sibling app’s login counts here too.' } }
  }
  if (mode.value === 'silent') {
    steps.push({ icon: '🤫', title: 'Silent mode', text: 'Never prompts.' })
    return { steps, result: { tone: 'wait', text: 'Returns null. Draw a “Connect” button and call login() again from its click.' } }
  }
  if (!fromClick.value) {
    steps.push({ icon: '🪟', title: 'Open a login window', text: 'No click behind the call, so the browser blocks the popup.', tone: 'bad' })
    return { steps, result: { tone: 'bad', text: 'Throws "The login window was blocked". Call login() from a click handler.' } }
  }
  steps.push({ icon: '🪟', title: 'Open a login window', text: `One window walks through ${missing.join(', then ')}.` })
  steps.push({ icon: '🔒', title: 'Store the result', text: 'A Fresh Breath token lands in the store, with any provider credential sealed inside it.' })
  return { steps, result: { tone: 'good', text: 'Resolves with a proxy. The next app behind the same gate won’t prompt at all.' } }
})
</script>

<template>
  <div class="sim">
    <div class="sim-label">Try it · What does login() cost?</div>

    <div class="sim-row">
      <span class="sim-q">The service</span>
      <div class="chips" role="radiogroup" aria-label="Service">
        <button type="button" role="radio" :aria-checked="service === 'anon'" @click="service = 'anon'">Anonymous service</button>
        <button type="button" role="radio" :aria-checked="service === 'gated'" @click="service = 'gated'">Behind the app’s gate</button>
        <button type="button" role="radio" :aria-checked="service === 'oauth'" @click="service = 'oauth'">An OAuth MCP (Notion)</button>
      </div>
    </div>

    <div class="sim-row">
      <span class="sim-q">Second argument</span>
      <div class="chips" role="radiogroup" aria-label="Mode">
        <button v-for="m in (['normal', 'silent', 'fresh'] as const)" :key="m" type="button" role="radio" :aria-checked="mode === m" @click="mode = m">{{ m }}</button>
      </div>
    </div>

    <div class="sim-row toggles" v-if="service !== 'anon'">
      <label><input type="checkbox" v-model="gateCleared" /> Already signed in to this gate (here or in a sibling app)</label>
      <label v-if="service === 'oauth'"><input type="checkbox" v-model="notionConnected" /> Already connected to Notion</label>
      <label><input type="checkbox" v-model="fromClick" /> Called from a click</label>
    </div>

    <pre class="sim-code"><code>{{ code }}</code></pre>

    <ol class="sim-steps">
      <li v-for="(s, i) in run.steps" :key="i" :class="s.tone">
        <span class="sim-step-icon" aria-hidden="true">{{ s.icon }}</span>
        <span><b>{{ s.title }}.</b> {{ s.text }}</span>
      </li>
    </ol>
    <p class="sim-result" :class="run.result.tone">{{ run.result.text }}</p>
  </div>
</template>
