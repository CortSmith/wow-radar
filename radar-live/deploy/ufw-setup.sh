#!/bin/sh
# Firewall for the radar-live laptop (ufw). Default deny inbound; SSH only from your LAN, rate limited;
# the web port only from the LAN, from nowhere (tunnel mode) or, on request, from anywhere.
#
# Usage:  sudo sh ufw-setup.sh [--dry-run] [--tunnel | --public] LAN_CIDR
#   LAN_CIDR   your home network, for example 192.0.2.0/24 (a placeholder; look at `ip -4 addr`). Needed
#              always: it is the only place SSH is allowed from. IPv6 networks such as fd00:1234::/64 work too.
#   --tunnel   open NO web port at all. Use with Cloudflare Tunnel / a local Caddy: the proxy runs on this
#              machine and reaches radar-db on loopback, cloudflared only makes outbound connections.
#   --public   allow the web port 8787/tcp from anywhere (only with Caddy-style direct exposure; prefer
#              --tunnel). Without --public or --tunnel the web port is opened for LAN_CIDR only.
#   --dry-run  print what would be done, change nothing (needs neither root nor ufw).
#
# Never opened, in any mode: 8788 (radar-ingest) and 8789 (radar-db internal write API/metrics). They are
# loopback listeners; a feeder on another PC goes through the tunnel or a VPN (see docs/security/hardening.md).
# Safe to run again: ufw skips rules that already exist. Established SSH sessions survive `ufw enable`.
#
# ROLLBACK (pick one):
#   sudo ufw disable                       # stop filtering, keep the rules
#   sudo ufw status numbered && sudo ufw delete N   # remove single rules
#   sudo ufw --force reset                 # delete every rule and disable (back to a clean slate)
set -eu

WEB_PORT=8787
SSH_PORT=22
dry=no
mode=lan
cidr=""

die() {
    echo "ufw-setup.sh: $*" >&2
    exit 1
}

while [ $# -gt 0 ]; do
    case $1 in
        --dry-run) dry=yes ;;
        --public) mode=public ;;
        --tunnel) mode=tunnel ;;
        -h | --help)
            sed -n '2,21p' "$0"
            exit 0
            ;;
        -*) die "unknown option: $1" ;;
        *)
            [ -z "$cidr" ] || die "only one LAN_CIDR is accepted"
            cidr=$1
            ;;
    esac
    shift
done
[ -n "$cidr" ] || die "LAN_CIDR is required (for example 192.0.2.0/24); see --help"

# Accept a.b.c.d/n (n 1..32, octets 0..255) or an IPv6 prefix (hex and colons, /1../128). /0 means "anywhere"
# and is refused: SSH must never be open to the world.
valid_cidr() {
    case $1 in
        *[!0-9a-fA-F:./]*) return 1 ;;
    esac
    case $1 in
        */*/*) return 1 ;;
        */*) ;;
        *) return 1 ;;
    esac
    addr=${1%/*}
    len=${1#*/}
    case $len in
        "" | *[!0-9]*) return 1 ;;
    esac
    [ "$len" -ge 1 ] || return 1
    case $addr in
        *:*)
            [ "$len" -le 128 ] || return 1
            printf '%s' "$addr" | grep -Eq '^[0-9a-fA-F:]+$' || return 1
            printf '%s' "$addr" | grep -q ':.*:' || return 1
            ;;
        *)
            [ "$len" -le 32 ] || return 1
            printf '%s' "$addr" | grep -Eq '^[0-9]{1,3}(\.[0-9]{1,3}){3}$' || return 1
            old_ifs=$IFS
            IFS=.
            # shellcheck disable=SC2086
            set -- $addr
            IFS=$old_ifs
            for octet in "$@"; do
                [ "$octet" -le 255 ] || return 1
            done
            ;;
    esac
    return 0
}
valid_cidr "$cidr" || die "'$cidr' is not a usable network (expected like 192.0.2.0/24; /0 is refused)"

# run: print in dry-run mode, execute otherwise. Arguments are never passed through a shell.
run() {
    if [ "$dry" = yes ]; then
        echo "ufw $*"
    else
        ufw "$@"
    fi
}

if [ "$dry" = no ]; then
    [ "$(id -u)" -eq 0 ] || die "run as root (sudo sh ufw-setup.sh ...), or use --dry-run"
    command -v ufw >/dev/null 2>&1 || die "ufw is not installed (sudo apt install ufw)"
fi

echo "mode: $mode, trusted network: $cidr"

# IPv6: make sure ufw also filters it, otherwise v6 stays wide open.
if [ "$dry" = yes ]; then
    echo "IPV6=yes (would be set in /etc/default/ufw if it says no)"
elif [ -f /etc/default/ufw ] && grep -q '^IPV6=no' /etc/default/ufw; then
    sed -i 's/^IPV6=no/IPV6=yes/' /etc/default/ufw
    echo "IPV6=yes set in /etc/default/ufw"
else
    echo "IPV6=yes (already)"
fi

run default deny incoming
run default allow outgoing
run default deny routed
# SSH: only from the LAN, and rate limited (6 new connections per 30 s per address).
run limit from "$cidr" to any port "$SSH_PORT" proto tcp
case $mode in
    lan) run allow from "$cidr" to any port "$WEB_PORT" proto tcp ;;
    public) run allow "$WEB_PORT/tcp" ;;
    tunnel) echo "tunnel mode: no inbound web port is opened" ;;
esac

if [ "$dry" = yes ]; then
    echo "ufw --force enable"
    echo "ufw status verbose"
    echo "dry run: nothing was changed"
    exit 0
fi

ufw --force enable
ufw status verbose
echo
echo "Check from another machine that SSH still works BEFORE closing this session."
echo "ROLLBACK: sudo ufw disable    (or: sudo ufw --force reset)"
