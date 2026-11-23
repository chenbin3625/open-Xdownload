const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

const webDir = path.resolve(__dirname, '..');
const lock = JSON.parse(fs.readFileSync(path.join(webDir, 'package-lock.json'), 'utf8'));
const packages = Object.keys(lock.packages).filter(name => /(?:^|\/)node_modules\/picomatch$/.test(name));
assert.ok(packages.length, 'No locked picomatch installation found');
const script = `
  const assert = require('node:assert/strict');
  const pico = require(process.argv[1]);
  const riskyCases = [
    ['+(*)b', 'a'.repeat(64) + '!'],
    ['+(*a)b', 'a'.repeat(64) + '!'],
    ['*(*)b', 'a'.repeat(64) + '!'],
    ['*(*a)b', 'a'.repeat(64) + '!'],
    ['+(a*)b', 'a'.repeat(64) + '!'],
    ['+(@(*a))b', 'a'.repeat(64) + '!'],
    ['+(@(+(a)))b', 'a'.repeat(64) + '!'],
    ['+([[]*a)b', '[a'.repeat(32) + '!'],
    ['+([a*a)b', '[aa'.repeat(32) + '!'],
    ['+("a*a)b', '"aa'.repeat(32) + '!'],
    ['+("a*a)b', 'a'.repeat(64) + '!'],
    ["+('a*a)b", "'aa".repeat(32) + '!'],
    ['+(a"*a")b', 'a"a"'.repeat(32) + '!'],
    ['+(a"b*a")b', 'a"ba"'.repeat(32) + '!'],
    ['+(.a"*a")b', '.a"a"'.repeat(32) + '!'],
    ['+(a+)b', 'a'.repeat(64) + '!'],
    ['+([a]+)b', 'a'.repeat(64) + '!'],
  ];
  for (const [pattern, input] of riskyCases) {
    assert.equal(pico(pattern)(input), false, pattern);
  }
  for (const [pattern, value] of [
    ['+(abc)', 'abcabc'], ['+(a|b)', 'abba'], ['+([ab])', 'abba'],
    ['+(\\\\*)', '*'], ['+("*")', '*'], ['+([*])', '***'],
    ['+(."*")b', '.*.*b'], ['+([a-z]"*a")b', 'a*ab'],
    ['+("+")', '++'], ['+([+])', '++'], ['+(\\\\+)', '++'],
    ['+(*(a))', 'aaaa'], ['src/**/*.ts', 'src/index.ts']
  ]) assert.equal(pico(pattern)(value), true, pattern);
`;
for (const name of packages) {
  const result = spawnSync(process.execPath, ['-e', script, path.resolve(webDir, name)], { timeout: 2000, encoding: 'utf8' });
  assert.equal(result.status, 0, `${name}: ${result.error?.message ?? result.stderr}`);
}
console.log(`Picomatch security and compatibility checks passed (${packages.length} installations)`);
