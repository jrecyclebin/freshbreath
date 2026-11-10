import { defineConfig } from 'vitepress'
import llms from 'vitepress-plugin-llms'
import type MarkdownIt from 'markdown-it'

// Episodes are plain Markdown. Two small markdown-it rules give them their
// tutorial shape, so the source stays free of wrapper components:
//
// * Every `## Heading` starts a "beat": one bite-sized screen of an episode.
//   The section from that heading up to the next one is wrapped in <Beat>,
//   which the theme shows one at a time (Play) or all together (Read).
//   Content above the first `##` is the episode's intro and always shows.
//   A page with `beats: false` in its frontmatter is left as one plain page.
//
// * A paragraph that starts with `@you`, `@claude` or `@admin` gets a
//   speaker badge. In an ordered list those become the You/Claude step lists,
//   and inside <ChatSim> they become a playable conversation.
function tutorialMarkdown(md: MarkdownIt) {
  md.core.ruler.push('beats', (state) => {
    if (state.env.frontmatter?.beats === false) return
    const tokens = state.tokens
    const out: typeof tokens = []
    let open = false
    let index = 0
    for (let i = 0; i < tokens.length; i++) {
      const t = tokens[i]
      if (t.type === 'heading_open' && t.tag === 'h2') {
        const title = tokens[i + 1].content.replace(/"/g, '&quot;')
        const block = new state.Token('html_block', '', 0)
        block.content = (open ? '</Beat>\n' : '') + `<Beat :index="${index++}" title="${title}">\n`
        out.push(block)
        open = true
      }
      out.push(t)
    }
    if (open) {
      const close = new state.Token('html_block', '', 0)
      close.content = '</Beat>\n'
      out.push(close)
    }
    state.tokens = out
  })

  md.core.ruler.push('speakers', (state) => {
    for (const t of state.tokens) {
      if (t.type !== 'inline' || !t.children?.length) continue
      const first = t.children[0]
      const m = first.type === 'text' && first.content.match(/^@(you|claude|admin)\s+/)
      if (!m) continue
      const label = { you: 'You', claude: 'Claude', admin: 'Admin' }[m[1]]
      const badge = new state.Token('html_inline', '', 0)
      badge.content = `<span class="who ${m[1]}">${label}</span>`
      first.content = first.content.slice(m[0].length)
      t.children.unshift(badge)
    }
  })
}

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
      href: 'https://fonts.googleapis.com/css2?family=IBM+Plex+Mono:wght@400;500;600&family=IBM+Plex+Sans:ital,wght@0,400;0,500;0,600;0,700;1,400&family=Bricolage+Grotesque:opsz,wght@12..96,700;12..96,800&display=swap'
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
    theme: 'github-dark',
    config: tutorialMarkdown
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
    ],
    optimizeDeps: { exclude: ['sql.js'] }
  }
})
