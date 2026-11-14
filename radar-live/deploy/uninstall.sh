#!/bin/sh
# Removes the radar-live services and binaries. Run as root:   sudo sh uninstall.sh [--purge]
# Without --purge the database (/var/lib/radar-live), backups, settings/keys (/etc/radar-live)
# (including the armory client id and secret files in /etc/radar-live/secrets)
# the key manager's keyring, vault and sealing key (/var/lib/radar-keys) and the radar user are kept, so a later install.sh brings everything back.
# --purge deletes all of those too, plus the security log (/var/log/radar).
# Firewall rules (ufw) are never touched; undo them with the ROLLBACK lines in ufw-setup.sh.
set -eu

purge=no
case ${1:-} in
    "") ;;
    --purge) purge=yes ;;
    *)
        echo "usage: sh uninstall.sh [--purge]" >&2
        exit 2
        ;;
esac
[ "$(id -u)" -eq 0 ] || {
    echo "uninstall.sh: run as root (sudo sh uninstall.sh)" >&2
    exit 1
}

if command -v systemctl >/dev/null 2>&1; then
    for unit in radar-armory.timer radar-armory.service radar-ingest.service radar-db.service; do
        systemctl disable --now "$unit" >/dev/null 2>&1 || true
        rm -f "/etc/systemd/system/$unit"
    done
    systemctl daemon-reload
fi
rm -f /usr/local/bin/radar-keys /usr/local/bin/radar-keys.new /usr/local/sbin/radar-keys-backup
rm -f /usr/local/bin/radar-db /usr/local/bin/radar-ingest /usr/local/bin/radar-armory /usr/local/bin/radar-db.new /usr/local/bin/radar-ingest.new /usr/local/bin/radar-armory.new
rm -rf /usr/local/share/doc/radar-live
# fail2ban filter and jail shipped by install.sh --with-fail2ban (harmless when absent).
if [ -f /etc/fail2ban/jail.d/radar-live.local ] || [ -f /etc/fail2ban/filter.d/radar-live.conf ]; then
    rm -f /etc/fail2ban/jail.d/radar-live.local /etc/fail2ban/jail.d/radar-live.local.new /etc/fail2ban/filter.d/radar-live.conf
    if command -v systemctl >/dev/null 2>&1; then
        systemctl reload fail2ban >/dev/null 2>&1 || true
    fi
fi

if [ "$purge" = yes ]; then
    rm -rf /var/lib/radar-live /var/lib/radar-keys /var/backups/radar-live /etc/radar-live /var/log/radar
    if id radar >/dev/null 2>&1; then
        userdel radar >/dev/null 2>&1 || true
    fi
    echo "radar-live removed, including data, the key manager's keyring and vault, backups, settings, logs and the radar user."
else
    echo "radar-live removed. Kept: /var/lib/radar-live, /var/lib/radar-keys, /var/backups/radar-live, /etc/radar-live, /var/log/radar (use --purge to delete)."
fi
