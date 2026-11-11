/**
 * Lint for the backend and frontend sources. Two checks:
 *  1. syntax: `node --check` on every .mjs/.cjs/.js script;
 *  2. docs: a JSDoc description must add information. It fails when every significant word of the
 *     description already appears in the name of the thing it documents (for example "Reads one item."
 *     on `readItem`). Say what the name cannot: consistency, cost, units, error behaviour, why.
 *     There is no escape hatch; fix the comment instead.
 * Usage: node scripts/lint.mjs      (part of `npm run lint`, which also checks formatting and docs)
 */
import fs from 'node:fs';
import path from 'node:path';
import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const ROOT_DIR = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const CHECKED_PATHS = ['lambda', 'lib', 'bin', 'dev', 'scripts', 'site', 'test'];
const SYNTAX_EXTENSIONS = new Set(['.mjs', '.cjs', '.js']);
const DOC_EXTENSIONS = new Set(['.mjs', '.cjs', '.js', '.ts']);
const SKIPPED_DIRS = new Set(['node_modules', 'cdk.out', 'out', 'maps', 'ads']);
/** Words that carry no information on their own. */
const FILLER_WORDS = new Set(
  'a an the of to for and or in on is are as at by it its this that with from one be if when into per'.split(' '),
);
const DOC_BLOCK_PATTERN = /\/\*\*([\s\S]*?)\*\/[ \t]*\r?\n[ \t]*([^\r\n]*)/g;
const KEYWORD_MODIFIERS =
  'export|default|async|function\\*?|class|const|let|var|private|public|static|readonly|get|set';
const DECLARATION_MODIFIERS = new RegExp(`^(?:(?:${KEYWORD_MODIFIERS})\\s+)+`);

/** @returns {string[]} Absolute paths of the files under a directory whose extension is in `extensions`. */
function listFiles(directory, extensions) {
  if (!fs.existsSync(directory)) return [];
  return fs.readdirSync(directory, { withFileTypes: true }).flatMap(entry => {
    const full = path.join(directory, entry.name);
    if (entry.isDirectory()) return SKIPPED_DIRS.has(entry.name) ? [] : listFiles(full, extensions);
    return extensions.has(path.extname(entry.name)) ? [full] : [];
  });
}

/** @returns {string[]} Lower-case words of a camelCase, snake_case or prose string, plural "s" removed. */
function toWords(text) {
  const spaced = text.replace(/([a-z0-9])([A-Z])/g, '$1 $2').replace(/[^A-Za-z0-9]+/g, ' ');
  return spaced
    .toLowerCase()
    .split(' ')
    .filter(Boolean)
    .map(word => word.replace(/s$/, ''));
}

/** @returns {string} The description of a JSDoc block: its text before the first @tag, without asterisks. */
function descriptionOf(blockBody) {
  const lines = blockBody.split('\n').map(line => line.replace(/^\s*\*?\s?/, ''));
  const tagIndex = lines.findIndex(line => line.startsWith('@'));
  return (tagIndex < 0 ? lines : lines.slice(0, tagIndex)).join(' ').trim();
}

/** @returns {string[]} Descriptions-vs-names violations of one source text. */
export function findRestatingDocs(source) {
  const problems = [];
  for (const [, body, declaration] of source.matchAll(DOC_BLOCK_PATTERN)) {
    const name = declaration
      .trim()
      .replace(DECLARATION_MODIFIERS, '')
      .match(/^[A-Za-z_$][\w$]*/)?.[0];
    const description = descriptionOf(body);
    if (!name || !description) continue;
    const nameWords = new Set(toWords(name));
    const significant = toWords(description).filter(word => !FILLER_WORDS.has(word));
    if (significant.length && significant.every(word => nameWords.has(word))) {
      problems.push(`"${description}" only restates ${name}`);
    }
  }
  return problems;
}

let failedCount = 0;
const fail = (file, detail) => {
  failedCount++;
  console.log(`${path.relative(ROOT_DIR, file)}: ${detail}`);
};

const roots = CHECKED_PATHS.map(entry => path.join(ROOT_DIR, entry));
for (const script of roots.flatMap(root => listFiles(root, SYNTAX_EXTENSIONS))) {
  try {
    execFileSync(process.execPath, ['--check', script], { stdio: 'pipe' });
  } catch (error) {
    fail(script, String(error.stderr).trim());
  }
}
for (const source of roots.flatMap(root => listFiles(root, DOC_EXTENSIONS))) {
  for (const problem of findRestatingDocs(fs.readFileSync(source, 'utf8'))) fail(source, problem);
}
console.log(failedCount ? `${failedCount} problem(s)` : 'lint OK');
process.exitCode = failedCount ? 1 : 0;
