import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { randomBytes } from 'node:crypto';
import { access, mkdir, mkdtemp, rm, writeFile } from 'node:fs/promises';
import { createServer } from 'node:net';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';

assert.ok(process.argv[2] && process.argv[3], 'Usage: node scripts/smoke-binary.mjs <binary> <version>');
const binary = resolve(process.argv[2]);
const version = process.argv[3];
const work = await mkdtemp(join(tmpdir(), 'x-archive-release-'));
const staging = join(work, 'data', '.staging');
await mkdir(join(staging, 'orphan'), { recursive: true });
await writeFile(join(staging, 'orphan', 'asset.part'), 'interrupted download');
const socket = createServer();
await new Promise((resolve, reject) => {
  socket.once('error', reject);
  socket.listen(0, '127.0.0.1', resolve);
});
const port = socket.address().port;
await new Promise(resolve => socket.close(resolve));
const base = `http://127.0.0.1:${port}`;
const password = randomBytes(24).toString('hex');
const child = spawn(binary, [
  '--bind', `127.0.0.1:${port}`,
  '--password', password,
  '--data-dir', join(work, 'data'),
  `--config-dir=${join(work, 'config')}`,
], {
  cwd: work,
  env: {
    ...process.env,
    X_ARCHIVE_BIND: 'invalid-env-bind',
    X_ARCHIVE_DATA: join(work, 'unused-data'),
    X_ARCHIVE_CONFIG: join(work, 'unused-config'),
    X_ARCHIVE_PASSWORD: 'unused-env-password',
  },
  stdio: ['ignore', 'pipe', 'pipe'],
});
let log = '';
let startupError;
for (const stream of [child.stdout, child.stderr]) {
  stream.on('data', chunk => { log = (log + chunk.toString()).slice(-16000); });
}
child.once('error', error => { startupError = error; });
const stopped = new Promise(resolve => {
  child.once('exit', resolve);
  child.once('error', resolve);
});
const get = path => fetch(`${base}${path}`, { signal: AbortSignal.timeout(5000) });
try {
  const deadline = Date.now() + 30000;
  let health;
  while (Date.now() < deadline) {
    if (startupError) throw startupError;
    assert.equal(child.exitCode, null, `Binary exited during startup: ${child.exitCode}`);
    try {
      const response = await get('/api/health');
      if (response.ok) { health = await response.json(); break; }
    } catch {}
    await delay(250);
  }
  assert.deepEqual(health, { status: 'ok', version });
  const page = await get('/');
  assert.equal(page.status, 200);
  assert.match(page.headers.get('content-type'), /text\/html/);
  const html = await page.text();
  assert.match(html, /id="root"/);
  const assets = [...html.matchAll(/(?:src|href)="(\/assets\/[^" ]+)"/g)].map(match => match[1]);
  assert.ok(assets.some(path => path.endsWith('.js')), 'Missing embedded frontend bundle');
  for (const path of assets) {
    const response = await get(path);
    assert.equal(response.status, 200, path);
    assert.ok((await response.arrayBuffer()).byteLength > 0, path);
  }
  assert.equal((await get('/api/dashboard')).status, 401);
  const login = await fetch(`${base}/api/session`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ password }),
    signal: AbortSignal.timeout(5000),
  });
  assert.equal(login.status, 200);
  const cookie = login.headers.get('set-cookie')?.split(';')[0];
  assert.ok(cookie);
  const dashboard = await fetch(`${base}/api/dashboard`, {
    headers: { Cookie: cookie },
    signal: AbortSignal.timeout(5000),
  });
  assert.equal(dashboard.status, 200);
  await access(join(work, 'data', 'archive.sqlite'));
  await access(join(work, 'config', 'settings.json'));
  await assert.rejects(access(join(work, 'unused-data')), { code: 'ENOENT' });
  await assert.rejects(access(join(work, 'unused-config')), { code: 'ENOENT' });
  await assert.rejects(access(staging), { code: 'ENOENT' });
  console.log(`Release ${version}: CLI overrides, custom directories, startup, embedded frontend, authentication, SQLite and staging recovery passed`);
} catch (error) {
  console.error(log.replaceAll(password, '[redacted]'));
  throw error;
} finally {
  if (child.exitCode === null && !startupError) {
    child.kill();
    const timeout = setTimeout(() => child.kill('SIGKILL'), 10000);
    try { await stopped; } finally { clearTimeout(timeout); }
  }
  await rm(work, { recursive: true, force: true, maxRetries: 5, retryDelay: 200 });
}
