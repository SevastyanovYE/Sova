#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Usage: scripts/prepare-alwaysdata-env.sh --source OLD_ENV --output NEW_ENV
       --account ALWAYS_DATA_ACCOUNT [--overwrite]

Creates an alwaysdata .env from an existing Sova environment without printing
secret values. It removes obsolete Ollama/Codex settings and rewrites only
runtime paths plus the SQLite/heartbeat settings required by the deployment.
The output is written atomically with mode 0600.
USAGE
}

source_env=""
output_env=""
account=""
overwrite=false

while (($#)); do
  case "$1" in
    --source) [[ $# -ge 2 ]] || exit 64; source_env="$2"; shift 2 ;;
    --output) [[ $# -ge 2 ]] || exit 64; output_env="$2"; shift 2 ;;
    --account) [[ $# -ge 2 ]] || exit 64; account="$2"; shift 2 ;;
    --overwrite) overwrite=true; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "error: unknown argument: $1" >&2; usage >&2; exit 64 ;;
  esac
done

[[ -f "$source_env" ]] || { echo "error: source env not found" >&2; exit 66; }
[[ -n "$output_env" ]] || { echo "error: --output is required" >&2; exit 64; }
[[ "$account" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]] || { echo "error: invalid account name" >&2; exit 64; }
if [[ -e "$output_env" && "$overwrite" != true ]]; then
  echo "error: output already exists; pass --overwrite to replace it" >&2
  exit 73
fi

output_dir="$(dirname "$output_env")"
umask 077
mkdir -p "$output_dir"
temporary="$output_dir/.sova-env.new.$$"
trap 'rm -f "$temporary"' EXIT
root="/home/$account/sova"

awk -v root="$root" '
BEGIN {
  seen_state = seen_database = seen_journal = seen_heartbeat = 0
  seen_session = seen_credentials = seen_token = 0
}
/^[[:space:]]*(SOVA_OLLAMA_URL|SOVA_OLLAMA_MODEL|SOVA_CODEX_PATH)=/ { next }
/^[[:space:]]*SOVA_STATE_DIR=/ {
  print "SOVA_STATE_DIR=" root "/data/state"; seen_state = 1; next
}
/^[[:space:]]*SOVA_DATABASE_PATH=/ {
  print "SOVA_DATABASE_PATH=" root "/data/state/sova.db"; seen_database = 1; next
}
/^[[:space:]]*SOVA_SQLITE_JOURNAL_MODE=/ {
  print "SOVA_SQLITE_JOURNAL_MODE=DELETE"; seen_journal = 1; next
}
/^[[:space:]]*SOVA_HEARTBEAT_PATH=/ {
  print "SOVA_HEARTBEAT_PATH=" root "/data/state/health/heartbeat.json"; seen_heartbeat = 1; next
}
/^[[:space:]]*SOVA_TELEGRAM_SESSION_PATH=/ {
  print "SOVA_TELEGRAM_SESSION_PATH=" root "/data/sessions/sova-user.json"; seen_session = 1; next
}
/^[[:space:]]*SOVA_GOOGLE_CREDENTIALS_PATH=/ {
  print "SOVA_GOOGLE_CREDENTIALS_PATH=" root "/data/secrets/google-calendar-client.json"; seen_credentials = 1; next
}
/^[[:space:]]*SOVA_GOOGLE_TOKEN_PATH=/ {
  print "SOVA_GOOGLE_TOKEN_PATH=" root "/data/secrets/google-calendar-token.json"; seen_token = 1; next
}
{ print }
END {
  if (!seen_state) print "SOVA_STATE_DIR=" root "/data/state"
  if (!seen_database) print "SOVA_DATABASE_PATH=" root "/data/state/sova.db"
  if (!seen_journal) print "SOVA_SQLITE_JOURNAL_MODE=DELETE"
  if (!seen_heartbeat) print "SOVA_HEARTBEAT_PATH=" root "/data/state/health/heartbeat.json"
  if (!seen_session) print "SOVA_TELEGRAM_SESSION_PATH=" root "/data/sessions/sova-user.json"
  if (!seen_credentials) print "SOVA_GOOGLE_CREDENTIALS_PATH=" root "/data/secrets/google-calendar-client.json"
  if (!seen_token) print "SOVA_GOOGLE_TOKEN_PATH=" root "/data/secrets/google-calendar-token.json"
}
' "$source_env" > "$temporary"

chmod 0600 "$temporary"
mv "$temporary" "$output_env"
trap - EXIT
echo "Prepared protected alwaysdata env: $output_env"
