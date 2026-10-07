// Markdown rules shared with the server's Help (internal/help), so a guide
// reads the same on linxpbx.com as it does inside Linx:
//  - ![alt](screen:name) is that screen's picture, light or dark to match
//    the reader's appearance (docs/help/pictures/{light,dark}-name.webp);
//  - a heading's link name follows help.Anchor ("2. Open the link" ->
//    "2-open-the-link"), so links like install-linx#2-open-the-link work.

const anchor = (s) => s.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '');
const text = (n) => (n.value ?? '') + (n.children ?? []).map(text).join('');
const attr = (s) => s.replace(/&/g, '&amp;').replace(/"/g, '&quot;').replace(/</g, '&lt;');

function walk(node) {
  if (node.type === 'heading') {
    node.data = node.data ?? {};
    node.data.hProperties = { ...(node.data.hProperties ?? {}), id: anchor(text(node)) };
  }
  if (!node.children) return;
  node.children = node.children.map((c) => {
    if (c.type === 'image' && c.url.startsWith('screen:')) {
      const name = c.url.slice('screen:'.length);
      const alt = attr(c.alt ?? '');
      return {
        type: 'html',
        value:
          `<picture class="help-picture">` +
          `<source media="(prefers-color-scheme: dark)" srcset="/help/pictures/dark-${name}.webp">` +
          `<img src="/help/pictures/light-${name}.webp" alt="${alt}" loading="lazy" decoding="async">` +
          `</picture>`,
      };
    }
    walk(c);
    return c;
  });
}

export default function remarkHelp() {
  return (tree) => walk(tree);
}
