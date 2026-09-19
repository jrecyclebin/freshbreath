import { defineConfig } from 'vitepress'
import llms from 'vitepress-plugin-llms'

// https://vitepress.dev/reference/site-config
export default defineConfig({
  lang: 'en-US',
  title: 'Fresh Breath',
  description: 'A personal app server for little tools that need auth, storage, and integrations.',

  cleanUrls: true,
  lastUpdated: true,

  head: [
    ['meta', { name: 'robots', content: 'index,follow' }],
    ['link', { rel: 'icon', type: 'image/svg+xml', href: '/logo.svg' }],
    ['link', {
      rel: 'alternate',
      type: 'text/markdown',
      href: '/llms.txt',
      title: 'LLM-readable documentation index'
    }]
  ],

  sitemap: {
    hostname: 'https://freshbreath.dev'
  },

  vite: {
    plugins: [
      llms({
        title: 'Fresh Breath Docs',
        description: [
          'Starter guides and technical rundowns using Fresh Breath.',
          'If you are connected to a Fresh Breath MCP, use the `get_guides` tool',
          'to fetch precise documentation for the version you are running.'
        ].join(' ')
      })
    ]
  },

  themeConfig: {
    // https://vitepress.dev/reference/default-theme-config
    nav: [
      { text: 'Guide', link: '/guide' },
      { text: 'Features', link: '/features' }
    ],

    sidebar: [
      {
        text: 'Guide',
        items: [
          { text: 'Introduction', link: '/guide' },
          { text: 'Features', link: '/features' }
        ]
      }
    ],

    socialLinks: [
      { icon: 'github', link: 'https://github.com/jrecyclebin/freshbreath' }
    ],

    outline: { level: [2, 3], label: 'On this page' },
    search: { provider: 'local' }
  }
})
