#!/bin/sh
# Read-only audit of a radar-live host. Run ON the laptop, as root for the complete picture:
#     sudo sh verify-hardening.sh [--public-ok]
# Prints PASS / FAIL / SKIP per check and exits 1 when anything FAILed (SKIP = could not be checked,
# for example the firewall without root; it does not fail the run, but fix it before you trust the result).
# It changes nothing: no restarts, no mode changes, no package or firewall commands that write.
#   --public-ok   accept web port 8787 being open to anywhere in ufw (only if you chose ufw-setup.sh --public)
# What each check means and how to fix a FAIL: docs/security/hardening.md.
set -eu

ENV_FILE=/etc/radar-live/radar.env
ETC_DIR=/etc/radar-live
SECRETS_DIR=$ETC_DIR/secrets
ARMORY_ID_FILE=$SECRETS_DIR/armory-client-id
ARMORY_KEY_FILE=$SECRETS_DIR/armory-api-key
DATA_DIR=/var/lib/radar-live
BACKUP_DIR=/var/backups/radar-live
# Key manager layout (docs/keys.md).
KEYS_DIR=/var/lib/radar-keys
KEYRING_DIR=$KEYS_DIR/keyring
USAGE_DIR=$KEYS_DIR/usage
VAULT_DIR=$KEYS_DIR/vault
LOG_DIR=/var/log/radar
SVC_USER=radar
public_ok=no

case ${1:-} in
    "") ;;
    --public-ok) public_ok=yes ;;
    -h | --help)
        sed -n '2,9p' "$0"
        exit 0
        ;;
    *)
        echo "usage: sh verify-hardening.sh [--public-ok]" >&2
        exit 2
        ;;
esac

pass_n=0
fail_n=0
skip_n=0
pass() {
    pass_n=$((pass_n + 1))
    echo "PASS $1"
}
fail() {
    fail_n=$((fail_n + 1))
    echo "FAIL $1"
}
skip() {
    skip_n=$((skip_n + 1))
    echo "SKIP $1"
}
is_root=no
if [ "$(id -u)" -eq 0 ]; then is_root=yes; fi
have() { command -v "$1" >/dev/null 2>&1; }

# ---- systemd units ----------------------------------------------------------------------------------
prop() { systemctl show -p "$2" --value "$1" 2>/dev/null || echo "?"; }

check_prop() { # unit property expected
    got=$(prop "$1" "$2")
    if [ "$got" = "$3" ]; then
        pass "$1 $2=$3"
    else
        fail "$1 $2 is '$got', expected '$3'"
    fi
}

check_limited() { # unit property: set and not unlimited
    got=$(prop "$1" "$2")
    case $got in
        "" | infinity | "?" | 18446744073709551615) fail "$1 $2 is not limited ('$got')" ;;
        *) pass "$1 $2=$got" ;;
    esac
}

