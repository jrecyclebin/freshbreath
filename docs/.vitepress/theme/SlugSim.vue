<script setup lang="ts">
// Episode 4: type an app's URL setting and see the address it's served at,
// slot by slot, with the server's rules for slugs.
import { computed, reactive, ref, watch } from 'vue'
import { store, useBeat, slugProblem, RESERVED_SLUGS, BrowserFrame } from './kit'

const raw = ref('tix')
const name = ref('Tix')
const slot = ref('')
const seen = reactive({ valid: false, reserved: false, staging: false, deep: false, shape: false })
const beat = useBeat()

const server = computed(() => (store.setup.server || 'http://localhost:9009').replace(/\/+$/, ''))
// The server's own slugify: lowercase, runs of anything else become one dash.
const slugify = (s: string) => s.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '')

const result = computed(() => {
  const value = raw.value.trim()
  if (value.includes('://')) {
    if (!/^https?:\/\/[^/\s]+/i.test(value)) return { ok: false, note: 'A full URL has to be http or https, with a host.' }
    return { ok: true, url: value.replace(/\/?$/, '/'), note: 'A full URL. If its host is on your Hosts list, the app is served there, one level down. Anywhere else, Fresh Breath doesn’t serve it; it just lets that origin call in with the app’s nonce.' }
  }
  const slug = value.toLowerCase().replace(/^\/+|\/+$/g, '')
  if (!slug) {
    const s = slugify(name.value)
    return { ok: true, slug: s, url: `${server.value}/${s}${slot.value}/`, note: 'Left blank, the app is served at the slug of its name. (With no files uploaded, it’s treated as a page you’re running from disk.)' }
  }
  const problem = slugProblem(slug)
  if (problem) {
    return { ok: false, reserved: RESERVED_SLUGS.includes(slug), deep: slug.includes('/'), note: problem }
  }
  const cleaned = value !== slug ? ' (Stored as “' + slug + '”: it’s lowercased and trimmed of slashes.)' : ''
  return { ok: true, slug, url: `${server.value}/${slug}${slot.value}/`, note: 'Served from the ' + slotName.value + ' copy.' + cleaned }
})

const slotName = computed(() => ({ '': 'default', '@dev': 'Development', '@staging': 'Staging', '@prod': 'Production' } as any)[slot.value])

watch([result, slot], () => {
  if (result.value.ok && result.value.slug) seen.valid = true
  if (result.value.reserved) seen.reserved = true
  if (result.value.ok && result.value.slug === 'tix' && slot.value === '@staging') seen.staging = true
  if (!result.value.ok && result.value.deep) seen.deep = true
  if (!result.value.ok && !result.value.deep && !result.value.reserved && !raw.value.includes('://')) seen.shape = true
  if (seen.valid && seen.reserved && seen.staging && seen.deep && seen.shape) beat.pass('You know where apps live')
}, { immediate: true })
</script>

<template>
  <div class="sim slug-sim">
    <div class="sim-head"><b>Try it: an app's address</b></div>
    <div class="sim-body">
      <div class="row">
        <label class="inline grow">App name<input v-model="name" type="text"></label>
        <label class="inline grow">URL setting<input v-model="raw" type="text" class="mono" spellcheck="false" placeholder="blank, a slug, or a full URL"></label>
        <label class="inline">Slot
          <select v-model="slot">
            <option value="">default</option>
            <option value="@dev">@dev</option>
            <option value="@staging">@staging</option>
            <option value="@prod">@prod</option>
          </select>
        </label>
      </div>
      <BrowserFrame v-if="result.ok" :url="result.url" :title="name || 'App'" icon="🎫" accent="#6d4bd8" style="margin-top:14px">
        <div class="wa-head"><b>🎫 {{ name || 'App' }}</b><small>{{ slotName }}</small></div>
      </BrowserFrame>
      <div class="verdict" :class="result.ok ? 'ok' : 'bad'">{{ result.ok ? '✅ ' : '🚫 ' }}{{ result.note }}</div>
    </div>
    <ul class="quests">
      <li class="quests-head">Quests</li>
      <li :class="{ got: seen.valid }">Give the app a slug</li>
      <li :class="{ got: seen.reserved }">Try to steal one of Fresh Breath’s own names</li>
      <li :class="{ got: seen.deep }">Try to put an app in a subdirectory, like <code>tix/admin</code></li>
      <li :class="{ got: seen.shape }">Try a slug the server won’t take, like <code>my_app</code> or <code>tix--2</code></li>
      <li :class="{ got: seen.staging }">Find the Staging address for an app at <code>tix</code></li>
    </ul>
  </div>
</template>
