import type { Theme } from 'vitepress'
import Layout from './Layout.vue'
import { Beat, Say } from './kit'
import ChatSim from './ChatSim.vue'
import PlugSim from './PlugSim.vue'
import SlugSim from './SlugSim.vue'
import SlotSim from './SlotSim.vue'
import GateSim from './GateSim.vue'
import LoginSim from './LoginSim.vue'
import ToolSim from './ToolSim.vue'
import WatchSim from './WatchSim.vue'
import Checklist from './Checklist.vue'
import './style.css'

export default {
  Layout,
  enhanceApp({ app }) {
    const parts = { Beat, Say, ChatSim, PlugSim, SlugSim, SlotSim, GateSim, LoginSim, ToolSim, WatchSim, Checklist }
    for (const [name, part] of Object.entries(parts)) app.component(name, part)
  }
} satisfies Theme
