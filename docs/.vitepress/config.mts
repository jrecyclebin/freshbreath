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
    ['link', { rel: 'preconnect', href: 'https://fonts.googleapis.com' }],
    ['link', { rel: 'preconnect', href: 'https://fonts.gstatic.com', crossorigin: '' }],
    ['link', {
      rel: 'stylesheet',
      href: 'https://fonts.googleapis.com/css2?family=IBM+Plex+Mono:wght@400;500;600&family=IBM+Plex+Sans:ital,wght@0,400;0,500;0,600;0,700;1,400&display=swap'
    }],
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

  markdown: {
    config(md) {
      // ```say fences become copyable prompts (see theme/Say.vue). An optional
      // label follows the word: ```say Paste into CLAUDE.md
      const fence = md.renderer.rules.fence!
      md.renderer.rules.fence = (tokens, idx, options, env, self) => {
        const token = tokens[idx]
        const [lang, ...label] = token.info.trim().split(/\s+/)
        if (lang !== 'say') return fence(tokens, idx, options, env, self)
        const labelAttr = label.length ? ` label="${md.utils.escapeHtml(label.join(' '))}"` : ''
        return `<Say text="${encodeURIComponent(token.content.trim())}"${labelAttr} />\n`
      }

      // [You], [Claude] and [Admin] become little "who does this" tags.
      md.core.ruler.push('who_tags', (state) => {
        for (const block of state.tokens) {
          if (block.type !== 'inline' || !block.children) continue
          block.children = block.children.flatMap((child) => {
            if (child.type !== 'text' || !/\[(You|Claude|Admin)\]/.test(child.content)) return [child]
            return child.content.split(/(\[(?:You|Claude|Admin)\])/).filter(Boolean).map((piece) => {
              const who = piece.match(/^\[(You|Claude|Admin)\]$/)?.[1]
              const t = new state.Token(who ? 'html_inline' : 'text', '', 0)
              t.content = who ? `<span class="who ${who.toLowerCase()}">${who}</span>` : piece
              return t
            })
          })
        }
      })
    }
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
    logo: '/logo.svg',

    nav: [
      { text: 'Guide', link: '/setup' },
      { text: 'Reference', link: '/api-map' }
    ],

    sidebar: [
      {
        text: 'Quick Start',
        items: [
          { text: 'Four-step Setup', link: '/setup' },
          { text: 'The Full Tour', link: '/tour' },
          { text: 'Using with Agents', link: '/agents' }
        ]
      },
      {
        text: 'Building',
        items: [
          { text: 'Hosting Apps', link: '/apps' },
          { text: 'Logins and Gates', link: '/auth' },
          { text: 'Services', link: '/services' },
          { text: 'Tools and Databases', link: '/tools' }
        ]
      },
      {
        text: 'Reference',
        items: [
          { text: 'Glossary', link: '/glossary' },
          { text: 'API and MCP Map', link: '/api-map' }
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
