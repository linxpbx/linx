import { defineCollection, z } from 'astro:content';
import { glob } from 'astro/loaders';

// Two kinds of page under /docs:
//  - help: the server's own help guides, read straight from docs/help. One
//    file serves both the Help screen inside Linx and this website, so they
//    never drift apart; editing a guide and pushing updates both.
//  - docs: a few website-only pages (getting started, requirements).
export const collections = {
  help: defineCollection({
    loader: glob({ pattern: '*.md', base: './.help-guides' }), // from docs/help by scripts/sync-help.mjs
    schema: z.object({
      title: z.string(),
      audience: z.enum(['public', 'everyone', 'admin', 'system_admin']),
      section: z.enum(['whats-new', 'install', 'everyday', 'admin', 'running']),
      keywords: z.array(z.coerce.string()).default([]), // "999" reads as a number in YAML
      screens: z.array(z.coerce.string()).default([]),
    }),
  }),
  // One file per release, written by tools/changelog/entry.sh when the
  // Release workflow publishes it (ADR-084).
  changelog: defineCollection({
    loader: glob({ pattern: '*.md', base: './src/content/changelog' }),
    schema: z.object({
      version: z.string(),
      date: z.string(),
      channel: z.enum(['stable', 'beta']),
      previous: z.string().default(''),
    }),
  }),
  docs: defineCollection({
    loader: glob({ pattern: '**/*.md', base: './src/content/docs' }),
    schema: z.object({
      title: z.string(),
      description: z.string().optional(),
      order: z.number().default(100),
    }),
  }),
};
