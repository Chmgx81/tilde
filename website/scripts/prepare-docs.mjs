// Copies developer docs from ../docs/ into src/content/docs/docs/,
// adding Starlight frontmatter and rewriting relative .md links.
//
// - Destination filenames are lowercased so slugs match the sidebar
//   (e.g. ARCHITECTURE.md -> docs/architecture, Plan.md -> docs/plan).
// - Internal .md links are rewritten to their site URL (/docs/<slug>/).
// - Links that escape ../docs/ (e.g. ../README.md) are rewritten to
//   their GitHub URL, since that file is not part of the site.
import { mkdir, readdir, readFile, rm, writeFile } from 'node:fs/promises';
import { dirname, join, relative, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const REPO_URL = 'https://github.com/Chmgx81/tilde/blob/main';

const scriptsDir = dirname(fileURLToPath(import.meta.url));
const websiteDir = resolve(scriptsDir, '..');
const repoRoot = resolve(websiteDir, '..');
const sourceDir = resolve(repoRoot, 'docs');
const destDir = resolve(websiteDir, 'src/content/docs/docs');

await rm(destDir, { recursive: true, force: true });
await mkdir(destDir, { recursive: true });
await copyDir(sourceDir, destDir);

async function copyDir(source, destination) {
  await mkdir(destination, { recursive: true });
  for (const entry of await readdir(source, { withFileTypes: true })) {
    const sourcePath = join(source, entry.name);
    if (entry.isDirectory()) {
      await copyDir(sourcePath, join(destination, entry.name.toLowerCase()));
      continue;
    }
    if (!entry.name.endsWith('.md')) continue;
    const destPath = join(destination, entry.name.toLowerCase());
    const raw = await readFile(sourcePath, 'utf8');
    const relPath = relative(sourceDir, sourcePath);
    await writeFile(destPath, transform(raw, relPath), 'utf8');
  }
}

function transform(raw, relPath) {
  const body = raw;
  const titleMatch = body.match(/^#\s+(.+)$/m);
  const title = titleMatch ? titleMatch[1].trim() : 'Untitled';
  const withoutTitle = titleMatch ? body.replace(titleMatch[0], '').trimStart() : body;
  const descMatch = withoutTitle.match(/^([^\\n#].+)$/m);
  const description = (descMatch ? descMatch[1] : title).replace(/`/g, '').slice(0, 140);
  const rewritten = rewriteLinks(withoutTitle, relPath);
  const frontmatter = [
    '---',
    `title: ${yamlString(title)}`,
    `description: ${yamlString(description)}`,
    'editUrl: false',
    '---',
    '',
  ].join('\n');
  return frontmatter + rewritten;
}

function rewriteLinks(content, relPath) {
  const currentDir = dirname(relPath);
  return content.replace(/\]\(([^)]+?\.md)(#[^)]*)?\)/g, (_match, link, anchor = '') => {
    const targetRel = join(currentDir, link);
    if (targetRel.startsWith('..')) {
      // Target lives outside ../docs/ (e.g. repo-root README.md) and is
      // not copied to the site — link to it on GitHub instead.
      const repoPath = relative(repoRoot, resolve(sourceDir, targetRel));
      return `](${REPO_URL}/${repoPath}${anchor})`;
    }
    const slug = toSlug(targetRel).toLowerCase();
    return `](/docs/${slug}/${anchor})`;
  });
}

function toSlug(relPath) {
  const noExt = relPath.replace(/\.md$/, '');
  return noExt === 'index' || noExt.endsWith('/index') ? noExt.replace(/\/?index$/, '') : noExt;
}

function yamlString(value) {
  return JSON.stringify(value);
}