if have systemctl; then
    for svc in radar-db radar-ingest; do
        unit=$svc.service
        if [ "$(systemctl is-active "$unit" 2>/dev/null || true)" = active ]; then
            pass "$unit is active"
        else
            fail "$unit is not active"
        fi
        if [ "$(systemctl is-enabled "$unit" 2>/dev/null || true)" = enabled ]; then
            pass "$unit is enabled at boot"
        else
            fail "$unit is not enabled at boot"
        fi
        for kv in NoNewPrivileges=yes ProtectSystem=strict ProtectHome=yes PrivateTmp=yes PrivateDevices=yes \
            ProtectKernelTunables=yes ProtectKernelModules=yes ProtectKernelLogs=yes ProtectControlGroups=yes \
            ProtectClock=yes ProtectHostname=yes ProtectProc=invisible ProcSubset=pid RestrictNamespaces=yes \
            RestrictRealtime=yes RestrictSUIDSGID=yes LockPersonality=yes MemoryDenyWriteExecute=yes \
            SystemCallArchitectures=native UMask=0077 User=$SVC_USER; do
            check_prop "$unit" "${kv%%=*}" "${kv#*=}"
        done
        check_prop "$unit" CapabilityBoundingSet ""
        check_prop "$unit" AmbientCapabilities ""
        check_prop "$unit" RestrictAddressFamilies "AF_INET AF_INET6 AF_UNIX"
        filter=$(prop "$unit" SystemCallFilter)
        case $filter in
            "" | "?") fail "$unit has no SystemCallFilter" ;;
            *) pass "$unit has a SystemCallFilter" ;;
        esac
        check_limited "$unit" MemoryMax
        check_limited "$unit" TasksMax
        check_limited "$unit" LimitNOFILE
        pid=$(prop "$unit" MainPID)
        if [ "$pid" != "0" ] && [ "$pid" != "?" ] && [ -d "/proc/$pid" ]; then
            owner=$(ps -o user= -p "$pid" 2>/dev/null | tr -d ' ' || true)
            if [ "$owner" = "$SVC_USER" ]; then
                pass "$unit runs as $SVC_USER"
            else
                fail "$unit process runs as '$owner'"
            fi
        fi
    done
    # radar-armory (optional oneshot started by radar-armory.timer): same sandbox, writes only its log dir.
    if [ -f /etc/systemd/system/radar-armory.service ]; then
        unit=radar-armory.service
        for kv in NoNewPrivileges=yes ProtectSystem=strict ProtectHome=yes PrivateTmp=yes PrivateDevices=yes \
            ProtectKernelTunables=yes ProtectKernelModules=yes ProtectKernelLogs=yes ProtectControlGroups=yes \
            ProtectClock=yes ProtectHostname=yes ProtectProc=invisible ProcSubset=pid RestrictNamespaces=yes \
            RestrictRealtime=yes RestrictSUIDSGID=yes LockPersonality=yes MemoryDenyWriteExecute=yes \
            SystemCallArchitectures=native UMask=0077 User=$SVC_USER; do
            check_prop "$unit" "${kv%%=*}" "${kv#*=}"
        done
        check_prop "$unit" CapabilityBoundingSet ""
        check_prop "$unit" AmbientCapabilities ""
        check_prop "$unit" RestrictAddressFamilies "AF_INET AF_INET6 AF_UNIX"
        check_limited "$unit" MemoryMax
        check_limited "$unit" TasksMax
        rw=$(prop "$unit" ReadWritePaths)
        if [ -z "$rw" ]; then pass "radar-armory has no writable path"; else fail "radar-armory ReadWritePaths='$rw'"; fi
        if [ "$(systemctl is-enabled radar-armory.timer 2>/dev/null || true)" = enabled ]; then
            pass "radar-armory.timer is enabled"
            check_prop radar-armory.timer Unit radar-armory.service
            cal=$(systemctl show -p TimersCalendar --value radar-armory.timer 2>/dev/null || true)
            case $cal in
                *"00:00:00"*) pass "radar-armory.timer OnCalendar is midnight (systemctl list-timers radar-armory.timer shows the next run)" ;;
                *) fail "radar-armory.timer OnCalendar is not midnight ('$cal')" ;;
            esac
            if [ -f "$ARMORY_ID_FILE" ]; then pass "armory timer has its client id file"; else fail "armory timer is enabled but $ARMORY_ID_FILE is missing"; fi
            if [ -f "$ARMORY_KEY_FILE" ]; then pass "armory timer has its client secret file"; else fail "armory timer is enabled but $ARMORY_KEY_FILE is missing"; fi
        else
            skip "radar-armory.timer is not enabled (armory sync is optional: docs/armory.md)"
        fi
    else
        skip "radar-armory.service is not installed"
    fi
    # radar-ingest may write only the per-key usage directory (keyusage.json); radar-db owns the data directory.
    rw=$(prop radar-ingest.service ReadWritePaths)
    if [ "$rw" = "$USAGE_DIR" ]; then pass "radar-ingest writes only $USAGE_DIR"; else fail "radar-ingest ReadWritePaths='$rw', expected only $USAGE_DIR"; fi
else
    fail "systemctl not found (is this the Xubuntu laptop?)"
fi

shell=$(getent passwd "$SVC_USER" 2>/dev/null | cut -d: -f7 || true)
case $shell in
    */nologin | */false) pass "user $SVC_USER has no login shell" ;;
    "") fail "user $SVC_USER does not exist" ;;
    *) fail "user $SVC_USER has login shell $shell" ;;
