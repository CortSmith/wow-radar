#!/bin/sh
# Installs radar-live (radar-db + radar-ingest) as hardened systemd services. Run as root from the folder
# that holds the dist files:   sudo sh install.sh
# Safe to run again: it never replaces an existing /etc/radar-live/radar.env, never touches the database,
# and an upgrade just swaps the binaries and restarts the services. Two runs at once are refused (flock).
# Everything installed is checked against SHA256SUMS first; without a matching SHA256SUMS nothing is installed.
#
# Options:  --arch amd64|arm64      pick the binaries (default: this machine)
#           --no-start              install and enable, but do not (re)start the services
#           --with-ufw LAN_CIDR     also run ufw-setup.sh (default deny, SSH and web port only from LAN_CIDR)
#           --ufw-tunnel            with --with-ufw: open no web port at all (Cloudflare Tunnel / local proxy)
#           --with-fail2ban         also install the fail2ban filter and jail for the security log
# A 'maps' folder next to this script (zone images + index.json, built with --with-maps) is checked and
# installed read-only into /var/lib/radar-live/maps.
# Afterwards:  sudo sh verify-hardening.sh   (docs/security/hardening.md explains every check)
set -eu
# Files created below (the settings file with the keys) start private; modes are then set explicitly.
umask 077

BIN_DIR=/usr/local/bin
ETC_DIR=/etc/radar-live
ENV_FILE=$ETC_DIR/radar.env
DATA_DIR=/var/lib/radar-live
MAPS_DIR=$DATA_DIR/maps
BACKUP_DIR=/var/backups/radar-live
LOG_DIR=/var/log/radar
DOC_DIR=/usr/local/share/doc/radar-live
UNIT_DIR=/etc/systemd/system
LOCK_FILE=/run/lock/radar-live-install.lock
SVC_USER=radar

src=$(cd "$(dirname "$0")" && pwd)
nl=$(printf '\n_')
nl=${nl%_}
arch=""
start=yes
ufw_cidr=""
ufw_tunnel=no
with_f2b=no

die() {
    echo "install.sh: $*" >&2
    exit 1
}

while [ $# -gt 0 ]; do
    case $1 in
        --arch)
            [ $# -ge 2 ] || die "--arch needs a value"
            arch=$2
            shift 2
            ;;
        --no-start)
            start=no
            shift
            ;;
        --with-ufw)
            [ $# -ge 2 ] || die "--with-ufw needs a LAN network such as 192.0.2.0/24"
            ufw_cidr=$2
            shift 2
            ;;
        --ufw-tunnel)
            ufw_tunnel=yes
            shift
            ;;
        --with-fail2ban)
            with_f2b=yes
            shift
            ;;
        -h | --help)
            sed -n '2,18p' "$0"
            exit 0
            ;;
        *) die "unknown option: $1" ;;
    esac
done

[ "$(id -u)" -eq 0 ] || die "run as root (sudo sh install.sh)"
[ "$ufw_tunnel" = no ] || [ -n "$ufw_cidr" ] || die "--ufw-tunnel only makes sense together with --with-ufw LAN_CIDR"

# One installer at a time (a second run would race on the binaries and units).
if command -v flock >/dev/null 2>&1 && [ -d /run/lock ]; then
    exec 9>"$LOCK_FILE"
    flock -n 9 || die "another install.sh is running"
fi

if [ -z "$arch" ]; then
    case $(uname -m) in
        x86_64 | amd64) arch=amd64 ;;
        aarch64 | arm64) arch=arm64 ;;
        *) die "unsupported CPU $(uname -m); pass --arch amd64 or arm64" ;;
    esac
fi
case $arch in amd64 | arm64) ;; *) die "--arch must be amd64 or arm64" ;; esac

files="radar-db-linux-$arch radar-ingest-linux-$arch radar.env.example radar-db.service radar-ingest.service"
[ -z "$ufw_cidr" ] || files="$files ufw-setup.sh"
for f in $files; do
    [ -f "$src/$f" ] || die "missing $src/$f (copy the whole dist folder)"
