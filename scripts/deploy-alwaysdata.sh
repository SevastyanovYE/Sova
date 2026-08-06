#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Usage: scripts/deploy-alwaysdata.sh --account NAME --host HOST [--arch amd64|arm64]
       [--port 22] [--package PATH] [--cleanup-staging]

Uploads a prepared package over SSH, verifies both archive and binary checksums,
checks quota headroom, and atomically replaces only program/runtime files. It
does not upload or overwrite .env, state, sessions, or secrets. The current
binary is retained as bin/sova.previous-TIMESTAMP for rollback.

--cleanup-staging explicitly permits removal of this deployment's uploaded
archive and extraction directory after a successful installation. No existing
application data is ever deleted.
USAGE
}

repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
account=""
host=""
port=22
arch="amd64"
package=""
cleanup=false

while (($#)); do
  case "$1" in
    --account) [[ $# -ge 2 ]] || exit 64; account="$2"; shift 2 ;;
    --host) [[ $# -ge 2 ]] || exit 64; host="$2"; shift 2 ;;
    --port) [[ $# -ge 2 ]] || exit 64; port="$2"; shift 2 ;;
    --arch) [[ $# -ge 2 ]] || exit 64; arch="$2"; shift 2 ;;
    --package) [[ $# -ge 2 ]] || exit 64; package="$2"; shift 2 ;;
    --cleanup-staging) cleanup=true; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "error: unknown argument: $1" >&2; usage >&2; exit 64 ;;
  esac
done

[[ "$account" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]] || { echo "error: invalid account name" >&2; exit 64; }
[[ "$host" =~ ^[A-Za-z0-9][A-Za-z0-9.-]*$ ]] || { echo "error: invalid SSH host" >&2; exit 64; }
[[ "$port" =~ ^[0-9]+$ ]] && ((port >= 1 && port <= 65535)) || { echo "error: invalid SSH port" >&2; exit 64; }
case "$arch" in amd64|arm64) ;; *) echo "error: --arch must be amd64 or arm64" >&2; exit 64;; esac

for command_name in ssh scp tar gzip; do
  command -v "$command_name" >/dev/null 2>&1 || { echo "error: missing command: $command_name" >&2; exit 69; }
done

if [[ -z "$package" ]]; then
  package="$repo_dir/.state/build/alwaysdata/sova-alwaysdata-linux-$arch.tar.gz"
elif [[ "$package" != /* ]]; then
  package="$repo_dir/$package"
fi
[[ -f "$package" ]] || { echo "error: package not found: $package" >&2; exit 66; }

while IFS= read -r member; do
  case "$member" in
    /*|*"../"*|*/.env|*/data/*|*/.sessions/*|*/.secrets/*|*/.state/*)
      echo "error: refusing unsafe/private package member: $member" >&2
      exit 70
      ;;
  esac
done < <(tar -tzf "$package")

if command -v sha256sum >/dev/null 2>&1; then
  archive_sha="$(sha256sum "$package" | awk '{print $1}')"
else
  archive_sha="$(shasum -a 256 "$package" | awk '{print $1}')"
fi
archive_bytes="$(wc -c < "$package" | tr -d '[:space:]')"
payload_bytes="$(gzip -dc "$package" | wc -c | tr -d '[:space:]')"
binary_bytes="$(tar -xOzf "$package" sova-alwaysdata/bin/sova | wc -c | tr -d '[:space:]')"
remote_root="/home/$account/sova"
stamp="$(date -u +%Y%m%dT%H%M%SZ)-$$"
incoming="$remote_root/.incoming/sova-$stamp.tar.gz"
ssh_target="$account@$host"

echo "Checking free space on $host..."
ssh -p "$port" "$ssh_target" bash -s -- \
  "$remote_root" "$archive_bytes" "$payload_bytes" "$binary_bytes" <<'REMOTE_PREFLIGHT'
set -euo pipefail
root=$1
archive_bytes=$2
payload_bytes=$3
binary_bytes=$4
mkdir -p "$root/.incoming"
free_kib=$(df -Pk "$root" | awk 'NR==2 {print $4}')
[[ "$free_kib" =~ ^[0-9]+$ ]] || { echo "error: cannot determine remote free space" >&2; exit 1; }
# Uploaded archive + full extraction + atomic binary copy + optional rollback
# copy of the current binary + 32 MiB operating headroom.
existing_binary_bytes=0
if [[ -f "$root/bin/sova" ]]; then
  existing_binary_bytes=$(wc -c < "$root/bin/sova" | tr -d '[:space:]')
fi
required_bytes=$((archive_bytes + payload_bytes + binary_bytes + existing_binary_bytes))
required_kib=$(((required_bytes + 1023) / 1024 + 32768))
if ((free_kib < required_kib)); then
  echo "error: insufficient remote space: ${free_kib} KiB free, ${required_kib} KiB required" >&2
  exit 1
fi
sova_used_kib=$(du -sk "$root" | awk '{print $1}')
[[ "$sova_used_kib" =~ ^[0-9]+$ ]] || { echo "error: cannot determine Sova disk use" >&2; exit 1; }
# The Free plan has a 1 GiB account quota. Reserve at least 100 MiB for DB/journal,
# logs, and an emergency backup. This is a second guard; the panel's account
# quota view remains authoritative because other account data may also count.
free_plan_budget_kib=$((1024 * 1024))
reserve_kib=$((100 * 1024))
if ((sova_used_kib + required_kib > free_plan_budget_kib - reserve_kib)); then
  echo "error: deployment would exceed the conservative Free-plan Sova budget" >&2
  echo "Sova use: ${sova_used_kib} KiB; required: ${required_kib} KiB; reserved: ${reserve_kib} KiB" >&2
  exit 1
fi
echo "Filesystem free: ${free_kib} KiB; Sova use: ${sova_used_kib} KiB; deployment requirement: ${required_kib} KiB"
REMOTE_PREFLIGHT

echo "Uploading $(basename "$package")..."
scp -P "$port" "$package" "$ssh_target:$incoming"

cleanup_arg=false
[[ "$cleanup" == true ]] && cleanup_arg=true
ssh -p "$port" "$ssh_target" bash -s -- "$remote_root" "$incoming" "$archive_sha" "$stamp" "$cleanup_arg" <<'REMOTE_INSTALL'
set -euo pipefail
root=$1
incoming=$2
expected_archive_sha=$3
stamp=$4
cleanup=$5

sha_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

actual_archive_sha=$(sha_file "$incoming")
[[ "$actual_archive_sha" == "$expected_archive_sha" ]] || {
  echo "error: uploaded archive checksum mismatch" >&2
  exit 1
}

stage="$root/.incoming/extract-$stamp"
mkdir -p "$stage"
tar -xzf "$incoming" -C "$stage"
payload="$stage/sova-alwaysdata"
[[ -f "$payload/SHA256SUMS" && -x "$payload/bin/sova" ]] || {
  echo "error: incomplete deployment package" >&2
  exit 1
}
(
  cd "$payload"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum -c SHA256SUMS >/dev/null
  else
    shasum -a 256 -c SHA256SUMS >/dev/null
  fi
)

mkdir -p "$root/bin" "$root/data/state" "$root/data/sessions" \
  "$root/data/secrets" "$root/backups" "$root/scripts"
chmod 0700 "$root/data" "$root/data/state" "$root/data/sessions" \
  "$root/data/secrets" "$root/backups"

if [[ -e "$root/bin/sova" ]]; then
  cp -p "$root/bin/sova" "$root/bin/sova.previous-$stamp"
fi
install -m 0755 "$payload/bin/sova" "$root/bin/.sova.new-$stamp"
[[ "$(sha_file "$root/bin/.sova.new-$stamp")" == "$(sha_file "$payload/bin/sova")" ]] || {
  echo "error: staged binary checksum mismatch" >&2
  exit 1
}
mv "$root/bin/.sova.new-$stamp" "$root/bin/sova"

for runner in run-all.sh run-nest.sh run-workspace.sh; do
  install -m 0755 "$payload/$runner" "$root/.$runner.new-$stamp"
  mv "$root/.$runner.new-$stamp" "$root/$runner"
done
for maintenance in backup-alwaysdata.sh restore-alwaysdata.sh smoke-alwaysdata.sh; do
  install -m 0755 "$payload/scripts/$maintenance" "$root/scripts/.$maintenance.new-$stamp"
  mv "$root/scripts/.$maintenance.new-$stamp" "$root/scripts/$maintenance"
done
install -m 0644 "$payload/README.md" "$root/DEPLOYMENT.md"
install -m 0600 "$payload/env.example" "$root/.env.example"

if [[ "$cleanup" == true ]]; then
  rm -rf -- "$stage"
  rm -f -- "$incoming"
else
  echo "Staging retained (remove only after verification): $stage"
  echo "Upload retained: $incoming"
fi

echo "Installed binary SHA-256: $(sha_file "$root/bin/sova")"
echo "No .env, database, session, or secret file was read or changed."
REMOTE_INSTALL

echo "Deployment files installed atomically. Restart the service in alwaysdata only after .env/state are ready."
