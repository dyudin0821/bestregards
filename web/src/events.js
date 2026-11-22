const MAX_BATCH = 10;

// Collects usage events and sends them in small batches. Failures are ignored on purpose: the
// metric must never get in the way of the product. Events carry no text.
export function createTracker({send, delay = 1000, setTimer = setTimeout, clearTimer = clearTimeout}) {
  let queue = [];
  let timer = null;

  function flush() {
    if (timer !== null) { clearTimer(timer); timer = null; }
    while (queue.length) {
      const batch = queue.splice(0, MAX_BATCH);
      Promise.resolve(send(batch)).catch(() => {});
    }
  }

  function track(event) {
    queue.push(event);
    if (queue.length >= MAX_BATCH) flush();
    else if (timer === null) timer = setTimer(flush, delay);
  }

  return {track, flush, pending: () => queue.length};
}

export const newResultId = () =>
  (globalThis.crypto?.randomUUID?.() ?? `${Date.now().toString(36)}${Math.random().toString(36).slice(2)}`)
    .replace(/[^a-z0-9]/gi, '').toLowerCase().slice(0, 16).padEnd(8, '0');
