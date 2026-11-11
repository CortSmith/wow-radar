/**
 * Documentation check: every file name cited in backticks in the docs must exist somewhere in the repo.
 * Usage: node scripts/check-docs.mjs      (exit code 1 lists the missing names)
 */
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const ROOT_DIR = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const DOCS = ['README.md', 'NAMING.md', 'PLAN.md', 'lambda/ARCHITECTURE.md'];
const SKIPPED_DIRS = new Set(['node_modules', 'cdk.out', '.git', 'out']);
/** Generated at runtime (git-ignored), so it legitimately may not exist. */
const GENERATED_FILES = new Set(['dev/.data.json']);
const CITED_FILE_PATTERN = /`([\w./-]+\.(?:mjs|cjs|js|ts|json|md|bat))`/g;

/** @returns {string[]} Repo-relative paths (forward slashes) of every file. */
function listFiles(directory) {
  return fs.readdirSync(directory, { withFileTypes: true }).flatMap(entry => {
    if (SKIPPED_DIRS.has(entry.name)) return [];
    const full = path.join(directory, entry.name);
    return entry.isDirectory() ? listFiles(full) : [path.relative(ROOT_DIR, full).split(path.sep).join('/')];
  });
}

const files = listFiles(ROOT_DIR);
const baseNames = new Set(files.map(file => path.posix.basename(file)));
const relativePaths = new Set(files);
let missingCount = 0;

for (const doc of DOCS) {
  const text = fs.readFileSync(path.join(ROOT_DIR, doc), 'utf8');
  for (const [, cited] of text.matchAll(CITED_FILE_PATTERN)) {
    if (
      GENERATED_FILES.has(cited) ||
      cited.startsWith('.') ||
      relativePaths.has(cited) ||
      baseNames.has(path.posix.basename(cited))
    )
      continue;
    console.log(`${doc}: missing ${cited}`);
    missingCount++;
  }
}
console.log(missingCount ? `${missingCount} missing` : 'all cited files exist');
process.exitCode = missingCount ? 1 : 0;
