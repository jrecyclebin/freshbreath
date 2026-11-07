<script setup lang="ts">
// Episode 4: type an app's URL setting and see the address it's served at,
// slot by slot, with the server's rules for slugs.
import { computed, reactive, ref, watch } from 'vue'
import { store, useBeat } from './kit'

const RESERVED = ['api', 'control', 'mcp', 'service', 'oauth']
const raw = ref('tix')
const name = ref('Tix')
const slot = ref('')
const seen = reactive({ valid: false, reserved: false, staging: false, deep: false })
const beat = useBeat()

const server = computed(() => (store.setup.server || 'http://localhost:9009').replace(/\/+$/, ''))
const slugify = (s: string) => s.toLowerCase().trim().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '')

const result = computed(() => {
  const value = raw.value.trim()
  if (/^https?:\/\//.test(value)) {
    return { ok: true, url: value.replace(/\/?$/, '/'), note: 'A full URL: Fresh Breath doesn’t serve this one. It just lets that origin call in (CORS) with the app’s nonce. If the host is on your Hosts list, the app is served there one level down.' }
  }
  const slug = value.replace(/^\/+|\/+$/g, '')
  if (!slug) {
    const s = slugify(name.value)
    return { ok: true, slug: s, url: `${server.value}/${s}${slot.value}/`, note: `Left blank, the URL is the slug of the app’s name. (With no files uploaded, it’s treated as a page you’re running from disk.)` }
  }
  if (slug.includes('/')) {
    return { ok: false, note: `Apps can’t sit in subdirectories. Make a “${slug.split('/')[0]}” app and publish into its “${slug.split('/').slice(1).join('/')}” folder instead.` }
  }
  if (RESERVED.includes(slug.toLowerCase())) {
    return { ok: false, reserved: true, note: `“${slug}” belongs to Fresh Breath itself. The reserved names are ${RESERVED.join(', ')}.` }
  }
  const cleaned = value !== slug ? ' (Leading and trailing slashes are cleaned up for you.)' : ''
  return { ok: true, slug, url: `${server.value}/${slug}${slot.value}/`, note: 'Served from the ' + (slotName.value) + ' copy.' + cleaned }
})

const slotName = computed(() => ({ '': 'default', '@dev': 'Development', '@staging': 'Staging', '@prod': 'Production' } as any)[slot.value])

watch([result, slot], () => {
  if (result.value.ok && result.value.slug) seen.valid = true
  if (result.value.reserved) seen.reserved = true
  if (result.value.ok && result.value.slug === 'tix' && slot.value === '@staging') seen.staging = true
  if (!result.value.ok && !result.value.reserved) seen.deep = true
  if (seen.valid && seen.reserved && seen.staging && seen.deep) beat.pass('You know where apps live')
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
      <div v-if="result.ok" class="urlbar" style="margin-top:14px"><span class="lock">●</span>{{ result.url }}</div>
      <div class="verdict" :class="result.ok ? 'ok' : 'bad'">{{ result.ok ? '✅ ' : '🚫 ' }}{{ result.note }}</div>
    </div>
    <ul class="quests">
      <li class="quests-head">Quests</li>
      <li :class="{ got: seen.valid }">Give the app a slug</li>
      <li :class="{ got: seen.reserved }">Try to steal one of Fresh Breath’s own names</li>
      <li :class="{ got: seen.deep }">Try to put an app in a subdirectory, like <code>tix/admin</code></li>
      <li :class="{ got: seen.staging }">Find the Staging address for an app at <code>tix</code></li>
    </ul>
  </div>
</template>
