import { defineCollection, z } from 'astro:content';
import { glob } from 'astro/loaders';

// Support/help and documentation pages: one Markdown file each under
// src/content/docs. Adding or editing a file here (and pushing) is all it
// takes to change the live site — this is the content Claude Code keeps up to
// date. `order` sorts a section; `section` groups pages in the sidebar.
export const collections = {
  docs: defineCollection({
    loader: glob({ pattern: '**/*.md', base: './src/content/docs' }),
    schema: z.object({
      title: z.string(),
      description: z.string().optional(),
      section: z.string().default('Guides'),
      order: z.number().default(100),
    }),
  }),
};
