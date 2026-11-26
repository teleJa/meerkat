import assert from 'node:assert/strict';

export function requireImmutability(readPolicy) {
  const hint = 'Check Settings → General → Releases → Enable release immutability and the Administration: read credential';
  let policy;
  try {
    policy = readPolicy();
  } catch (cause) {
    throw new Error(`Cannot verify repository release immutability. ${hint}`, { cause });
  }
  assert.equal(policy?.enabled, true, `Release immutability must be enabled. ${hint}`);
}

export function publishDraft({ api, repo, tag, runId, files, path }) {
  const endpoint = `repos/${repo}/releases`;
  // Distinguish drafts created after the immutable-policy preflight from older CI drafts.
  const marker = `<!-- Meerkat immutable release run ${runId} -->`;
  const expected = new Map(files.map(file => [file.name, file]));
  function checkAssets(release) {
    const seen = new Set();
    for (const asset of release.assets) {
      const file = expected.get(asset.name);
      assert.ok(file && !seen.has(asset.name), `Unexpected or duplicate asset: ${asset.name}`);
      assert.equal(asset.state, 'uploaded', `Incomplete asset: ${asset.name}`);
      assert.equal(asset.digest, file.digest, `Digest mismatch: ${asset.name}`);
      assert.equal(asset.size, file.size, `Size mismatch: ${asset.name}`);
      seen.add(asset.name);
    }
    return seen;
  }
  function checkDraft(release) {
    assert.ok(release.draft === true && release.tag_name === tag && release.body === marker,
      `Expected this run's draft for ${tag}`);
    return checkAssets(release);
  }
  const pages = api('--paginate', '--slurp', `${endpoint}?per_page=100`);
  const matches = pages.flat().filter(r => r.tag_name === tag);
  assert.ok(matches.length <= 1, 'Ambiguous release tag');
  const draft = matches[0] ?? api(endpoint, '-f', `tag_name=${tag}`, '-f', `name=Meerkat ${tag}`, '-f', `body=${marker}`, '-F', 'draft=true');
  checkDraft(draft);
  const id = `${endpoint}/${draft.id}`;
  for (const file of files) {
    if (checkDraft(api(id)).has(file.name)) continue;
    const upload = `https://uploads.github.com/${endpoint}/${draft.id}/assets?name=${encodeURIComponent(file.name)}`;
    try {
      api(upload, '-H', 'Content-Type: application/octet-stream', '--input', path(file.name));
    } catch (error) {
      // A competing upload or a lost response is recoverable only on an exact match.
      if (!checkDraft(api(id)).has(file.name)) throw error;
    }
  }
  assert.equal(checkDraft(api(id)).size, files.length, 'Draft has missing assets');
  // Keep the publish transition minimal; immutable releases still allow body edits.
  api('-X', 'PATCH', id, '-F', 'draft=false');
  const published = api(id);
  assert.ok(published.id === draft.id && published.tag_name === tag && published.draft === false && published.immutable === true,
    `Expected an immutable published release for ${tag}`);
  assert.equal(checkAssets(published).size, files.length, 'Published release has missing assets');
  assert.equal(api(`${endpoint}/latest`).id, draft.id, 'Published release is not latest');
}
