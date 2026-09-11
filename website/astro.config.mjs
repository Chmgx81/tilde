// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';

export default defineConfig({
  site: 'https://tilde.sh',
  integrations: [
    starlight({
      title: 'tilde',
      description: 'A security-first terminal coding agent built for real engineering.',
      logo: { src: './src/assets/banner_nav_logo.png', alt: 'tilde (~)' },
      favicon: '/images/banner_favicon_32x32.png',
      head: [
        { tag: 'link', attrs: { rel: 'icon', type: 'image/png', sizes: '32x32', href: '/images/banner_favicon_32x32.png' } },
        { tag: 'link', attrs: { rel: 'icon', type: 'image/png', sizes: '48x48', href: '/images/banner_favicon_48x48.png' } },
        { tag: 'link', attrs: { rel: 'apple-touch-icon', sizes: '48x48', href: '/images/banner_favicon_48x48.png' } },
        { tag: 'meta', attrs: { property: 'og:image', content: '/images/banner_hero_logo.png' } },
        { tag: 'meta', attrs: { name: 'twitter:image', content: '/images/banner_hero_logo.png' } },
      ],
      social: [{ icon: 'github', label: 'GitHub', href: 'https://github.com/Chmgx81/tilde' }],
      editLink: {
        baseUrl: 'https://github.com/Chmgx81/tilde/edit/main/website/',
      },
      customCss: ['./src/styles/fonts.css', './src/styles/custom.css'],
      components: {
        Header: './src/components/SiteHeader.astro',
      },
      pagination: false,
      sidebar: [
        {
          label: 'Get started',
          items: [
            { label: 'Overview', slug: 'docs/readme' },
            { label: 'Architecture', slug: 'docs/architecture' },
            { label: 'Design spec', slug: 'docs/tui-design-spec' },
          ],
        },
        {
          label: 'Reference',
          items: [
            { label: 'Plan', slug: 'docs/plan' },
            { label: 'Marketplace', slug: 'docs/marketplace' },
            { label: 'Sandbox image', slug: 'docs/sandbox-image' },
          ],
        },
        {
          label: 'Internals',
          items: [
            { label: 'Vision design', slug: 'docs/vision-design' },
            { label: 'TUI audit', slug: 'docs/tui-audit' },
            { label: 'AI coding agents', slug: 'docs/ai-agents-and-terminal-coding-agents-2026' },
          ],
        },
      ],
    }),
  ],
});
