// Boots the page in a browser: loads every feature in features.js (a missing file is skipped) and starts the host.
import { createApp } from './core.js';
import { resolveConfig } from './config.js';
import { loaders } from './features.js';

async function loadFeatures() {
  const found = [];
  for (const load of loaders) {
    try {
      found.push((await load()).default);
    } catch (err) {
      console.warn('feature skipped', err);
    }
  }
  return found;
}

const timers = {
  setTimeout: (fn, ms) => window.setTimeout(fn, ms),
  clearTimeout: (id) => window.clearTimeout(id),
  setInterval: (fn, ms) => window.setInterval(fn, ms),
  clearInterval: (id) => window.clearInterval(id),
};

let storage = null;
try {
  storage = window.localStorage;
} catch {
  storage = null;
}

const app = createApp({
  document,
  window,
  fetch: window.fetch.bind(window),
  storage,
  timers,
  now: () => Date.now(),
  config: resolveConfig(window.location.search),
  features: await loadFeatures(),
  log: (msg, err) => console.warn(msg, err),
});
app.start();
