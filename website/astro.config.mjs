// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';

export default defineConfig({
  site: 'https://tilde.sh',
  integrations: [
    starlight({
      title: 'tilde',
      description: 'A security-first terminal coding agent built for real engineering.',
      logo: { src: './src/assets/tilde-mark-white.png', alt: 'tilde — the tilde mark' },
      favicon: '/favicon.ico',
      social: [{ icon: 'github', label: 'GitHub', href: 'https://github.com/Chmgx81/tilde' }],
      editLink: {
        baseUrl: 'https://github.com/Chmgx81/tilde/edit/main/website/',
      },
      customCss: ['./src/styles/fonts.css', './src/styles/custom.css'],
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
