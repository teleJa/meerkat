import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { setTimeout as sleep } from 'node:timers/promises';
import { test } from 'node:test';
import vm from 'node:vm';

const client = await readFile(new URL('./client.js', import.meta.url), 'utf8');

function fixture(t) {
  const entries = new Map();
  const pulls = [];
  let component;
  let dispose;
  let unmount;
  const document = new EventTarget();
  document.visibilityState = 'visible';
  const directory = {
    entries,
    status: id => entries.get(id)?.state ?? 'cold',
    resolve: id => entries.get(id)?.value,
    invalidateAll() {}, resetConnected() {}, resetSession() {}, warm() {},
    async ensureReady(id) { return entries.get(id)?.value; },
    refresh(id) {
      const { promise, resolve } = Promise.withResolvers();
      pulls.push({ id, complete(value) {
        entries.set(id, value instanceof Error ? { state: 'failed', lastError: value } : { state: 'ready', value });
        resolve();
      } });
      return promise;
    },
  };
  vm.runInNewContext(client, {
    window: { __ModuleLoader__: { load({ factory }) {
      const plugin = factory(() => ({ useEffect(effect) { unmount = effect(); } }));
      plugin.apply({
        get: () => ({ directory }),
        effect(effect) { dispose = effect(); },
        slots: { inject: (_name, register) => register(), register: (_opts, fn) => { component = fn; } },
      });
    } } },
    document, Map, AbortSignal, AbortController, Promise, Error, setTimeout, clearTimeout, console,
  });
  assert.equal(typeof component, 'function');
  let disposed = false;
  function close() {
    if (!disposed) { dispose(); disposed = true; }
  }
  t.after(close);
  return {
    entries, pulls, directory, document, close,
    mount(id) { component({ sessionId: id }); return unmount; },
  };
}

test('coalesces bursts and one in-flight follow-up, leaving background catalogs lazy', async t => {
  const { directory, pulls, entries, mount } = fixture(t);
  entries.set('background', { state: 'ready', value: 'old background' });
  entries.set('current', { state: 'ready', value: 'shown command' });
  const unmount = mount('current');
  for (let i = 0; i < 20; i += 1) directory.invalidateAll();
  assert.equal(directory.resolve('current', 'name'), 'shown command');
  await sleep(65);
  assert.deepEqual(pulls.map(pull => pull.id), ['current']);
  for (let i = 0; i < 20; i += 1) directory.invalidateAll();
  await sleep(65);
  assert.equal(pulls.length, 1);
  assert.equal(directory.resolve('current', 'name'), 'shown command');
  pulls[0].complete('old');
  assert.equal(directory.resolve('current', 'name'), 'old');
  assert.equal(directory.resolve('background', 'name'), 'old background');
  await sleep(65);
  assert.deepEqual(pulls.map(pull => pull.id), ['current', 'current']);
  pulls[1].complete('fresh');
  await sleep(65);
  assert.equal(directory.resolve('current', 'name'), 'fresh');
  assert.equal(pulls.length, 2);

  unmount();
  mount('background');
  const signal = new AbortController().signal;
  const first = directory.ensureReady('background', signal);
  const second = directory.ensureReady('background', signal);
  await Promise.resolve();
  assert.equal(pulls.length, 3);
  pulls[2].complete('new background');
  assert.deepEqual(await Promise.all([first, second]), ['new background', 'new background']);
  assert.equal(directory.resolve('background', 'name'), 'new background');
});

test('preserves command failures, caller cancellation and disposal', async t => {
  const { directory, entries, pulls, mount, close } = fixture(t);
  const original = { state: 'ready', value: 'original' };
  entries.set('one', original);
  mount('one');
  directory.resetSession('one');
  assert.equal(directory.resolve('one', 'name'), undefined);
  const requester = new AbortController();
  const aborted = directory.ensureReady('one', requester.signal);
  const reading = directory.ensureReady('one', new AbortController().signal);
  await Promise.resolve();
  requester.abort(new Error('picker closed'));
  await assert.rejects(aborted, /picker closed/);
  assert.equal(pulls.length, 1);
  const failure = new Error('catalog failed');
  pulls[0].complete(failure);
  await assert.rejects(reading, error => error.cause === failure);
  const active = directory.ensureReady('one', new AbortController().signal);
  await Promise.resolve();
  close();
  await assert.rejects(active, { name: 'AbortError' });
  pulls[1].complete('upstream settled');
  await sleep(65);
  assert.equal(directory.resolve('one', 'name'), 'upstream settled');
  assert.equal(pulls.length, 2);
});

test('hidden pages defer refresh until visible again', async t => {
  const { directory, entries, pulls, mount, document } = fixture(t);
  entries.set('one', { state: 'ready', value: 'old' });
  mount('one');
  document.visibilityState = 'hidden';
  directory.resetConnected();
  assert.equal(directory.resolve('one', 'name'), undefined);
  await sleep(65);
  assert.equal(pulls.length, 0);
  document.visibilityState = 'visible';
  document.dispatchEvent(new Event('visibilitychange'));
  await sleep(65);
  assert.equal(pulls.length, 1);
  pulls[0].complete('new');
});
