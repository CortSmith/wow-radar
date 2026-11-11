'use strict';
/**
 * ads-ui.js - single responsibility: fill the sponsor column from the static ads/ads.json.
 *
 * Public API (Radar.ui.ads): load() -> Promise. A missing or broken file leaves the column empty (CSS hides it).
 * Links open in a new tab with rel="sponsored noopener"; values are assigned as properties, never as HTML.
 */
(function (g) {
  const radar = g.Radar,
    { $, el } = radar.dom;

  function buildLink(ad) {
    const img = el('img', { attrs: { alt: ad.alt || '' } });
    img.src = ad.img;
    const link = el('a', { attrs: { rel: 'sponsored noopener', target: '_blank' } }, [img]);
    link.href = ad.href;
    return link;
  }

  async function load() {
    const ads = await radar.adapters.loadStaticJson('ads/ads.json');
    if (!Array.isArray(ads)) return;
    for (const ad of ads) $('ads').appendChild(buildLink(ad));
  }

  (radar.ui || (radar.ui = {})).ads = { load };
})(typeof self !== 'undefined' ? self : globalThis);
