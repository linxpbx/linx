import { getCollection, type CollectionEntry } from 'astro:content';

// Every page under /docs, in sidebar order: the website's own pages first,
// then the server's help guides by section.

export type DocPage = {
  id: string;
  title: string;
  description: string;
  group: string;
  audience: string;
  entry: CollectionEntry<'help'> | CollectionEntry<'docs'>;
};

export const GROUPS = ['Start here', 'Installing', 'Everyday use', 'For admins', 'Running Linx', "What's new"];

const SECTION_GROUP: Record<string, string> = {
  install: 'Installing',
  everyday: 'Everyday use',
  admin: 'For admins',
  running: 'Running Linx',
  'whats-new': "What's new",
};

// Within a section guides are alphabetical, except these lead.
const FIRST = ['install-linx', 'setup-wizard', 'signing-in', 'admin-home'];

export const AUDIENCE_LABEL: Record<string, string> = {
  admin: 'For admins',
  system_admin: 'For the system admin',
};

// The first paragraph of a guide, as plain words, for cards and the page's
// description.
function summary(body = ''): string {
  const para = body
    .split(/\n\s*\n/)
    .map((p) => p.trim())
    .find((p) => p && !p.startsWith('#') && !p.startsWith('!['));
  if (!para) return '';
  const plain = para
    .replace(/!\[[^\]]*\]\([^)]*\)/g, '')
    .replace(/\[([^\]]+)\]\([^)]*\)/g, '$1')
    .replace(/[*_`>]/g, '')
    .replace(/\s+/g, ' ')
    .trim();
  return plain.length > 160 ? plain.slice(0, 157).replace(/\s+\S*$/, '') + '…' : plain;
}

export async function docPages(): Promise<DocPage[]> {
  const site = (await getCollection('docs')).sort((a, b) => a.data.order - b.data.order);
  const help = (await getCollection('help')).sort((a, b) => {
    const fa = FIRST.indexOf(a.id), fb = FIRST.indexOf(b.id);
    if (fa !== fb) return (fa < 0 ? 99 : fa) - (fb < 0 ? 99 : fb);
    return a.data.title.localeCompare(b.data.title);
  });
  const pages: DocPage[] = [
    ...site.map((e) => ({
      id: e.id, title: e.data.title, description: e.data.description ?? summary(e.body),
      group: 'Start here', audience: 'public', entry: e,
    })),
    ...help.map((e) => ({
      id: e.id, title: e.data.title, description: summary(e.body),
      group: SECTION_GROUP[e.data.section], audience: e.data.audience, entry: e,
    })),
  ];
  return pages.sort((a, b) => GROUPS.indexOf(a.group) - GROUPS.indexOf(b.group));
}