esac

# ---- settings file -----------------------------------------------------------------------------------
envval() { # NAME -> value of the last uncommented NAME=value line
    sed -n "s/^$1=//p" "$ENV_FILE" 2>/dev/null | tail -n 1
}
check_stat() { # path "mode owner group" label
    if [ ! -e "$1" ]; then
        fail "$3: $1 is missing"
        return
    fi
    got=$(stat -c '%a %U %G' "$1")
    if [ "$got" = "$2" ]; then pass "$3: $1 is $2"; else fail "$3: $1 is '$got', expected '$2'"; fi
}
check_stat "$ETC_DIR" "750 root $SVC_USER" "settings dir"
check_stat "$ENV_FILE" "640 root $SVC_USER" "env file"
check_stat "$DATA_DIR" "700 $SVC_USER $SVC_USER" "data dir"
check_stat "$BACKUP_DIR" "700 $SVC_USER $SVC_USER" "backup dir"
check_stat "$LOG_DIR" "700 $SVC_USER $SVC_USER" "log dir"
for bin in /usr/local/bin/radar-db /usr/local/bin/radar-ingest; do
    check_stat "$bin" "755 root root" "binary"
done
if [ -e /usr/local/bin/radar-armory ]; then check_stat /usr/local/bin/radar-armory "755 root root" "binary"; fi
check_stat /usr/local/bin/radar-keys "755 root root" "binary"
# Key manager: the service user reads the keyring and writes only the usage file; the vault and its sealing key are root only.
check_stat "$KEYS_DIR" "755 root root" "key manager dir"
check_stat "$KEYRING_DIR" "750 root $SVC_USER" "keyring dir"
check_stat "$KEYRING_DIR/keyring.json" "640 root $SVC_USER" "keyring file"
check_stat "$USAGE_DIR" "700 $SVC_USER $SVC_USER" "key usage dir"
check_stat "$VAULT_DIR" "700 root root" "key vault dir"
for f in "$VAULT_DIR/keyvault.bin" "$VAULT_DIR/keyvault.key"; do
    if [ -e "$f" ]; then check_stat "$f" "600 root root" "key vault file"; fi
done
if [ -e "$USAGE_DIR/keyusage.json" ]; then check_stat "$USAGE_DIR/keyusage.json" "600 $SVC_USER $SVC_USER" "key usage file"; fi
if [ -e "$BACKUP_DIR/keys" ]; then check_stat "$BACKUP_DIR/keys" "700 root root" "key manager backup dir"; fi
# The sealing key must never be in a backup.
if ls "$BACKUP_DIR"/keys/keyvault.key* >/dev/null 2>&1; then fail "a copy of the vault sealing key is in $BACKUP_DIR/keys"; else pass "no vault sealing key in the backups"; fi
if [ -r "$ENV_FILE" ]; then
    for name in RADAR_KEYRING_FILE RADAR_KEYUSAGE_FILE; do
        if [ -n "$(envval "$name")" ]; then pass "$name is set"; else fail "$name is not set in $ENV_FILE (radar-keys changes would not reach radar-ingest)"; fi
    done
    for name in RADAR_KEYVAULT_FILE RADAR_KEYVAULT_KEY_FILE; do
        if [ -n "$(envval "$name")" ]; then fail "$name is set in $ENV_FILE, which the service user can read"; else pass "$name is not in the service's settings"; fi
    done
