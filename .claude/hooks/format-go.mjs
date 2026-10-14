// PostToolUse: gofmt the .go file Claude just edited, so the gofmt check in CI stays green.
import { readFileSync } from 'node:fs';
import { spawnSync } from 'node:child_process';

const input = JSON.parse(readFileSync(0, 'utf8') || '{}');
const file = input.tool_input?.file_path ?? '';
if (file.endsWith('.go')) {
	spawnSync('gofmt', ['-w', file], { stdio: 'ignore', timeout: 20000 });
}
