// PreToolUse guard: Claude may not edit lock files or secrets by hand.
// Lock files change through their package manager; secrets are edited by a person.
import { readFileSync } from 'node:fs';
import { basename } from 'node:path';

const input = JSON.parse(readFileSync(0, 'utf8') || '{}');
const file = input.tool_input?.file_path ?? input.tool_input?.notebook_path ?? '';
const name = basename(file);

const LOCK_FILES = new Set(['Cargo.lock', 'package-lock.json', 'pnpm-lock.yaml', 'yarn.lock', 'bun.lock', 'bun.lockb', 'go.sum']);
const isSecret = (/^\.env(\..+)?$/.test(name) && !/\.(example|sample|template)$/.test(name))
	|| /\.(pem|key|p12|pfx)$/i.test(name);

if (LOCK_FILES.has(name)) {
	console.error(`${name} is a lock file. Change it with its package manager (cargo, npm, pnpm, go), not by hand.`);
	process.exit(2);
}
if (isSecret) {
	console.error(`${name} may hold secrets. Ask the user to edit it.`);
	process.exit(2);
}