done
if [ "$with_f2b" = yes ]; then
    for f in fail2ban/radar-live.conf fail2ban/jail.local; do
        [ -f "$src/$f" ] || die "missing $src/$f (copy deploy/fail2ban next to install.sh)"
    done
fi

# Verify BEFORE anything is installed. SHA256SUMS is mandatory: it lists every file of dist/.
command -v sha256sum >/dev/null 2>&1 || die "sha256sum not found (coreutils)"
[ -f "$src/SHA256SUMS" ] || die "SHA256SUMS is missing; copy the whole dist folder (refusing to install unverified files)"
echo "verifying checksums"
for f in $files; do
    line=$(grep "  $f\$" "$src/SHA256SUMS" || true)
    [ -n "$line" ] || die "$f is not listed in SHA256SUMS"
    (cd "$src" && echo "$line" | sha256sum -c - >/dev/null 2>&1) || die "checksum mismatch for $f"
done

# Optional zone maps: every file of ./maps must be listed in SHA256SUMS and match. Plain path characters only.
map_files=""
if [ -d "$src/maps" ]; then
    map_files=$(cd "$src" && find maps -type f | LC_ALL=C sort)
    case $map_files in
        *[!A-Za-z0-9._/"$nl"-]*) die "maps/ holds a file name with unusual characters; use plain names" ;;
    esac
    for f in $map_files; do
        line=$(awk -v n="$f" 'substr($0, 67) == n' "$src/SHA256SUMS")
        [ -n "$line" ] || die "$f is not listed in SHA256SUMS"
        (cd "$src" && echo "$line" | sha256sum -c - >/dev/null 2>&1) || die "checksum mismatch for $f"
    done
fi

# Service account and directories.
if ! id "$SVC_USER" >/dev/null 2>&1; then
    echo "creating user $SVC_USER"
    useradd --system --user-group --home-dir "$DATA_DIR" --no-create-home --shell /usr/sbin/nologin "$SVC_USER"
fi
install -d -o "$SVC_USER" -g "$SVC_USER" -m 0700 "$DATA_DIR"
install -d -o "$SVC_USER" -g "$SVC_USER" -m 0700 "$BACKUP_DIR"
install -d -o "$SVC_USER" -g "$SVC_USER" -m 0700 "$LOG_DIR"
install -d -o root -g "$SVC_USER" -m 0750 "$ETC_DIR"
install -d -m 0755 "$DOC_DIR"
# Zone maps: owned by root, so the service (user radar) can read them but never change them.
install -d -o root -g root -m 0755 "$MAPS_DIR"

# Binaries: copy next to the target, then rename, so a running service is never half-overwritten.
install_bin() {
    install -m 0755 -o root -g root "$src/$1-linux-$arch" "$BIN_DIR/$1.new"
    mv -f "$BIN_DIR/$1.new" "$BIN_DIR/$1"
}
install_bin radar-db
install_bin radar-ingest

