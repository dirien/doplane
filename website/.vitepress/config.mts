import { defineConfig } from 'vitepress'

export default defineConfig({
  title: 'doplane',
  description: 'Manage cloud resources as Kubernetes objects with Pulumi do.',
  base: '/doplane/',
  cleanUrls: true,
  lastUpdated: true,
  sitemap: { hostname: 'https://dirien.github.io/doplane/' },
  head: [
    ['link', { rel: 'icon', href: '/doplane/logo.svg', type: 'image/svg+xml' }],
    ['meta', { name: 'theme-color', content: '#6d5dfc' }],
    ['meta', { property: 'og:type', content: 'website' }],
    ['meta', { property: 'og:title', content: 'doplane' }],
    ['meta', {
      property: 'og:description',
      content: 'Cloud resources as Kubernetes objects. Pulumi under the hood — the whole Pulumi ecosystem on any cluster.'
    }]
  ],
  markdown: { lineNumbers: true },
  themeConfig: {
    logo: '/logo.svg',
    siteTitle: 'doplane',
    nav: [
      { text: 'Getting started', link: '/guide/getting-started' },
      { text: 'Concepts', link: '/concepts/control-loop' },
      { text: 'Operations', link: '/operations/day-2' },
      { text: 'Agent guide', link: '/reference/agent-context' }
    ],
    sidebar: [
      {
        text: 'Start here',
        items: [
          { text: 'Getting started', link: '/guide/getting-started' },
          { text: 'Discover providers and components', link: '/guide/discover' },
          { text: 'Choose an API', link: '/guide/choose-an-api' }
        ]
      },
      {
        text: 'Concepts',
        items: [
          { text: 'Control loop and state', link: '/concepts/control-loop' },
          { text: 'Dependencies and composites', link: '/concepts/dependencies' }
        ]
      },
      {
        text: 'Operations',
        items: [
          { text: 'Day-2 operations', link: '/operations/day-2' },
          { text: 'Security and tenancy', link: '/operations/security' }
        ]
      },
      {
        text: 'For agents',
        items: [
          { text: 'Repository context', link: '/reference/agent-context' },
          { text: 'Conditions and failures', link: '/reference/conditions' }
        ]
      },
      {
        text: 'Contributing',
        items: [
          { text: 'Build from source', link: '/guide/build-from-source' }
        ]
      }
    ],
    outline: { level: [2, 3] },
    search: { provider: 'local' },
    editLink: {
      pattern: 'https://github.com/dirien/doplane/edit/main/website/:path',
      text: 'Edit this page on GitHub'
    },
    socialLinks: [
      { icon: 'github', link: 'https://github.com/dirien/doplane' }
    ],
    footer: {
      message: 'Released under the Apache-2.0 License.',
      copyright: 'doplane is an experimental Kubernetes operator.'
    }
  }
})
