import DefaultTheme from 'vitepress/theme'
import type { Theme } from 'vitepress'
import './style.css'

import Say from './Say.vue'
import YourSetup from './YourSetup.vue'
import GateSim from './GateSim.vue'
import LoginSim from './LoginSim.vue'
import SlotSim from './SlotSim.vue'
import WatchSim from './WatchSim.vue'
import ToolPlayground from './ToolPlayground.vue'

export default {
  extends: DefaultTheme,
  enhanceApp({ app }) {
    app.component('Say', Say)
    app.component('YourSetup', YourSetup)
    app.component('GateSim', GateSim)
    app.component('LoginSim', LoginSim)
    app.component('SlotSim', SlotSim)
    app.component('WatchSim', WatchSim)
    app.component('ToolPlayground', ToolPlayground)
  }
} satisfies Theme
