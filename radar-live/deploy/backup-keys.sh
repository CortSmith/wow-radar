#!/bin/sh
# Nightly copy of the key manager files (run by cron as root, see backup.cron):
#   keyring.json (hashes, e-mail addresses, limits) and keyvault.bin (the keys, sealed with AES-256-GCM).
# The sealing key keyvault.key is NOT copied: a backup alone cannot reveal any key. Restoring the vault needs the
# backup plus the original /var/lib/radar-keys/vault/keyvault.key (keep your own offline copy of that file).
# Files go to /var/backups/radar-live/keys (root only, 0700); the newest 14 of each kind are kept.
set -eu
umask 077

KEYS_DIR=/var/lib/radar-keys
DEST=/var/backups/radar-live/keys
KEEP=14

[ "$(id -u)" -eq 0 ] || {
    echo "radar-keys-backup: run as root" >&2
    exit 1
}
install -d -o root -g root -m 0700 "$DEST"
ts=$(date -u +%Y%m%dT%H%M%SZ)
status=0
copy() { # file, name prefix, extension
    [ -f "$1" ] || return 0
    if cp "$1" "$DEST/$2-$ts.$3.tmp" && mv "$DEST/$2-$ts.$3.tmp" "$DEST/$2-$ts.$3"; then
        ls -1t "$DEST"/"$2"-*."$3" 2>/dev/null | tail -n +$((KEEP + 1)) | while read -r old; do rm -f "$old"; done
    else
        rm -f "$DEST/$2-$ts.$3.tmp"
        status=1
    fi
}
copy "$KEYS_DIR/keyring/keyring.json" keyring json
copy "$KEYS_DIR/vault/keyvault.bin" keyvault bin
exit $status
