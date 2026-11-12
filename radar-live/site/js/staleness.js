import { CONFIG } from './config.js';

/** 'live' | 'stale' | 'gone' from a contact's lastSeen. */
export function classify(contact, now, cfg = CONFIG) {
  const age = now - contact.lastSeen;
  if (age >= cfg.GONE_AFTER_MS) return 'gone';
  return age >= cfg.STALE_AFTER_MS ? 'stale' : 'live';
}

/** Contacts of a Map that belong in the live view (gone rows are dropped; pinnedGuid always stays). */
export function visibleContacts(contacts, now, pinnedGuid = '', cfg = CONFIG) {
  const out = [];
  for (const c of contacts.values()) {
    if (c.guid === pinnedGuid || classify(c, now, cfg) !== 'gone') out.push(c);
  }
  return out;
}
