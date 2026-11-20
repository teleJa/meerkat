import { createHash, createPrivateKey, createPublicKey, sign } from 'node:crypto';
import { spawnSync } from 'node:child_process';
import { createReadStream, readFileSync, writeFileSync } from 'node:fs';
import { basename, dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const repository = 'teleJa/meerkat';
const [action, version, installer, manifestURL] = process.argv.slice(2);
const sourceDir = fileURLToPath(new URL('../', import.meta.url));
const stableVersion = /^(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)$/;

function git(...args) {
  const result = spawnSync('git', ['-C', sourceDir, ...args], { encoding: 'utf8' });
  if (result.error) throw result.error;
  if (result.status !== 0) throw new Error(result.stderr.trim() || 'Unable to verify the Git source.');
  return result.stdout.trim();
}

function verifyRelease() {
  if (!stableVersion.test(version) || readFileSync(join(sourceDir, 'VERSION'), 'utf8').trim() !== version) {
    throw new Error('The release version must match VERSION in the source checkout.');
  }
  if (git('status', '--porcelain')) throw new Error('Build releases from a clean checkout.');
  const ref = `refs/tags/v${version}`;
  const refs = new Map(git('ls-remote', '--exit-code', `https://github.com/${repository}.git`, ref, `${ref}^{}`)
    .split('\n').map(line => line.split('\t').reverse()));
  if ((refs.get(`${ref}^{}`) ?? refs.get(ref)) !== git('rev-parse', 'HEAD')) {
    throw new Error(`Check out the published v${version} source tag before building the release.`);
  }
}

if (action === 'verify-release' || action === 'publish') verifyRelease();
if (action === 'verify-release') process.exit(0);
const filename = process.env.MEERKAT_RELEASE_SIGNING_KEY_FILE;
if (!filename) throw new Error('Set MEERKAT_RELEASE_SIGNING_KEY_FILE to the Ed25519 PEM file on the release machine.');
const privateKey = createPrivateKey(readFileSync(filename));
if (privateKey.asymmetricKeyType !== 'ed25519') throw new Error('The release signing key must be Ed25519.');

if (action === 'public-key') {
  const publicKey = createPublicKey(privateKey).export({ format: 'jwk' });
  console.log(Buffer.from(publicKey.x, 'base64url').toString('hex'));
} else if (action === 'manifest' || action === 'publish') {
  if (!stableVersion.test(version) || !installer) {
    throw new Error('Expected a stable version and an installer path.');
  }
  const tag = `v${version}`;
  if (action === 'publish' && basename(installer) !== `Meerkat-Setup-${version}.exe`) {
    throw new Error('Installer filename does not match the release version.');
  }
  const hash = createHash('sha256');
  for await (const bytes of createReadStream(installer)) hash.update(bytes);
  const notes = process.env.MEERKAT_RELEASE_NOTES_FILE ? readFileSync(process.env.MEERKAT_RELEASE_NOTES_FILE, 'utf8') : `Meerkat ${tag}`;
  const url = action === 'manifest' && manifestURL ? manifestURL : `https://github.com/${repository}/releases/download/${tag}/${encodeURIComponent(basename(installer))}`;
  const manifest = Buffer.from(JSON.stringify({ version, notes, platforms: { 'windows-amd64': { url, sha256: hash.digest('hex') } } }));
  const output = join(dirname(installer), 'latest.json');
  const envelope = JSON.stringify({ manifest: manifest.toString('base64'), signature: sign(null, manifest, privateKey).toString('base64') }) + '\n';
  if (Buffer.byteLength(envelope) > 64 * 1024) throw new Error('Update manifest exceeds the client size limit.');
  writeFileSync(output, envelope);
  if (action === 'publish') {
    // Publish an already-tagged source revision. Never tag the public default
    // branch implicitly from this separate development repository.
    const published = spawnSync('gh', ['release', 'create', tag, installer, output, '--repo', repository, '--verify-tag', '--latest', '--title', `Meerkat ${tag}`, '--notes', notes], { stdio: 'inherit' });
    if (published.error) throw published.error;
    process.exitCode = published.status ?? 1;
  } else {
    console.log(output);
  }
} else {
  throw new Error('Use public-key, manifest VERSION INSTALLER [URL], or publish VERSION INSTALLER.');
}
