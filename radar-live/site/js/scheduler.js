// The sweep scheduler: chained setTimeout (never overlapping), debounced user requests, backoff, visibility pause.
export function createScheduler(opts) {
  const { run, intervalMs, debounceMs, retryBaseMs, maxBackoffMs, timers, now, onError } = opts;
  let timer = null;
  let nextAtMs = 0;
  let running = false;
  let ctrl = null;
  let rerun = false;
  let hidden = false;
  let stopped = true;
  let failures = 0;
  let pausedDeadlineMs = null; // absolute time the next sweep was due when the tab was hidden

  const clear = () => {
    if (timer !== null) timers.clearTimeout(timer);
    timer = null;
    nextAtMs = 0;
  };
  const schedule = (delay) => {
    clear();
    if (stopped || hidden) return;
    nextAtMs = now() + delay;
    timer = timers.setTimeout(tick, delay);
  };
  const backoff = () => Math.min(maxBackoffMs, retryBaseMs * 2 ** (failures - 1));

  async function tick() {
    timer = null;
    if (stopped || hidden || running) return;
    running = true;
    ctrl = new AbortController();
    const signal = ctrl.signal;
    let next = intervalMs;
    try {
      await run(signal);
      failures = 0;
    } catch (err) {
      if (!signal.aborted) {
        failures += 1;
        next = backoff();
        if (onError) onError(err);
      }
    } finally {
      running = false;
    }
    if (rerun) {
      rerun = false;
      next = 0;
    }
    schedule(next);
  }

  return {
    start() {
      stopped = false;
      schedule(0);
    },
    stop() {
      stopped = true;
      rerun = false;
      pausedDeadlineMs = null;
      if (ctrl) ctrl.abort();
      clear();
    },
    /** A user input changed the sector: sweep soon (debounced), aborting any request in flight. */
    requestNow() {
      if (stopped) return;
      if (running) {
        rerun = true;
        ctrl.abort();
        return;
      }
      schedule(debounceMs);
    },
    /** "Update now" button: no debounce. */
    refreshNow() {
      if (stopped || running) return;
      schedule(0);
    },
    setHidden(value) {
      if (value === hidden) return;
      hidden = value;
      if (value) {
        // Remember the pending deadline so the countdown survives the pause (null: nothing was pending).
        pausedDeadlineMs = timer !== null ? nextAtMs : null;
        clear();
        return;
      }
      const deadline = pausedDeadlineMs;
      pausedDeadlineMs = null;
      // Resume with the time that was left; sweep immediately if the deadline passed or none was pending.
      if (!stopped && !running) schedule(deadline === null ? 0 : Math.max(0, deadline - now()));
    },
    nextAt: () => nextAtMs,
    failures: () => failures,
    isRunning: () => running,
  };
}
