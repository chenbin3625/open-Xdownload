import { randomBytes, createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import assert from 'node:assert/strict';

// Disposable volumes and credentials; this script never reads a personal X session.
const image = process.argv[2] || 'x-archive:local';
const architectures = process.argv.slice(3).length ? process.argv.slice(3) : ['arm64', 'amd64'];
const results = [];
for (const [index, architecture] of architectures.entries()) {
  const name = `x-archive-verify-${architecture}-${Date.now()}`;
  const volumes = [`${name}-data`, `${name}-config`];
  const password = randomBytes(24).toString('hex');
  const port = 3102 + index;
  const base = `http://127.0.0.1:${port}`;
  const docker = (args) => execFileSync('docker', args, { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] }).trim();
  let cookie = '';
  async function api(path, method = 'GET', body) {
    const response = await fetch(`${base}/api${path}`, { method, headers: { Cookie: cookie, 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) });
    if (path === '/session') cookie = response.headers.get('set-cookie')?.split(';')[0] || '';
    const json = await response.json();
    assert.equal(response.ok, true, `${path}: ${JSON.stringify(json).replaceAll(password, '[redacted]')}`);
    return json;
  }
  async function healthy() {
    const deadline = Date.now() + 45000;
    while (Date.now() < deadline) {
      try { if ((await fetch(`${base}/api/health`)).ok) return; } catch {}
      await new Promise(resolve => setTimeout(resolve, 500));
    }
    throw new Error(`${architecture} container did not become healthy`);
  }
  try {
    volumes.forEach(volume => docker(['volume', 'create', volume]));
    docker(['run', '-d', '--platform', `linux/${architecture}`, '--name', name, '-p', `127.0.0.1:${port}:3000`, '-e', 'GALLERY_DL_BIN=/bin/false', '-v', `${volumes[0]}:/archive/media`, '-v', `${volumes[1]}:/archive/settings`, image, 'x-archive', '--bind', '0.0.0.0:3000', '--password', password, '--data-dir', '/archive/media', '--config-dir=/archive/settings']);
    await healthy();
    const html = await (await fetch(base)).text();
    assert.match(html, /<div id="root">/);
    const bundle = html.match(/src="([^"]+\.js)"/)[1];
    assert.equal((await fetch(`${base}${bundle}`)).ok, true);
    assert.equal((await fetch(`${base}/api/settings`)).status, 401);
    await api('/session', 'POST', { password });
    const { settings } = await api('/settings');
    settings.download_concurrency = 2;
    settings.image_path = 'persisted-images';
    await api('/settings', 'PUT', settings);
    const savedCookie = JSON.stringify(await api('/cookies', 'POST', { cookie: 'auth_token=OFFLINE_FIXTURE; ct0=OFFLINE_CSRF' }));
    assert.equal(savedCookie.includes('OFFLINE_FIXTURE'), false);
    assert.equal(savedCookie.includes('OFFLINE_CSRF'), false);
    const seed = `import sqlite3,pathlib,base64,os,json
p=pathlib.Path('/archive/media/persisted-images');p.mkdir(exist_ok=True)
p.joinpath('probe.png').write_bytes(base64.b64decode('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aD1sAAAAASUVORK5CYII='))
c=sqlite3.connect('/archive/media/archive.sqlite')
c.execute("INSERT INTO users(id,username) VALUES('persist-user','persist_user')")
c.execute("INSERT INTO tweets(id,user_id,author,published_at,url) VALUES('123','persist-user','persist_user','2026-11-10T00:00:00Z','https://x.com/persist_user/status/123')")
c.execute("INSERT INTO media(id,tweet_id,ordinal,kind,extension,remote_url,status,file_path,size) VALUES('persist-media','123',1,'image','png','https://pbs.twimg.com/media/fixture.png','ready','persisted-images/probe.png',68)")
c.commit()
assert os.stat('/archive/settings/cookies.txt').st_mode & 0o777 == 0o600
assert os.stat('/archive/settings/settings.json').st_mode & 0o777 == 0o600
uids=[]
for proc in pathlib.Path('/proc').glob('[0-9]*'):
 try:
  if proc.joinpath('comm').read_text().strip()=='x-archive':
   uids.append([x for x in proc.joinpath('status').read_text().splitlines() if x.startswith('Uid:')][0].split()[1])
 except OSError: pass
assert '10001' in uids
print('permissions and non-root service verified')`;
    docker(['exec', name, 'python3', '-c', seed]);
    const mediaBefore = await api('/media');
    assert.equal(mediaBefore.total, 1);
    const first = await (await fetch(`${base}/api/media/persist-media/file`, { headers: { Cookie: cookie } })).arrayBuffer();
    const hash = createHash('sha256').update(Buffer.from(first)).digest('hex');
    const oldCookie = cookie;
    docker(['restart', '--time', '10', name]);
    await healthy();
    assert.equal((await fetch(`${base}/api/settings`, { headers: { Cookie: oldCookie } })).status, 401);
    await api('/session', 'POST', { password });
    const persisted = await api('/settings');
    assert.equal(persisted.settings.download_concurrency, 2);
    assert.equal(persisted.settings.image_path, 'persisted-images');
    assert.notEqual(persisted.cookie.status, 'missing');
    assert.equal((await api('/users'))[0].username, 'persist_user');
    assert.equal((await api('/media')).total, 1);
    const second = await (await fetch(`${base}/api/media/persist-media/file`, { headers: { Cookie: cookie } })).arrayBuffer();
    assert.equal(createHash('sha256').update(Buffer.from(second)).digest('hex'), hash);
    const versions = docker(['exec', name, 'sh', '-c', 'uname -m; gallery-dl --version; yt-dlp --version; ffprobe -version | head -1']);
    const logs = docker(['logs', name]);
    assert.equal(logs.includes('OFFLINE_FIXTURE'), false);
    assert.equal(logs.includes(password), false);
    results.push({ architecture, persistence: 'passed', auth: 'passed', permissions: 'passed', versions: versions.split('\n') });
  } finally {
    try { docker(['rm', '-f', name]); } catch {}
    volumes.forEach(volume => { try { docker(['volume', 'rm', volume]); } catch {} });
  }
}
console.log(JSON.stringify(results, null, 2));
