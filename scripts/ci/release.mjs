import { publishDraft, requireImmutability } from './release-draft.mjs';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { createHash, createPublicKey, verify } from 'node:crypto';
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join } from 'node:path';

const [command, directory] = process.argv.slice(2);
const repo = 'teleJa/meerkat';
const version = readFileSync('VERSION', 'utf8').trim();
assert.match(version, /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$/);
const gradle = readFileSync('desktop/MeerkatDesktop/build/android/app/build.gradle', 'utf8');
assert.equal(gradle.match(/^\s*versionName "([^"]+)"\s*$/m)?.[1], version);
const code = Number(gradle.match(/^\s*versionCode (\d+)\s*$/m)?.[1]);
assert.ok(Number.isSafeInteger(code) && code > 0);
const tag = `v${version}`;
const installer = `Meerkat-Setup-${version}.exe`;
const apk = `Meerkat-Android-${version}.apk`;
const names = [installer, apk, `Meerkat-Windows-${tag}.zip`, 'latest.json'].sort();
const path = name => join(directory, name);
const digest = name => createHash('sha256').update(readFileSync(path(name))).digest('hex');

function tagEvent() {
  assert.equal(process.env.GITHUB_EVENT_NAME, 'push');
  assert.equal(process.env.GITHUB_REF, `refs/tags/${tag}`);
  assert.equal(process.env.GITHUB_REPOSITORY, repo);
  const event = JSON.parse(readFileSync(process.env.GITHUB_EVENT_PATH));
  assert.equal(event.deleted, false);
  assert.equal(event.forced, false);
}

function decodeManifest(text) {
  const envelope = JSON.parse(text);
  const bytes = Buffer.from(envelope.manifest, 'base64');
  const key = Buffer.from(readFileSync('scripts/release-public-key.txt', 'utf8').trim(), 'hex');
  const publicKey = createPublicKey({ format: 'jwk', key: { kty: 'OKP', crv: 'Ed25519', x: key.toString('base64url') } });
  assert.ok(verify(null, bytes, publicKey, Buffer.from(envelope.signature, 'base64')), 'Invalid manifest signature');
  return JSON.parse(bytes);
}

function verifyAssets() {
  assert.deepEqual(readdirSync(directory).sort(), names);
  const manifest = decodeManifest(readFileSync(path('latest.json')));
  assert.equal(manifest.version, version);
  for (const [platform, name] of [['windows-amd64', installer], ['android-arm64', apk]]) {
    const entry = manifest.platforms[platform];
    assert.equal(entry.url, `https://github.com/${repo}/releases/download/${tag}/${name}`);
    assert.equal(entry.sha256, digest(name));
  }
  const android = manifest.platforms['android-arm64'];
  assert.equal(android.size, statSync(path(apk)).size);
  assert.equal(android.versionCode, code);
  assert.equal(android.minSdk, Number(gradle.match(/^\s*minSdk (\d+)\s*$/m)[1]));
}

function publish() {
  tagEvent();
  const gh = (...args) => execFileSync('gh', args, { encoding: 'utf8' });
  const endpoint = `repos/${repo}/releases`;
  const api = (...args) => JSON.parse(gh('api', ...args));
  requireImmutability(() => {
    const token = process.env.IMMUTABILITY_READ_TOKEN;
    assert.ok(token, 'Configure IMMUTABILITY_READ_TOKEN with repository Administration: read');
    return JSON.parse(execFileSync('gh', ['api', `repos/${repo}/immutable-releases`], {
      encoding: 'utf8', env: { ...process.env, GH_TOKEN: token },
    }));
  });
  verifyAssets();
  const latest = api(`${endpoint}/latest`);
  const previousAsset = latest.assets.find(a => a.name === 'latest.json');
  assert.ok(previousAsset, 'Latest release is missing its signed manifest');
  const previous = decodeManifest(gh('api', `${endpoint}/assets/${previousAsset.id}`, '-H', 'Accept: application/octet-stream'));
  assert.equal(`v${previous.version}`, latest.tag_name);
  const parts = version.split('.').map(Number);
  const old = previous.version.split('.').map(Number);
  const changed = parts.findIndex((n, i) => n !== old[i]);
  assert.ok(changed >= 0 && parts[changed] > old[changed], 'Version must advance beyond latest');
  assert.ok(code > previous.platforms['android-arm64'].versionCode, 'Android versionCode must advance');
  const files = names.map(name => ({ name, size: statSync(path(name)).size, digest: `sha256:${digest(name)}` }));
  publishDraft({ api, repo, tag, runId: process.env.GITHUB_RUN_ID, files, path });
}

switch (command) {
  case 'source':
    if (process.env.GITHUB_EVENT_NAME === 'push') tagEvent();
    else assert.equal(process.env.GITHUB_EVENT_NAME, 'workflow_dispatch');
    break;
  case 'verify': verifyAssets(); break;
  case 'publish': publish(); break;
  default: throw new Error('Expected source, verify or publish');
}