fi
# Armory client id and secret files: only mode and owner are checked, the files are never read.
if [ -d "$SECRETS_DIR" ]; then check_stat "$SECRETS_DIR" "750 root $SVC_USER" "secrets dir"; fi
if [ -e "$ARMORY_KEY_FILE" ]; then check_stat "$ARMORY_KEY_FILE" "640 root $SVC_USER" "armory client secret file"; fi
if [ -e "$ARMORY_ID_FILE" ]; then check_stat "$ARMORY_ID_FILE" "640 root $SVC_USER" "armory client id file"; fi
for f in "$DATA_DIR"/*.db "$DATA_DIR"/abuse-bans.json "$LOG_DIR"/security*.log; do
    [ -e "$f" ] || continue
    mode=$(stat -c '%a' "$f")
    if [ "$mode" = 600 ]; then pass "$f is 600"; else fail "$f is $mode, expected 600"; fi
done

listen=:8787
if [ -r "$ENV_FILE" ]; then
    if grep -v '^[[:space:]]*#' "$ENV_FILE" | grep -q 'CHANGE_ME'; then
        fail "env file still holds a CHANGE_ME placeholder"
    else
        pass "env file has no CHANGE_ME placeholder"
    fi
    for name in RADAR_INGEST_KEYS RADAR_DB_KEY RADAR_VIEW_KEYS; do
        v=$(envval "$name")
        if [ "${#v}" -ge 16 ]; then pass "$name is set (length ${#v})"; else fail "$name is missing or shorter than 16 characters"; fi
    done
    v=$(envval RADAR_DB_LISTEN)
    if [ -n "$v" ]; then listen=$v; fi
    req=$(envval RADAR_REQUIRE_KEYS)
    if [ -z "$req" ]; then req=auto; fi
    case $req in
        true) pass "RADAR_REQUIRE_KEYS=true" ;;
        auto)
            case $listen in
                127.0.0.1:* | "[::1]:"*) pass "RADAR_REQUIRE_KEYS=auto with a loopback listener" ;;
                *) pass "RADAR_REQUIRE_KEYS=auto: effectively true, listener $listen is not loopback (true is more explicit)" ;;
            esac
            ;;
        *) fail "RADAR_REQUIRE_KEYS=$req allows an open API" ;;
    esac
    case $listen in
        127.0.0.1:* | "[::1]:"*)
            # Behind a proxy or tunnel on this machine.
            if [ "$(envval RADAR_HEADERS_HSTS)" = true ]; then pass "HSTS on (RADAR_HEADERS_HSTS=true)"; else fail "loopback listener (proxy mode) but RADAR_HEADERS_HSTS is not true"; fi
            tp=$(envval RADAR_RATELIMIT_TRUSTED_PROXIES)
            case $tp in
                *127.0.0.1*) pass "tunnel/proxy trusted for client addresses ($tp)" ;;
                *) fail "loopback listener (proxy mode) but RADAR_RATELIMIT_TRUSTED_PROXIES does not trust 127.0.0.1" ;;
            esac
            ;;
        *)
            if [ -n "$(envval RADAR_RATELIMIT_TRUSTED_PROXIES)" ]; then
                fail "RADAR_RATELIMIT_TRUSTED_PROXIES is set but the web port is reachable directly (headers could be spoofed)"
            else
                pass "no proxy trusted while the web port is directly reachable"
            fi
            ;;
    esac
else
    skip "env file $ENV_FILE not readable (run as root)"
fi

# ---- listeners ----------------------------------------------------------------------------------------
if have ss; then
    listeners=$(ss -ltn 2>/dev/null | awk 'NR > 1 {print $4}')
    addrs_for() { printf '%s\n' "$listeners" | grep -E ":$1\$" || true; }
    is_loopback() { case $1 in 127.0.0.1:* | "[::1]:"*) return 0 ;; *) return 1 ;; esac; }
    for port in 8788 8789; do
        found=$(addrs_for "$port")
        if [ -z "$found" ]; then
            fail "nothing listens on $port (service down?)"
            continue
        fi
        bad=""
        for a in $found; do is_loopback "$a" || bad="$bad $a"; done
        if [ -z "$bad" ]; then pass "port $port bound to loopback only"; else fail "port $port is reachable beyond loopback:$bad"; fi
    done
    found=$(addrs_for 8787)
    if [ -z "$found" ]; then
        fail "nothing listens on 8787 (radar-db down?)"
    else
        case $listen in
            127.0.0.1:* | "[::1]:"*)
                bad=""
                for a in $found; do is_loopback "$a" || bad="$bad $a"; done
                if [ -z "$bad" ]; then pass "port 8787 on loopback (proxy mode)"; else fail "RADAR_DB_LISTEN is loopback but 8787 is bound to$bad"; fi
                ;;
            *) pass "port 8787 bound to $(printf '%s' "$found" | tr '\n' ' ')(LAN mode; the firewall limits who may connect)" ;;
        esac
    fi
else
    fail "ss not found (iproute2)"
fi
if have curl; then
    code=$(curl -s -o /dev/null -m 5 -w '%{http_code}' http://127.0.0.1:8787/internal/metrics 2>/dev/null || true)
    case $code in
        404 | 405 | 000) pass "/internal/metrics is not served on the public port ($code)" ;;
        *) fail "/internal/metrics answered $code on the public port" ;;
    esac
fi

# ---- firewall, fail2ban, updates, ssh ------------------------------------------------------------------
if [ "$is_root" = yes ] && have ufw; then
    ufw_out=$(ufw status verbose 2>/dev/null || true)
    case $ufw_out in
        *"Status: active"*) pass "ufw is active" ;;
        *) fail "ufw is not active" ;;
    esac
    case $ufw_out in
        *"deny (incoming)"*) pass "ufw default: deny incoming" ;;
        *) fail "ufw default for incoming is not deny" ;;
    esac
    if printf '%s\n' "$ufw_out" | grep -Eq '^(8788|8789)(/| )'; then fail "ufw has a rule for 8788/8789"; else pass "ufw has no rule for 8788/8789"; fi
    if printf '%s\n' "$ufw_out" | grep -E '^22(/tcp)? ' | grep -vq 'LIMIT'; then fail "ufw permits SSH without rate limit"; else pass "ufw SSH rules are rate limited"; fi
    if printf '%s\n' "$ufw_out" | grep -E '^22(/tcp)? ' | grep -q 'Anywhere'; then fail "ufw permits SSH from anywhere"; else pass "ufw SSH is not open to anywhere"; fi
    if printf '%s\n' "$ufw_out" | grep -E '^8787(/tcp)? ' | grep -q 'Anywhere'; then
        if [ "$public_ok" = yes ]; then pass "ufw: 8787 open to anywhere (accepted by --public-ok)"; else fail "ufw: 8787 is open to anywhere (use the LAN or tunnel mode, or pass --public-ok)"; fi
    else
        pass "ufw: 8787 is not open to anywhere"
    fi
elif [ "$is_root" = yes ]; then
    fail "ufw is not installed"
else
    skip "ufw status needs root"
fi

if [ "$is_root" = yes ] && have fail2ban-client; then
    if fail2ban-client status radar-live >/dev/null 2>&1; then pass "fail2ban jail radar-live is active"; else fail "fail2ban jail radar-live is not active"; fi
elif [ "$is_root" = yes ]; then
    fail "fail2ban is not installed"
else
    skip "fail2ban-client needs root"
fi

if have systemctl; then
    ua=$(systemctl is-enabled unattended-upgrades 2>/dev/null || true)
    if [ "$ua" = enabled ]; then pass "unattended-upgrades is enabled"; else fail "unattended-upgrades is '$ua'"; fi
fi
auto=/etc/apt/apt.conf.d/20auto-upgrades
if grep -Eq 'Unattended-Upgrade[[:space:]]+"1"' "$auto" 2>/dev/null && grep -Eq 'Update-Package-Lists[[:space:]]+"1"' "$auto" 2>/dev/null; then
    pass "$auto turns on daily package lists and unattended upgrades"
else
    fail "$auto does not enable Update-Package-Lists and Unattended-Upgrade"
fi

if [ "$is_root" = yes ] && have sshd; then
    sshd_out=$(sshd -T 2>/dev/null || true)
    for kv in passwordauthentication=no permitrootlogin=no kbdinteractiveauthentication=no; do
        if printf '%s\n' "$sshd_out" | grep -qx "$kv"; then pass "sshd $kv"; else fail "sshd is not $kv"; fi
    done
elif [ "$is_root" = yes ]; then
    skip "sshd is not installed"
else
    skip "sshd -T needs root"
fi

echo
echo "summary: $pass_n passed, $fail_n failed, $skip_n skipped"
if [ "$fail_n" -gt 0 ]; then
    exit 1
fi
