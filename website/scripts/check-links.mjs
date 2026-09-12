// Fails on a relative markdown link in ../docs/ that points at a missing
// file. prepare-docs rewrites those to /docs/<slug>/, so a broken source
// link becomes a dead link on the site — catch it in CI, not after deploy.
import { readdir, readFile, stat } from 'node:fs/promises';
import { dirname, join, resolve, relative } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const docsDir = resolve(here, '..', '..', 'docs');

let broken = 0;
let checked = 0;

async function walk(dir) {
  for (const entry of await readdir(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) {
      await walk(path);
      continue;
    }
    if (!entry.name.endsWith('.md')) continue;
    const text = await readFile(path, 'utf8');
    let line = 0;
    for (const raw of text.split('\n')) {
      line++;
      const re = /\]\(([^)\s]+)(?:\s+"[^"]*")?\)/g;
      let m;
      while ((m = re.exec(raw)) !== null) {
        const link = m[1];
        if (/^(https?:|mailto:|#)/.test(link)) continue;
        const target = link.split('#')[0];
        if (!target) continue;
        checked++;
        const cleaned = decodeURIComponent(target.replace(/^<|>$/g, ''));
        const dest = resolve(dirname(path), cleaned);
        try {
          await stat(dest);
        } catch {
          broken++;
          console.error(`${relative(process.cwd(), path)}:${line}: missing ${link}`);
        }
      }
    }
  }
}

await walk(docsDir);
console.log(`check-links: ${checked} local link(s) checked, ${broken} missing`);
if (broken > 0) process.exit(1);
