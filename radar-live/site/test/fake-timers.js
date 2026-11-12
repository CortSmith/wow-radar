// Deterministic clock + timers for tests (setTimeout/setInterval driven by advance()).
export function createFakeTimers(start = 0) {
  let t = start;
  let seq = 0;
  const tasks = new Map();
  const tick = () => new Promise((r) => setImmediate(r));
  return {
    now: () => t,
    setTimeout(fn, ms = 0) {
      const id = ++seq;
      tasks.set(id, { at: t + ms, fn, every: 0 });
      return id;
    },
    clearTimeout(id) {
      tasks.delete(id);
    },
    setInterval(fn, ms) {
      const id = ++seq;
      tasks.set(id, { at: t + ms, fn, every: ms });
      return id;
    },
    clearInterval(id) {
      tasks.delete(id);
    },
    pending: () => tasks.size,
    async advance(ms) {
      const end = t + ms;
      for (;;) {
        let next = null;
        for (const [id, task] of tasks) {
          if (task.at <= end && (!next || task.at < next[1].at)) next = [id, task];
        }
        if (!next) break;
        const [id, task] = next;
        t = Math.max(t, task.at);
        if (task.every) task.at += task.every;
        else tasks.delete(id);
        task.fn();
        await tick();
      }
      t = end;
    },
  };
}
