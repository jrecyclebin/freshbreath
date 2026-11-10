// Build-time index of every page: episode order and blurbs for the sidebar and
// home page (episodes first, then the advanced ones that teach frbr.js code,
// then reference), plus each beat's title, anchor and text for the Jump search.
import { createContentLoader } from 'vitepress'

export interface BeatInfo { title: string, id: string, text: string }
export interface Page {
  url: string
  title: string
  episode?: number
  advanced?: number
  reference?: boolean
  blurb: string
  minutes?: number
  icon: string
  short: string
  beats: BeatInfo[]
}

declare const data: Page[]
export { data }

const strip = (html: string) => html
  .replace(/<[^>]+>/g, ' ')
  .replace(/&[a-z#0-9]+;/gi, ' ')
  .replace(/\s+/g, ' ')
  .trim()

const order = (p: Page) => p.episode ?? (p.advanced ? 1000 + p.advanced : 9999)

export default createContentLoader('*.md', {
  render: true,
  transform(raw): Page[] {
    return raw
      .filter((p) => p.frontmatter.episode || p.frontmatter.advanced || p.frontmatter.reference)
      .map((p) => {
        const fm = p.frontmatter
        const html = p.html || ''
        const parts = html.split(/(?=<h2 )/)
        const beats = parts.slice(1).map((part) => {
          const m = part.match(/^<h2 id="([^"]+)"[^>]*>([\s\S]*?)<\/h2>/)
          return {
            id: m?.[1] || '',
            title: strip(m?.[2] || '').replace(/^​|​$/g, '').trim(),
            text: strip(part.slice(m?.[0].length || 0)).slice(0, 600)
          }
        })
        return {
          url: p.url,
          title: fm.title,
          episode: fm.episode,
          advanced: fm.advanced,
          reference: !!fm.reference,
          blurb: fm.blurb || '',
          minutes: fm.minutes,
          icon: fm.icon || '📄',
          short: fm.short || '',
          beats
        }
      })
      .sort((a, b) => order(a) - order(b) || a.title.localeCompare(b.title))
  }
})
