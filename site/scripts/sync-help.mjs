// Brings the server's help guides (docs/help) into the website's build:
//  - each guide into .help-guides/, its front matter rewritten as strict YAML.
//    The server reads front matter with its own simple rules (internal/help:
//    "key: value", lists as [a, b, c]), where keywords like *43 or 999 are
//    plain words; to a YAML parser they're an alias and a number. Rewriting
//    every value as a quoted string keeps them what the server means.
//  - the pictures (made by `make screens`) into public/help/pictures, failing
//    the build if a guide asks for one that isn't there in light and dark.
import { cpSync, existsSync, mkdirSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

const guides = new URL('../../docs/help/', import.meta.url).pathname;
const from = join(guides, 'pictures');
const to = new URL('../public/help/pictures/', import.meta.url).pathname;

const out = new URL('../.help-guides/', import.meta.url).pathname;
rmSync(out, { recursive: true, force: true });
mkdirSync(out, { recursive: true });
for (const f of readdirSync(guides)) {
  if (!f.endsWith('.md')) continue;
  const text = readFileSync(join(guides, f), 'utf8');
  const m = text.match(/^---\n([\s\S]*?)\n---\n/);
  if (!m) throw new Error(`${f}: no front matter`);
  const yaml = m[1].split('\n').map((line) => {
    const i = line.indexOf(':');
    if (i < 0) return line;
    const key = line.slice(0, i).trim();
    const value = line.slice(i + 1).trim();
    if (value.startsWith('[') && value.endsWith(']')) {
      const items = value.slice(1, -1).split(',').map((v) => v.trim()).filter(Boolean);
      return `${key}: ${JSON.stringify(items)}`;
    }
    return `${key}: ${JSON.stringify(value)}`;
  });
  writeFileSync(join(out, f), `---\n${yaml.join('\n')}\n---\n` + text.slice(m[0].length));
}

rmSync(to, { recursive: true, force: true });
mkdirSync(to, { recursive: true });
for (const f of readdirSync(from)) if (f.endsWith('.webp')) cpSync(join(from, f), join(to, f));

const missing = [];
for (const f of readdirSync(guides)) {
  if (!f.endsWith('.md')) continue;
  for (const m of readFileSync(join(guides, f), 'utf8').matchAll(/\]\(screen:([a-z0-9-]+)\)/g)) {
    for (const mode of ['light', 'dark']) {
      if (!existsSync(join(from, `${mode}-${m[1]}.webp`))) missing.push(`${f}: ${mode}-${m[1]}.webp`);
    }
  }
}
if (missing.length) {
  console.error('Help pictures missing:\n  ' + missing.join('\n  '));
  process.exit(1);
}
console.log(`help: ${readdirSync(out).length} guides, ${readdirSync(to).length} pictures`);
