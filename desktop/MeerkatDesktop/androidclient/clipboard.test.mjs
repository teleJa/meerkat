import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';
import { runInNewContext } from 'node:vm';

const source = readFileSync(new URL('./clipboard.js', import.meta.url), 'utf8');
const writePath = '/__meerkat/clipboard';
const renewPath = `${writePath}/renew`;
const text = '复制消息\n第二行';

function response(status, grant = null) {
  return { status, headers: { get: name => name === 'X-Meerkat-Clipboard' ? grant : null } };
}

function setup(t, { responses = [], nativeWrite } = {}) {
  const requests = [];
  const notices = [];
  const parent = {};
  const callbacks = new Map();
  const navigator = { userActivation: { isActive: true }, clipboard: { writeText: nativeWrite } };
  let clock = 0;
  const window = {
    parent,
    top: parent,
    addEventListener(type, callback) { callbacks.set(type, callback); },
    fetch: t.mock.fn(async (path, options) => {
      requests.push({
        path,
        grant: options.headers['X-Meerkat-Clipboard'],
        pageGrant: options.headers['X-Meerkat-Clipboard-Page'],
        body: options.body && JSON.parse(options.body),
      });
      const next = responses[requests.length - 1];
      return typeof next === 'function' ? next() : next ?? response(500);
    }),
  };
  runInNewContext(source, {
    window, navigator, DOMException,
    location: { ancestorOrigins: ['https://wails.localhost'] },
    performance: { now: () => clock },
    document: {
      currentScript: { dataset: { meerkatClipboard: 'old-grant', meerkatClipboardPage: 'page-grant' }, remove() {} },
      hidden: false,
      createElement: () => ({ setAttribute() {}, style: {} }),
      body: { append: notice => notices.push(notice) },
    },
    clearTimeout() {},
    setTimeout() {},
  });
  assert.ok(callbacks.has('click'));
  return {
    requests,
    notices,
    click: (isTrusted = true) => callbacks.get('click')({ isTrusted }),
    advance: milliseconds => { clock += milliseconds; },
    write: value => navigator.clipboard.writeText(value),
  };
}

const writeRequest = (grant, body = { text }) => ({ path: writePath, grant, pageGrant: undefined, body });
const renewRequest = { path: renewPath, grant: undefined, pageGrant: 'page-grant', body: undefined };

async function nativeDenied() {
  throw new DOMException('Browser denied clipboard write', 'NotAllowedError');
}

test('native success does not send message text over the bridge', async t => {
  const nativeWrite = t.mock.fn(async () => {});
  const shim = setup(t, { nativeWrite, responses: [response(204)] });
  shim.click();
  await shim.write(text);
  assert.equal(nativeWrite.mock.callCount(), 1);
  assert.deepEqual(Array.from(nativeWrite.mock.calls[0].arguments), [text]);
  assert.deepEqual(shim.requests, [writeRequest('old-grant', { native: true })]);
});

test('native rejection falls back to the bridge with unchanged text', async t => {
  const nativeWrite = t.mock.fn(nativeDenied);
  const shim = setup(t, { nativeWrite, responses: [response(204)] });
  shim.click();
  await shim.write(text);
  assert.equal(nativeWrite.mock.callCount(), 1);
  assert.deepEqual(Array.from(nativeWrite.mock.calls[0].arguments), [text]);
  assert.deepEqual(shim.requests, [writeRequest('old-grant')]);
});

test('a trusted click can be used for only one clipboard write', async t => {
  const shim = setup(t, { responses: [response(204), response(204)] });
  shim.click();
  await shim.write(text);
  await assert.rejects(shim.write(text), { name: 'NotAllowedError', message: /再点一次/ });
  assert.deepEqual(shim.requests, [writeRequest('old-grant')]);
  shim.click();
  await shim.write(text);
  assert.deepEqual(shim.requests, [writeRequest('old-grant'), writeRequest('old-grant')]);
});

test('an expired grant renews once and remains current for a later click', async t => {
  const shim = setup(t, { responses: [response(403), response(204, 'new-grant'), response(204), response(204)] });
  shim.click();
  await shim.write(text);
  shim.click();
  await shim.write(text);
  assert.deepEqual(shim.requests, [writeRequest('old-grant'), renewRequest, writeRequest('new-grant'), writeRequest('new-grant')]);
});

test('a second 403 asks to reload without renewing again', async t => {
  const shim = setup(t, { responses: [response(403), response(204, 'new-grant'), response(403)] });
  shim.click();
  await assert.rejects(shim.write(text), { name: 'NotAllowedError', message: /请重载 DSH/ });
  assert.deepEqual(shim.requests, [writeRequest('old-grant'), renewRequest, writeRequest('new-grant')]);
  assert.match(shim.notices[0].textContent, /请重载 DSH/);
});

test('failed renewal reports failure and does not retry the write', async t => {
  const shim = setup(t, { responses: [response(403), response(500)] });
  shim.click();
  await assert.rejects(shim.write(text), { name: 'NotAllowedError', message: /请重载 DSH/ });
  assert.deepEqual(shim.requests, [writeRequest('old-grant'), renewRequest]);
});

test('renewing a grant does not extend the original gesture window', async t => {
  const shim = setup(t, { responses: [response(403), () => {
    shim.advance(5001);
    return response(204, 'new-grant');
  }] });
  shim.click();
  await assert.rejects(shim.write(text), { name: 'NotAllowedError' });
  assert.deepEqual(shim.requests, [writeRequest('old-grant'), renewRequest]);
});

test('synthetic clicks and expired trusted clicks cannot write', async t => {
  const nativeWrite = t.mock.fn(async () => {});
  const shim = setup(t, { nativeWrite });
  shim.click(false);
  await assert.rejects(shim.write(text), { name: 'NotAllowedError', message: /再点一次/ });
  shim.click();
  shim.advance(5001);
  await assert.rejects(shim.write(text), { name: 'NotAllowedError', message: /再点一次/ });
  assert.deepEqual(shim.requests, []);
  assert.equal(nativeWrite.mock.callCount(), 0);
});
