#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Usage: scripts/restore-alwaysdata.sh --source BACKUP.sqlite[.gz]
       [--root PATH] [--database PATH] [--journal-mode DELETE]
       --confirm-service-stopped --execute

Restore is intentionally gated. Stop the alwaysdata Service first. The script
validates the source and a separately restored temporary database, creates a
fresh backup of the current database, and moves the old DB/journal/WAL/SHM files into a
timestamped pre-restore directory before the atomic replacement.
The restored database is converted to DELETE mode by default for alwaysdata.
USAGE
}

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
default_root="$(cd "$script_dir/.." && pwd -P)"
root="$default_root"
database=""
source_file=""
confirmed=false
execute=false
journal_mode="${SOVA_SQLITE_JOURNAL_MODE:-DELETE}"

while (($#)); do
  case "$1" in
    --root) [[ $# -ge 2 ]] || exit 64; root="$2"; shift 2 ;;
    --database) [[ $# -ge 2 ]] || exit 64; database="$2"; shift 2 ;;
    --source) [[ $# -ge 2 ]] || exit 64; source_file="$2"; shift 2 ;;
    --journal-mode) [[ $# -ge 2 ]] || exit 64; journal_mode="$2"; shift 2 ;;
    --confirm-service-stopped) confirmed=true; shift ;;
    --execute) execute=true; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "error: unknown argument: $1" >&2; usage >&2; exit 64 ;;
  esac
done

[[ "$root" == /* ]] || { echo "error: --root must be absolute" >&2; exit 64; }
journal_mode="$(printf '%s' "$journal_mode" | tr '[:lower:]' '[:upper:]')"
case "$journal_mode" in DELETE|TRUNCATE|WAL) ;; *) echo "error: --journal-mode must be DELETE, TRUNCATE, or WAL" >&2; exit 64;; esac
database="${database:-${SOVA_DATABASE_PATH:-$root/data/state/sova.db}}"
[[ "$database" == /* ]] || { echo "error: database path must be absolute" >&2; exit 64; }
[[ -n "$source_file" && -f "$source_file" ]] || { echo "error: --source backup is required" >&2; exit 66; }
[[ "$confirmed" == true && "$execute" == true ]] || {
  echo "Refusing restore: stop the alwaysdata Service, then pass --confirm-service-stopped --execute." >&2
  exit 64
}

for command_name in sqlite3; do
  command -v "$command_name" >/dev/null 2>&1 || { echo "error: missing command: $command_name" >&2; exit 69; }
done
if [[ "$source_file" == *.gz ]]; then
  command -v gzip >/dev/null 2>&1 || { echo "error: gzip is required" >&2; exit 69; }
fi

if [[ -f "$source_file.sha256" ]]; then
  if command -v sha256sum >/dev/null 2>&1; then
    (cd "$(dirname "$source_file")" && sha256sum -c "$(basename "$source_file.sha256")" >/dev/null)
  else
    (cd "$(dirname "$source_file")" && shasum -a 256 -c "$(basename "$source_file.sha256")" >/dev/null)
  fi
fi

mkdir -p "$(dirname "$database")" "$root/backups"
chmod 0700 "$(dirname "$database")" "$root/backups"
stamp="$(date -u +%Y%m%dT%H%M%SZ)-$$"
source_tmp="$root/backups/.restore-source-$stamp.sqlite.tmp"
restored_tmp="$(dirname "$database")/.sova-restore-$stamp.sqlite.tmp"
trap 'rm -f "$source_tmp" "$source_tmp-journal" "$source_tmp-wal" "$source_tmp-shm" "$restored_tmp" "$restored_tmp-journal" "$restored_tmp-wal" "$restored_tmp-shm"' EXIT

if [[ "$source_file" == *.gz ]]; then
  gzip -dc "$source_file" > "$source_tmp"
else
  cp "$source_file" "$source_tmp"
fi
[[ "$(sqlite3 -batch "$source_tmp" 'PRAGMA quick_check;')" == "ok" ]] || {
  echo "error: source backup quick_check failed" >&2
  exit 70
}

case "$restored_tmp" in *"'"*|*$'\n'*) echo "error: unsupported restore path" >&2; exit 64;; esac
sqlite3 -batch "$source_tmp" ".backup '$restored_tmp'"
installed_mode="$(sqlite3 -batch "$restored_tmp" "PRAGMA journal_mode=$journal_mode;" | tr '[:upper:]' '[:lower:]')"
[[ "$installed_mode" == "$(printf '%s' "$journal_mode" | tr '[:upper:]' '[:lower:]')" ]] || {
  echo "error: could not set restored SQLite journal mode to $journal_mode" >&2
  exit 70
}
[[ "$(sqlite3 -batch "$restored_tmp" 'PRAGMA quick_check;')" == "ok" ]] || {
  echo "error: restored temporary database quick_check failed" >&2
  exit 70
}

if [[ -f "$database" ]]; then
  current_quick="$(sqlite3 -batch "$database" 'PRAGMA quick_check;')"
  [[ "$current_quick" == "ok" ]] || { echo "error: current database quick_check failed" >&2; exit 70; }
  current_journal="$(sqlite3 -batch "$database" 'PRAGMA journal_mode;' | tr '[:upper:]' '[:lower:]')"
  if [[ "$current_journal" == wal ]]; then
    checkpoint="$(sqlite3 -batch "$database" <<'SQL'
.timeout 15000
PRAGMA wal_checkpoint(TRUNCATE);
SQL
)"
    case "$checkpoint" in 0\|*) ;; *) echo "error: current WAL checkpoint failed: $checkpoint" >&2; exit 70;; esac
  fi
  "$script_dir/backup-alwaysdata.sh" --root "$root" --database "$database"
fi

archive="$root/backups/pre-restore-$stamp"
mkdir -p "$archive"
chmod 0700 "$archive"
for existing in "$database" "$database-journal" "$database-wal" "$database-shm"; do
  [[ -e "$existing" ]] && mv "$existing" "$archive/"
done
chmod 0600 "$restored_tmp"
mv "$restored_tmp" "$database"
trap 'rm -f "$source_tmp" "$source_tmp-journal" "$source_tmp-wal" "$source_tmp-shm"' EXIT

[[ "$(sqlite3 -batch "$database" 'PRAGMA quick_check;')" == "ok" ]] || {
  echo "error: installed database quick_check failed; old files remain in $archive" >&2
  exit 70
}
echo "SQLite restore completed and verified: $database"
echo "Installed journal mode: $installed_mode"
echo "Previous database files retained in: $archive"
echo "Restart the alwaysdata Service and run smoke-alwaysdata.sh."
