#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Usage: scripts/smoke-alwaysdata.sh [--root PATH] [--strict-doctors] [--gemini]
       [--monitor]

Default smoke is offline and never calls Gemini, checks a live heartbeat, or
sends Telegram messages.
--strict-doctors runs both production doctors. --gemini explicitly performs
the paid/network model-smoke. --monitor runs only the cheap CLI healthcheck and
is suitable for alwaysdata's optional Monitoring command.
USAGE
}

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
root="$(cd "$script_dir/.." && pwd -P)"
strict_doctors=false
gemini=false
monitor=false

while (($#)); do
  case "$1" in
    --root) [[ $# -ge 2 ]] || exit 64; root="$2"; shift 2 ;;
    --strict-doctors) strict_doctors=true; shift ;;
    --gemini) gemini=true; shift ;;
    --monitor) monitor=true; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "error: unknown argument: $1" >&2; usage >&2; exit 64 ;;
  esac
done

[[ "$root" == /* ]] || { echo "error: --root must be absolute" >&2; exit 64; }
binary="$root/bin/sova"
env_file="$root/.env"
[[ -x "$binary" ]] || { echo "error: executable missing: $binary" >&2; exit 66; }
[[ -f "$env_file" ]] || { echo "error: configuration missing: $env_file" >&2; exit 66; }

cd "$root"
if [[ "$monitor" == true ]]; then
  exec "$binary" healthcheck
fi

required_keys=(
  SOVA_STATE_DIR SOVA_DATABASE_PATH SOVA_SQLITE_JOURNAL_MODE
  SOVA_HEARTBEAT_PATH SOVA_TELEGRAM_SESSION_PATH
  SOVA_NEST_BOT_TOKEN SOVA_WORKSPACE_BOT_TOKEN SOVA_GEMINI_API_KEY
  SOVA_GOOGLE_CREDENTIALS_PATH SOVA_GOOGLE_TOKEN_PATH
)
for key in "${required_keys[@]}"; do
  if ! grep -Eq "^[[:space:]]*$key=[^[:space:]].*" "$env_file"; then
    echo "error: required configuration key is missing or empty: $key" >&2
    exit 78
  fi
done
if ! grep -Eq '^[[:space:]]*SOVA_SQLITE_JOURNAL_MODE=DELETE[[:space:]]*$' "$env_file"; then
  echo "error: alwaysdata requires SOVA_SQLITE_JOURNAL_MODE=DELETE" >&2
  exit 78
fi

for directory in data/state data/sessions data/secrets backups; do
  [[ -d "$root/$directory" ]] || { echo "error: missing directory: $root/$directory" >&2; exit 66; }
done

database_path="$(sed -n 's/^[[:space:]]*SOVA_DATABASE_PATH=//p' "$env_file" | tail -n 1)"
if [[ "$database_path" != "$root/data/state/sova.db" ]]; then
  echo "error: SOVA_DATABASE_PATH must be $root/data/state/sova.db for this alwaysdata layout" >&2
  exit 78
fi
[[ -f "$database_path" ]] || {
  echo "error: existing migrated database is missing: $database_path" >&2
  echo "Refusing to create an empty database during deployment smoke." >&2
  exit 66
}

"$binary" version
"$binary" doctor
"$binary" init

if [[ "$strict_doctors" == true ]]; then
  "$binary" doctor --strict
  "$binary" workspace doctor --strict
fi
if [[ "$gemini" == true ]]; then
  "$binary" model-smoke
fi

echo "alwaysdata smoke: ok"