if [ -n "$map_files" ]; then
    echo "installing zone maps into $MAPS_DIR"
    for f in $map_files; do
        rel=${f#maps/}
        install -d -o root -g root -m 0755 "$MAPS_DIR/$(dirname "$rel")"
        install -m 0644 -o root -g root "$src/$f" "$MAPS_DIR/$rel"
    done
fi

# Settings and keys: created once, then left alone (except the mode, which is re-asserted).
new_keys=no
if [ -f "$ENV_FILE" ]; then
    echo "keeping existing $ENV_FILE"
    chown root:"$SVC_USER" "$ENV_FILE"
    chmod 0640 "$ENV_FILE"
    if grep -v '^[[:space:]]*#' "$ENV_FILE" | grep -q CHANGE_ME; then
        echo "WARNING: $ENV_FILE still holds CHANGE_ME placeholders; the services will refuse to start until they are replaced (radar-db keygen)." >&2
    fi
else
    view_key=$("$BIN_DIR/radar-db" keygen)
    ingest_key=$("$BIN_DIR/radar-db" keygen)
    [ -n "$view_key" ] && [ -n "$ingest_key" ] || die "radar-db keygen produced no key"
    tmp=$(mktemp "$ETC_DIR/.radar.env.XXXXXX")
    # The example file holds placeholders only; the real keys exist just on this host.
    grep -q CHANGE_ME_VIEW_KEY "$src/radar.env.example" && grep -q CHANGE_ME_INGEST_KEY "$src/radar.env.example" || die "radar.env.example lacks the CHANGE_ME_* key placeholders"
    sed -e "s/CHANGE_ME_VIEW_KEY/$view_key/g" -e "s/CHANGE_ME_INGEST_KEY/$ingest_key/g" "$src/radar.env.example" >"$tmp"
    chown root:"$SVC_USER" "$tmp"
    chmod 0640 "$tmp"
    mv -f "$tmp" "$ENV_FILE"
    new_keys=yes
fi
install -m 0644 "$src/radar.env.example" "$ETC_DIR/radar.env.example"
for doc in README-xubuntu.md backup.cron ufw-setup.sh verify-hardening.sh; do
    if [ -f "$src/$doc" ]; then install -m 0644 "$src/$doc" "$DOC_DIR/$doc"; fi
done

# systemd units.
if ! command -v systemctl >/dev/null 2>&1; then
    echo "systemctl not found: binaries and settings are installed, but no services were set up." >&2
    exit 0
fi
install -m 0644 "$src/radar-db.service" "$UNIT_DIR/radar-db.service"
install -m 0644 "$src/radar-ingest.service" "$UNIT_DIR/radar-ingest.service"
systemctl daemon-reload
systemctl enable radar-db.service radar-ingest.service >/dev/null 2>&1
if [ "$start" = yes ]; then
    systemctl restart radar-db.service radar-ingest.service
fi

# Optional: firewall. ufw-setup.sh validates the network and never opens 8788/8789.
if [ -n "$ufw_cidr" ]; then
    echo "configuring ufw for $ufw_cidr"
    if [ "$ufw_tunnel" = yes ]; then
        sh "$src/ufw-setup.sh" --tunnel "$ufw_cidr"
    else
        sh "$src/ufw-setup.sh" "$ufw_cidr"
    fi
fi

# Optional: fail2ban filter + jail. An edited jail is never overwritten; the new one is written next to it as .new.
if [ "$with_f2b" = yes ]; then
    command -v fail2ban-client >/dev/null 2>&1 || die "fail2ban is not installed (sudo apt install fail2ban), then run install.sh --with-fail2ban again"
    install -d -m 0755 /etc/fail2ban/filter.d /etc/fail2ban/jail.d
    install -m 0644 "$src/fail2ban/radar-live.conf" /etc/fail2ban/filter.d/radar-live.conf
    jail=/etc/fail2ban/jail.d/radar-live.local
    if [ -f "$jail" ] && ! cmp -s "$src/fail2ban/jail.local" "$jail"; then
        install -m 0644 "$src/fail2ban/jail.local" "$jail.new"
        echo "kept your edited $jail; the shipped version is $jail.new"
    else
        install -m 0644 "$src/fail2ban/jail.local" "$jail"
    fi
    systemctl reload fail2ban >/dev/null 2>&1 || systemctl restart fail2ban
fi

ip=$(hostname -I 2>/dev/null | awk '{print $1}')
[ -n "$ip" ] || ip=localhost
echo
echo "radar-live is installed."
echo "  page:     http://$ip:8787/"
echo "  status:   systemctl status radar-db radar-ingest"
echo "  logs:     journalctl -u radar-db -u radar-ingest -f"
echo "  settings: $ENV_FILE"
echo "  audit:    sudo sh $DOC_DIR/verify-hardening.sh"
if [ "$new_keys" = yes ]; then
    echo
    echo "Keys were generated just now on this host. They are shown only here; they are also stored in $ENV_FILE"
    echo "(readable by root and group $SVC_USER only)."
    echo "  view key (type it into the page):        $view_key"
    echo "  ingest key (give it to radar-feed):      $ingest_key"
fi
