#!/bin/sh
# Quick secret scan for pre-commit use (task 8.13, docs/security/secrets-policy.md). Same rules as the
# Go test internal/mod/guard/secrets_test.go, with grep-like logic; the Go test is the authority.
#   sh scripts/scan-secrets.sh        scans the files git would commit (tracked + untracked, not ignored)
# Exit 0 = clean, 1 = findings, 2 = cannot run. Known fakes: docs/security/secrets-allowlist.txt.
# Pre-commit hook:  printf '#!/bin/sh\nexec sh scripts/scan-secrets.sh\n' > .git/hooks/pre-commit
set -u
cd "$(dirname "$0")/.." || exit 2
ALLOW=docs/security/secrets-allowlist.txt
[ -f "$ALLOW" ] || { echo "scan-secrets: missing $ALLOW" >&2; exit 2; }
command -v git >/dev/null 2>&1 || { echo "scan-secrets: git is required" >&2; exit 2; }

PLACEHOLDER='change_me|changeme|test|fake|example|dummy|placeholder|sample|xxxx|0000|abcdef|1234|your|<|\$|\{\{|todo|none|redacted'
GENERIC='^(you|name|user|username|<you>|public|default|%username%|\$env:username|radar|me|example|foo|yourname|your-name|someone)$'
files=$(mktemp)
hits=$(mktemp)
trap 'rm -f "$files" "$hits"' EXIT INT TERM
git ls-files -c -o --exclude-standard | grep -vE '(^|/)(\.git|node_modules|dist|maps)/' >"$files" || true

# allowed RULE PATH: true when the allowlist covers it.
allowed() {
    while read -r arule aglob _; do
        case $arule in '' | \#*) continue ;; esac
        [ "$arule" = "*" ] || [ "$arule" = "$1" ] || continue
        case $aglob in */\*\*) aglob="${aglob%\*\*}*" ;; esac
        # shellcheck disable=SC2254
        case $2 in $aglob) return 0 ;; esac
    done <"$ALLOW"
    return 1
}

# report RULE REGEX [FILTER_OUT_REGEX]: matches of REGEX per file, minus FILTER_OUT_REGEX, minus the allowlist.
report() {
    rule=$1 regex=$2 drop=${3:-} mix=${4:-}
    while read -r f; do
        [ -f "$f" ] || continue
        grep -noIE -e "$regex" -- "$f" 2>/dev/null | while IFS= read -r m; do
            val=${m#*:}
            [ -z "$drop" ] || ! printf '%s\n' "$val" | grep -qiE "$drop" || continue
            if [ -n "$mix" ]; then
                v=${val#*[:=]}
                printf '%s\n' "$v" | grep -q '[0-9]' && printf '%s\n' "$v" | grep -q '[A-Za-z]' || continue
            fi
            allowed "$rule" "$f" && continue
            echo "$rule: $f:${m%%:*}: $(printf '%s' "$val" | cut -c1-6)..." >>"$hits"
        done
    done <"$files"
}

# tracked file names that must never be committed
while read -r f; do
    base=${f##*/}
    case $base in
        *.example) ;;
        .env | .env.* | *.env | *.key | *.pem | *.p12 | *.db | *.db-wal | *.db-shm | *.sqlite)
            allowed file "$f" || echo "file: $f" >>"$hits"
            ;;
    esac
done <"$files"

report pem '-----BEGIN [A-Z0-9 ]*PRIVATE KEY'
report aws '\b(AKIA|ASIA|AGPA|AIDA)[0-9A-Z]{16}\b'
report bearer 'Bearer[[:space:]]+[A-Za-z0-9._~+/=-]{20,}' "$PLACEHOLDER" mix
report token "(key|secret|token|passw(or)?d|credential)[A-Za-z0-9_.-]*[\"']?[[:space:]]*(:=|[:=])[[:space:]]*[\"'\`]?[A-Za-z0-9_+/=.-]{20,}" "$PLACEHOLDER" mix
report email '[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}' '@(example\.(com|org|net)|localhost|[a-z0-9.-]*\.(invalid|example|test))$'
report ipv4 '(^|[^0-9A-Za-z.])[0-9]{1,3}(\.[0-9]{1,3}){3}([^0-9.]|$)' '(^|[^0-9])(0|10|127|2[2-5][0-9]|22[4-9]|23[0-9])\.[0-9.]+|(^|[^0-9])(172\.(1[6-9]|2[0-9]|3[01])|192\.168|169\.254|100\.(6[4-9]|[7-9][0-9]|1[01][0-9]|12[0-7]))\.[0-9.]+|192\.0\.2\.|198\.51\.100\.|203\.0\.113\.|\.[0-9]{4,}'
report path '[A-Za-z]:(\\?|/)Users(\\?|/)[^\/"'"'"'<>*?| ]+' 'Users.{1,2}(<you>|you|name|user|username|public|default|%username%|\$env:username|someone)$'
report path '(^|[ "(=])/(Users|home)/[A-Za-z0-9._-]+/' '/(Users|home)/(you|name|user|username|public|default|radar|me|example|foo|yourname|someone)/'

if [ -s "$hits" ]; then
    sort -u "$hits"
    echo "scan-secrets: findings above. Fix them, or allowlist a known fake with a reason in $ALLOW" >&2
    exit 1
fi
echo "scan-secrets: clean"
