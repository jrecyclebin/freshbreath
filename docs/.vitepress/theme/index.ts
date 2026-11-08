import type { Theme } from 'vitepress'
import Layout from './Layout.vue'
import { Beat, Say } from './kit'
import ChatSim from './ChatSim.vue'
import PlugSim from './PlugSim.vue'
import OnboardingSim from './OnboardingSim.vue'
import ServiceSim from './ServiceSim.vue'
import SlugSim from './SlugSim.vue'
import SlotSim from './SlotSim.vue'
import BaseSim from './BaseSim.vue'
import GateSim from './GateSim.vue'
import ServiceAuthSim from './ServiceAuthSim.vue'
import LoginSim from './LoginSim.vue'
import ToolSim from './ToolSim.vue'
import WatchSim from './WatchSim.vue'
import SyncSim from './SyncSim.vue'
import './style.css'

export default {
  Layout,
  enhanceApp({ app }) {
    const parts = {
      Beat, Say, ChatSim, PlugSim, OnboardingSim, ServiceSim, SlugSim, SlotSim, BaseSim,
      GateSim, ServiceAuthSim, LoginSim, ToolSim, WatchSim, SyncSim
    }
    for (const [name, part] of Object.entries(parts)) app.component(name, part)
  }
} satisfies Theme
