const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

const lock = JSON.parse(fs.readFileSync('package-lock.json', 'utf8'));
const packages = Object.keys(lock.packages).filter(name => /(?:^|\/)node_modules\/picomatch$/.test(name));
assert.ok(packages.length, 'No locked picomatch installation found');
const script = `
  const assert = require('node:assert/strict');
  const pico = require(process.argv[1]);
  for (const pattern of ['+(*)b', '+(*a)b', '*(*)b', '*(*a)b', '+(a*)b', '+(@(*a))b', '+(@(+(a)))b']) {
    assert.equal(pico(pattern)('a'.repeat(64) + '!'), false, pattern);
  }
  for (const [pattern, value] of [
    ['+(abc)', 'abcabc'], ['+(a|b)', 'abba'], ['+([ab])', 'abba'],
    ['+(\\\\*)', '*'], ['+("*")', '*'], ['+([*])', '***'],
    ['+(*(a))', 'aaaa'], ['src/**/*.ts', 'src/index.ts']
  ]) assert.equal(pico(pattern)(value), true, pattern);
`;
for (const name of packages) {
  const result = spawnSync(process.execPath, ['-e', script, path.resolve(name)], { timeout: 2000, encoding: 'utf8' });
  assert.equal(result.status, 0, `${name}: ${result.error?.message ?? result.stderr}`);
}
console.log(`Picomatch security and compatibility checks passed (${packages.length} installations)`);
