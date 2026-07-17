import { defineConfig } from 'vitepress'

export default defineConfig({
  title: 'admin-project-tool',
  description: 'Manifest-driven, archetype-composable generator for per-project ./admin task runners.',
  cleanUrls: true,
  themeConfig: {
    nav: [
      { text: 'Guide', link: '/guide/' },
      { text: 'Architecture', link: '/architecture/design' },
      { text: 'Archetypes', link: '/archetypes/apple' },
      { text: 'Development', link: '/development/' },
    ],
    sidebar: {
      '/guide/': [
        {
          text: 'Guide',
          items: [
            { text: 'Overview', link: '/guide/' },
          ],
        },
      ],
      '/architecture/': [
        {
          text: 'Architecture',
          items: [
            { text: 'Design', link: '/architecture/design' },
          ],
        },
      ],
      '/archetypes/': [
        {
          text: 'Archetypes',
          items: [
            { text: 'apple ([apple] schema)', link: '/archetypes/apple' },
          ],
        },
      ],
      '/development/': [
        {
          text: 'Development',
          items: [
            { text: 'Overview', link: '/development/' },
            { text: 'Keybindings handoff', link: '/development/handoff-keybindings' },
          ],
        },
      ],
    },
    socialLinks: [
      { icon: 'github', link: 'https://github.com/McBrideMusings/admin-project-tool' },
    ],
    outline: { level: [2, 3] },
  },
})
