// The single list of page features. Delete a line (and its file) to remove that feature; nothing else changes.
export const loaders = [
  () => import('./features/status.js'),
  () => import('./features/view-key.js'),
  () => import('./features/ruleset-switch.js'),
  () => import('./features/chart-picker.js'),
  () => import('./features/group-filter.js'),
  () => import('./features/time-zone.js'),
  () => import('./features/radar-canvas.js'),
  () => import('./features/world-map.js'),
  () => import('./features/track-overlay.js'),
  () => import('./features/search.js'),
  () => import('./features/bounty-panel.js'),
  () => import('./features/contact-list.js'),
  () => import('./features/history-panel.js'),
];
