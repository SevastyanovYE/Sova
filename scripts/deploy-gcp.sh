#!/usr/bin/env bash
set -Eeuo pipefail

if [[ "$#" -ne 4 ]]; then
  echo "Usage: bash deploy-gcp.sh ARTIFACT SHA256 VERSION COMMIT" >&2
  exit 1
fi
sova_artifact="$1"
sova_expected_sha="$2"
sova_expected_version="$3"
sova_expected_commit="$4"
if [[ ! "$sova_expected_sha" =~ ^[0-9a-f]{64}$ || ! "$sova_expected_commit" =~ ^[0-9a-f]{40}$ || ! "$sova_expected_version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "Invalid checksum, version, or commit" >&2
  exit 1
fi
sova_database="/var/lib/sova/sova.db"
sova_binary="/opt/sova/sova"
sova_service="sova.service"
sova_env="/etc/sova/sova.env"

for sova_command in sha256sum sqlite3 sudo systemctl; do
  if ! command -v "$sova_command" >/dev/null 2>&1; then
    echo "Missing required command: $sova_command" >&2
    exit 1
  fi
done

if [[ ! -f "$sova_artifact" ]]; then
  echo "Artifact not found: $sova_artifact" >&2
  exit 1
fi

sova_actual_sha="$(sha256sum "$sova_artifact" | awk '{print $1}')"
if [[ "$sova_actual_sha" != "$sova_expected_sha" ]]; then
  echo "Artifact checksum mismatch: got $sova_actual_sha" >&2
  exit 1
fi

if [[ "$(uname -m)" != "x86_64" ]]; then
  echo "Unexpected server architecture: $(uname -m); expected x86_64" >&2
  exit 1
fi

if [[ "$(sudo systemctl is-active "$sova_service")" != "active" ]]; then
  echo "$sova_service is not active before update; stopping for manual inspection" >&2
  exit 1
fi

sova_db_check="$(sudo -u sova sqlite3 "$sova_database" 'PRAGMA quick_check;')"
if [[ "$sova_db_check" != "ok" ]]; then
  echo "Production database quick_check failed before update: $sova_db_check" >&2
  exit 1
fi

sova_previous_version_output="$(sudo -u sova sh -lc "set -a; . '$sova_env'; set +a; cd /opt/sova; '$sova_binary' version")"
if [[ -z "$sova_previous_version_output" ]]; then
  echo "Could not read the currently installed Sova version" >&2
  exit 1
fi

sova_stamp="$(date -u +%Y%m%dT%H%M%SZ)"
sova_journal_since="$(date -u '+%Y-%m-%d %H:%M:%S UTC')"
sova_db_backup="/var/backups/sova/sova-before-${sova_expected_version}-${sova_stamp}.sqlite"
sova_binary_backup="/var/backups/sova/sova-binary-before-${sova_expected_version}-${sova_stamp}"
sova_rollback_needed=0

sova_on_exit() {
  sova_exit_code=$?
  trap - EXIT
  if [[ "$sova_exit_code" -ne 0 && "$sova_rollback_needed" -eq 1 ]]; then
    set +e
    echo "Update failed; restoring the previous binary and starting $sova_service" >&2
    sudo systemctl stop "$sova_service"
    sudo install -o root -g root -m 0755 "$sova_binary_backup" "$sova_binary"
    sudo systemctl start "$sova_service"
    sudo systemctl is-active "$sova_service"
    sova_restored_version_output="$(sudo -u sova sh -lc "set -a; . '$sova_env'; set +a; cd /opt/sova; '$sova_binary' version")"
    if [[ "$sova_restored_version_output" != "$sova_previous_version_output" ]]; then
      echo "Rollback version verification failed: $sova_restored_version_output" >&2
    else
      echo "Rollback restored: $sova_restored_version_output" >&2
    fi
    sudo -u sova sh -lc "set -a; . '$sova_env'; set +a; cd /opt/sova; '$sova_binary' healthcheck"
  fi
  exit "$sova_exit_code"
}
trap sova_on_exit EXIT

sudo install -d -o sova -g sova -m 0750 /var/backups/sova
sudo install -o root -g root -m 0755 "$sova_binary" "$sova_binary_backup"
sova_rollback_needed=1

echo "Stopping $sova_service"
sudo systemctl stop "$sova_service"

sudo -u sova sqlite3 "$sova_database" 'PRAGMA wal_checkpoint(TRUNCATE);'
sudo -u sova sqlite3 "$sova_database" ".backup '$sova_db_backup'"
sova_backup_check="$(sudo -u sova sqlite3 "$sova_db_backup" 'PRAGMA quick_check;')"
if [[ "$sova_backup_check" != "ok" ]]; then
  echo "Backup quick_check failed: $sova_backup_check" >&2
  exit 1
fi
sudo sha256sum "$sova_db_backup"

echo "Installing Sova $sova_expected_version ($sova_expected_commit)"
sudo install -o root -g root -m 0755 "$sova_artifact" "$sova_binary"
sudo -u sova sh -lc "set -a; . '$sova_env'; set +a; cd /opt/sova; '$sova_binary' init"
sudo -u sova sh -lc "set -a; . '$sova_env'; set +a; cd /opt/sova; '$sova_binary' workspace seed-document-indexes --type task"
sudo systemctl start "$sova_service"

sudo systemctl is-active "$sova_service"
sova_version_output="$(sudo -u sova sh -lc "set -a; . '$sova_env'; set +a; cd /opt/sova; '$sova_binary' version")"
if [[ "$sova_version_output" != "sova $sova_expected_version ($sova_expected_commit)" ]]; then
  echo "Unexpected installed version: $sova_version_output" >&2
  exit 1
fi

sudo -u sova sh -lc "set -a; . '$sova_env'; set +a; cd /opt/sova; '$sova_binary' healthcheck"
sudo -u sova sh -lc "set -a; . '$sova_env'; set +a; cd /opt/sova; '$sova_binary' doctor --strict"
sudo -u sova sh -lc "set -a; . '$sova_env'; set +a; cd /opt/sova; '$sova_binary' workspace doctor --strict"
sova_smoke_ok=0
for sova_smoke_attempt in 1 2 3; do
  echo "Running model smoke attempt $sova_smoke_attempt/3"
  if sudo -u sova sh -lc "set -a; . '$sova_env'; set +a; cd /opt/sova; '$sova_binary' model-smoke"; then
    sova_smoke_ok=1
    break
  fi
done
if [[ "$sova_smoke_ok" -ne 1 ]]; then
  echo "Model smoke failed after three attempts" >&2
  exit 1
fi
sudo systemctl show "$sova_service" -p ActiveState -p SubState -p NRestarts -p MemoryCurrent -p MemoryMax
sudo journalctl -u "$sova_service" --since "$sova_journal_since" --no-pager -n 120

sudo -u sova sh -lc "set -a; . '$sova_env'; set +a; cd /opt/sova; '$sova_binary' healthcheck"
sova_final_db_check="$(sudo -u sova sqlite3 "$sova_database" 'PRAGMA quick_check;')"
if [[ "$sova_final_db_check" != "ok" ]]; then
  echo "Database quick_check failed after update: $sova_final_db_check" >&2
  exit 1
fi
sova_journal_errors="$(sudo journalctl -u "$sova_service" --since "$sova_journal_since" --priority err --no-pager --quiet)"
if [[ -n "$sova_journal_errors" ]]; then
  echo "$sova_journal_errors" >&2
  echo "Error-level service journal entries found; rolling back" >&2
  exit 1
fi
sova_rollback_needed=0
trap - EXIT

echo "Deployment complete"
echo "Version: $sova_version_output"
echo "Database backup: $sova_db_backup"
echo "Previous binary: $sova_binary_backup"
