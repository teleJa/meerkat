window.__ModuleLoader__.load({
  id: '@local/meerkat-command-refresh',
  factory(require) {
    const { useEffect } = require('react');
    const mergeWindowMs = 50;

    // DSH 0.2.2-rc.2 has no public hook for catalog invalidation. Keep its
    // fetch, cache and command resolution intact and replace only scheduling.
    function patchDirectory(directory, isVisible) {
      const methods = ['invalidateAll', 'resetConnected', 'resetSession', 'warm', 'ensureReady', 'resolve'];
      if (!Object.isExtensible(directory) || !(directory.entries instanceof Map) ||
          !['refresh', 'status', ...methods].every(name => typeof directory[name] === 'function')) {
        return undefined;
      }
      const originals = Object.fromEntries(methods.map(name => [name, Object.getOwnPropertyDescriptor(directory, name)]));
      const resolve = directory.resolve.bind(directory);
      const originalEnsureReady = directory.ensureReady.bind(directory);
      const refresh = directory.refresh.bind(directory);
      const status = directory.status.bind(directory);
      const mounted = new Map();
      const sessions = new Map();
      const lifetime = new AbortController();
      let generation = 0;
      let connectionResetGeneration = -1;
      let timer;

      function session(id) {
        let state = sessions.get(id);
        if (state === undefined) {
          state = { loadedGeneration: generation === 0 ? 0 : -1, revision: 0, loadedRevision: 0, pending: undefined };
          sessions.set(id, state);
        }
        return state;
      }

      function dirty(state) {
        return state.loadedGeneration !== generation || state.loadedRevision !== state.revision;
      }

      function start(id, state) {
        if (state.pending) return state.pending;
        const wantedGeneration = generation;
        const wantedRevision = state.revision;
        const pending = Promise.resolve().then(async () => {
          lifetime.signal.throwIfAborted();
          // A pre-patch warmup may already be in flight on first activation.
          // Join it only before the first invalidation of this session.
          if (wantedGeneration === 0 && wantedRevision === 0 && status(id) === 'pending') {
            try { await originalEnsureReady(id, lifetime.signal); } catch (error) {
              if (lifetime.signal.aborted) throw error;
            }
          } else {
            await refresh(id);
          }
          state.loadedGeneration = wantedGeneration;
          state.loadedRevision = wantedRevision;
        }).finally(() => {
          state.pending = undefined;
          if (dirty(state) && mounted.has(id)) schedule();
        });
        state.pending = pending;
        return pending;
      }

      function schedule() {
        if (timer !== undefined || lifetime.signal.aborted || !isVisible() || mounted.size === 0) return;
        timer = setTimeout(() => {
          timer = undefined;
          if (!isVisible()) return;
          for (const id of mounted.keys()) {
            const state = session(id);
            if (dirty(state) || status(id) === 'cold') void start(id, state).catch(() => {});
          }
        }, mergeWindowMs);
      }

      function invalidateAll() {
        generation += 1;
        schedule();
      }

      async function ensureReady(id, callerSignal) {
        const signal = AbortSignal.any([lifetime.signal, callerSignal]);
        const state = session(id);
        for (;;) {
          signal.throwIfAborted();
          if (status(id) === 'ready' && !dirty(state)) return originalEnsureReady(id, signal);
          await waitFor(start(id, state), signal);
          if (!dirty(state) && status(id) === 'failed') {
            const cause = directory.entries.get(id)?.lastError;
            throw new Error(`command directory warmup failed: ${cause instanceof Error ? cause.message : String(cause)}`, { cause });
          }
        }
      }

      const overrides = {
        invalidateAll,
        resetConnected() {
          invalidateAll();
          connectionResetGeneration = generation;
        },
        resetSession(id) {
          session(id).revision += 1;
          if (mounted.has(id)) schedule();
        },
        warm(id) {
          // A controller can be created for a background conversation. Its
          // mounted composer or an explicit command request starts the pull.
          if (mounted.has(id)) schedule();
        },
        ensureReady,
        resolve(id, name) {
          const state = session(id);
          // An already-open menu can still use the current snapshot during a
          // soft refresh. Reconnects and preset switches invalidate it outright.
          if (state.loadedGeneration < connectionResetGeneration || state.loadedRevision !== state.revision) return undefined;
          return resolve(id, name);
        },
      };
      for (const name of methods) {
        Object.defineProperty(directory, name, { configurable: true, writable: true, value: overrides[name] });
      }

      return {
        schedule,
        mount(id) {
          const count = mounted.get(id) ?? 0;
          mounted.set(id, count + 1);
          if (count === 0 && status(id) === 'failed') session(id).revision += 1;
          schedule();
          return () => {
            const remaining = mounted.get(id);
            if (remaining === 1) mounted.delete(id);
            else if (remaining !== undefined) mounted.set(id, remaining - 1);
          };
        },
        dispose() {
          lifetime.abort();
          clearTimeout(timer);
          mounted.clear();
          for (const name of methods) {
            if (originals[name]) Object.defineProperty(directory, name, originals[name]);
            else delete directory[name];
          }
        },
      };
    }

    function waitFor(pending, signal) {
      signal.throwIfAborted();
      return new Promise((resolve, reject) => {
        const abort = () => reject(signal.reason);
        signal.addEventListener('abort', abort, { once: true });
        pending.then(resolve, reject).finally(() => signal.removeEventListener('abort', abort));
      });
    }

    function apply(ctx) {
      ctx.effect(() => {
        const patch = patchDirectory(ctx.get('commandUi')?.directory, () => document.visibilityState !== 'hidden');
        if (!patch) {
          console.warn('[meerkat-command-refresh] This DSH command directory is incompatible.');
          return;
        }
        function MountedComposer({ sessionId }) {
          useEffect(() => patch.mount(sessionId), [sessionId]);
          return null;
        }
        const removeSlot = ctx.slots.inject('conversation.composer.dock', () => ctx.slots.register({
          name: 'conversation.composer.dock', id: 'meerkat-command-refresh',
          inject: sessionId => ({ sessionId }),
        }, MountedComposer));
        document.addEventListener('visibilitychange', patch.schedule);
        return () => {
          document.removeEventListener('visibilitychange', patch.schedule);
          removeSlot?.();
          patch.dispose();
        };
      }, 'meerkat: command catalog refresh scheduling');
    }

    return { inject: ['commandUi', 'slots'], apply };
  },
});
