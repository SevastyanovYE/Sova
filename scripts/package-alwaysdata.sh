#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Usage: scripts/package-alwaysdata.sh [--arch amd64|arm64] [--binary PATH] [--output PATH] [--allow-dirty]

Creates a deployment archive containing only the binary, launchers, runbook,
example environment, and maintenance scripts. It never includes .env, state,
sessions, secrets, raw data, logs, or the local Go build cache.
USAGE
}

repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
arch="amd64"
binary=""
output=""
allow_dirty=false

while (($#)); do
  case "$1" in
    --arch) [[ $# -ge 2 ]] || exit 64; arch="$2"; shift 2 ;;
    --binary) [[ $# -ge 2 ]] || exit 64; binary="$2"; shift 2 ;;
    --output) [[ $# -ge 2 ]] || exit 64; output="$2"; shift 2 ;;
    --allow-dirty) allow_dirty=true; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "error: unknown argument: $1" >&2; usage >&2; exit 64 ;;
  esac
done

case "$arch" in
  amd64|arm64) ;;
  *) echo "error: unsupported architecture: $arch" >&2; exit 64 ;;
esac

if [[ -z "$binary" ]]; then
  binary="$repo_dir/.state/build/alwaysdata/sova-linux-$arch"
  build_args=(--arch "$arch" --output "$binary")
  [[ "$allow_dirty" == true ]] && build_args+=(--allow-dirty)
  "$repo_dir/scripts/build-alwaysdata.sh" "${build_args[@]}"
elif [[ "$binary" != /* ]]; then
  binary="$repo_dir/$binary"
fi
[[ -f "$binary" ]] || { echo "error: binary not found: $binary" >&2; exit 66; }

if [[ -z "$output" ]]; then
  output="$repo_dir/.state/build/alwaysdata/sova-alwaysdata-linux-$arch.tar.gz"
elif [[ "$output" != /* ]]; then
  output="$repo_dir/$output"
fi
mkdir -p "$(dirname "$output")"

stage="$(mktemp -d "${TMPDIR:-/tmp}/sova-alwaysdata-package.XXXXXX")"
trap 'rm -rf "$stage"' EXIT
payload="$stage/sova-alwaysdata"
mkdir -p "$payload/bin" "$payload/scripts"

install -m 0755 "$binary" "$payload/bin/sova"
for runner in run-all.sh run-nest.sh run-workspace.sh; do
  install -m 0755 "$repo_dir/deploy/alwaysdata/$runner" "$payload/$runner"
done
for script_name in backup-alwaysdata.sh restore-alwaysdata.sh smoke-alwaysdata.sh; do
  install -m 0755 "$repo_dir/scripts/$script_name" "$payload/scripts/$script_name"
done
install -m 0644 "$repo_dir/deploy/alwaysdata/env.example" "$payload/env.example"
install -m 0644 "$repo_dir/deploy/alwaysdata/README.md" "$payload/README.md"

(
  cd "$payload"
  if command -v sha256sum >/dev/null 2>&1; then
    find . -type f ! -name SHA256SUMS -print0 | sort -z | xargs -0 sha256sum > SHA256SUMS
  else
    find . -type f ! -name SHA256SUMS -print0 | sort -z | xargs -0 shasum -a 256 > SHA256SUMS
  fi
)

temporary="${output}.new.$$"
tar -C "$stage" -czf "$temporary" sova-alwaysdata

while IFS= read -r member; do
  case "$member" in
    /*|*"../"*|*/.env|*/data/*|*/.sessions/*|*/.secrets/*|*/.state/*)
      echo "error: unsafe or private archive member: $member" >&2
      exit 70
      ;;
  esac
done < <(tar -tzf "$temporary")

mv -f "$temporary" "$output"
size_bytes="$(wc -c < "$output" | tr -d '[:space:]')"
echo "Package: $output"
echo "Size: $size_bytes bytes"
echo "Contents contain no runtime data or secrets."
