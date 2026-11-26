import assert from 'node:assert/strict';
import test from 'node:test';
import { publishDraft, requireImmutability } from './release-draft.mjs';

const repo = 'example/test';
const tag = 'v1.2.3';
const files = ['android.apk', 'setup.exe', 'portable.zip', 'latest.json'].map((name, i) => ({ name, size: 100 + i, digest: `sha256:${String(i).repeat(64)}` }));
const asset = file => ({ ...file, state: 'uploaded' });
const endpoint = `repos/${repo}/releases`;
function fixture() {
  const release = { id: 7, tag_name: tag, draft: true, immutable: false, body: '<!-- Meerkat immutable release run 5 -->', assets: [] };
  const writes = [];
  const server = { release, writes, beforeUpload() {}, afterPublish() {}, exists: true };
  function api(...args) {
    if (args[0] === '--paginate') return [server.exists ? [structuredClone(release)] : []];
    if (args[0] === endpoint) {
      assert.ok(!server.exists && args.includes('draft=true') && args.includes(`body=${release.body}`));
      server.exists = true;
      writes.push('create');
    } else if (args[0].startsWith('https://uploads.github.com/')) {
      const name = new URL(args[0]).searchParams.get('name');
      const file = files.find(f => f.name === name);
      writes.push(`upload ${name}`);
      server.beforeUpload(file);
      if (release.immutable) throw new Error('Immutable release rejects upload');
      if (release.assets.some(a => a.name === name)) throw new Error('422 already_exists');
      release.assets.push(asset(file));
      return release.assets.at(-1);
    } else if (args[0] === '-X') {
      assert.deepEqual(args, ['-X', 'PATCH', `${endpoint}/7`, '-F', 'draft=false']);
      writes.push('publish');
      release.draft = false;
      release.immutable = true;
      server.afterPublish();
    } else {
      assert.ok(args[0] === `${endpoint}/7` || args[0] === `${endpoint}/latest`);
    }
    return structuredClone(release);
  }
  server.run = () => publishDraft({ api, repo, tag, runId: '5', files, path: name => name });
  return server;
}

test('policy requires an explicit boolean true and a successful read', () => {
  for (const value of [undefined, {}, { enabled: false }, { enabled: 'true' }]) {
    assert.throws(() => requireImmutability(() => value), /must be enabled/);
  }
  assert.throws(() => requireImmutability(() => { throw Error('403'); }), /Cannot verify/);
  requireImmutability(() => ({ enabled: true }));
});

test('interrupted new draft resumes from identical assets only', () => {
  const s = fixture();
  s.exists = false;
  s.beforeUpload = f => { if (f === files[2]) throw Error('interrupted'); };
  assert.throws(s.run, /interrupted/);
  assert.deepEqual(s.release.assets, files.slice(0, 2).map(asset));
  s.beforeUpload = () => {};
  s.writes.length = 0;
  s.run();
  assert.deepEqual(s.writes, ['upload portable.zip', 'upload latest.json', 'publish']);
  assert.deepEqual(s.release.assets, files.map(asset));
});

for (const [field, value] of [['digest', 'wrong'], ['size', 0], ['state', 'starter'], ['name', 'unknown']]) {
  test(`unexpected stored ${field} fails before any write`, () => {
    const s = fixture();
    s.release.assets = [{ ...asset(files[3]), [field]: value }];
    assert.throws(s.run);
    assert.deepEqual(s.writes, []);
  });
}

test('already published release and legacy draft are rejected before writes', () => {
  for (const change of [{ draft: false }, { body: '<!-- Meerkat Actions run 5 -->' }]) {
    const s = fixture();
    Object.assign(s.release, change);
    assert.throws(s.run, /Expected this run/);
    assert.deepEqual(s.writes, []);
  }
});

test('publication between draft read and upload freezes the existing assets', () => {
  const s = fixture();
  s.release.assets.push(asset(files[0]));
  s.beforeUpload = () => { Object.assign(s.release, { draft: false, immutable: true }); };
  assert.throws(s.run, /Expected this run/);
  assert.deepEqual(s.writes, ['upload setup.exe']);
  assert.deepEqual(s.release.assets, [asset(files[0])]);
});

for (const identical of [true, false]) {
  test(`concurrent same-name upload, matching=${identical}`, () => {
    const s = fixture();
    s.beforeUpload = file => {
      if (file === files[0]) s.release.assets.push({ ...asset(file), size: identical ? file.size : 0 });
    };
    if (identical) {
      s.run();
      assert.deepEqual(s.release.assets, files.map(asset));
    } else {
      assert.throws(s.run, /Size mismatch/);
      assert.deepEqual(s.writes, ['upload android.apk']);
    }
  });
}

test('final read rejects mutable publication or a changed asset set', () => {
  for (const mutate of [r => { r.immutable = false; }, r => r.assets.pop()]) {
    const s = fixture();
    s.afterPublish = () => mutate(s.release);
    assert.throws(s.run, /Expected an immutable|missing assets/);
  }
});
