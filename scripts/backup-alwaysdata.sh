#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Usage: scripts/backup-alwaysdata.sh [--root PATH] [--database PATH]
       [--prune --keep N]

Creates a consistent, compressed SQLite backup. It performs quick_check,
checkpoints only when the source actually uses WAL, runs online .backup, then
logically restores into another temporary file and checks it. On alwaysdata the
configured mode is DELETE, so no WAL checkpoint is assumed. Old backups are
deleted only when the explicit --prune flag is supplied.
USAGE
}

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
default_root="$(cd "$script_dir/.." && pwd -P)"
root="$default_root"
database=""
prune=false
keep=3

while (($#)); do
  case "$1" in
    --root) [[ $# -ge 2 ]] || exit 64; root="$2"; shift 2 ;;
    --database) [[ $# -ge 2 ]] || exit 64; database="$2"; shift 2 ;;
    --prune) prune=true; shift ;;
    --keep) [[ $# -ge 2 ]] || exit 64; keep="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "error: unknown argument: $1" >&2; usage >&2; exit 64 ;;
  esac
done

[[ "$root" == /* ]] || { echo "error: --root must be absolute" >&2; exit 64; }
[[ "$keep" =~ ^[0-9]+$ ]] && ((keep >= 1)) || { echo "error: --keep must be at least 1" >&2; exit 64; }
database="${database:-${SOVA_DATABASE_PATH:-$root/data/state/sova.db}}"
[[ "$database" == /* ]] || { echo "error: database path must be absolute" >&2; exit 64; }
[[ -f "$database" ]] || { echo "error: database not found: $database" >&2; exit 66; }

for command_name in sqlite3 gzip; do
  command -v "$command_name" >/dev/null 2>&1 || { echo "error: missing command: $command_name" >&2; exit 69; }
done

backups="$root/backups"
mkdir -p "$backups"
chmod 0700 "$backups"
stamp="$(date -u +%Y%m%dT%H%M%SZ)-$$"
working="$backups/.sova-$stamp.sqlite.tmp"
restore_test="$backups/.restore-test-$stamp.sqlite.tmp"
compressed_tmp="$backups/.sova-$stamp.sqlite.gz.tmp"
final="$backups/sova-$stamp.sqlite.gz"
trap 'rm -f "$working" "$working-journal" "$working-wal" "$working-shm" "$restore_test" "$restore_test-journal" "$restore_test-wal" "$restore_test-shm" "$compressed_tmp"' EXIT

quick="$(sqlite3 -batch "$database" 'PRAGMA quick_check;')"
[[ "$quick" == "ok" ]] || { echo "error: source SQLite quick_check failed" >&2; exit 70; }

journal_mode="$(sqlite3 -batch "$database" 'PRAGMA journal_mode;' | tr '[:upper:]' '[:lower:]')"
case "$journal_mode" in
  wal)
    checkpoint="$(sqlite3 -batch "$database" <<'SQL'
.timeout 15000
PRAGMA wal_checkpoint(TRUNCATE);
SQL
)"
    case "$checkpoint" in
      0\|*) ;;
      *) echo "error: WAL checkpoint was busy or failed: $checkpoint" >&2; exit 70 ;;
    esac
    ;;
  delete|truncate) ;;
  *) echo "error: unsupported SQLite journal mode for backup: $journal_mode" >&2; exit 70 ;;
esac

case "$working" in *"'"*|*$'\n'*) echo "error: unsupported backup path" >&2; exit 64;; esac
sqlite3 -batch "$database" ".timeout 15000" ".backup '$working'"
[[ "$(sqlite3 -batch "$working" 'PRAGMA quick_check;')" == "ok" ]] || {
  echo "error: backup SQLite quick_check failed" >&2
  exit 70
}

case "$restore_test" in *"'"*|*$'\n'*) echo "error: unsupported restore-test path" >&2; exit 64;; esac
sqlite3 -batch "$working" ".backup '$restore_test'"
[[ "$(sqlite3 -batch "$restore_test" 'PRAGMA quick_check;')" == "ok" ]] || {
  echo "error: restored-copy SQLite quick_check failed" >&2
  exit 70
}

gzip -6 -c "$working" > "$compressed_tmp"
mv "$compressed_tmp" "$final"
chmod 0600 "$final"
if command -v sha256sum >/dev/null 2>&1; then
  (cd "$backups" && sha256sum "$(basename "$final")" > "$(basename "$final").sha256")
else
  (cd "$backups" && shasum -a 256 "$(basename "$final")" > "$(basename "$final").sha256")
fi
chmod 0600 "$final.sha256"

if [[ "$prune" == true ]]; then
  index=0
  while IFS= read -r old_backup; do
    index=$((index + 1))
    if ((index > keep)); then
      rm -f -- "$old_backup" "$old_backup.sha256"
    fi
  done < <(find "$backups" -maxdepth 1 -type f -name 'sova-*.sqlite.gz' -print | LC_ALL=C sort -r)
fi

echo "Verified SQLite backup: $final"
echo "Journal mode: $journal_mode; source, backup, and restored-copy quick_check: ok"
