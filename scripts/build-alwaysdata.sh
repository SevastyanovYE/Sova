#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Usage: scripts/build-alwaysdata.sh [--arch amd64|arm64] [--output PATH] [--allow-dirty]

Cross-builds a stripped, static Linux binary outside alwaysdata. The target
architecture defaults to amd64, matching the documented Debian x64 Public Cloud.
Do not infer a non-default target from the SSH server: Services run separately.
USAGE
}

repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
arch="amd64"
output=""
allow_dirty=false

while (($#)); do
  case "$1" in
    --arch)
      [[ $# -ge 2 ]] || { echo "error: --arch requires a value" >&2; exit 64; }
      arch="$2"
      shift 2
      ;;
    --output)
      [[ $# -ge 2 ]] || { echo "error: --output requires a value" >&2; exit 64; }
      output="$2"
      shift 2
      ;;
    --allow-dirty)
      allow_dirty=true
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "error: unknown argument: $1" >&2
      usage >&2
      exit 64
      ;;
  esac
done

case "$arch" in
  amd64|arm64) ;;
  *) echo "error: unsupported alwaysdata architecture: $arch" >&2; exit 64 ;;
esac

for command_name in go git file; do
  command -v "$command_name" >/dev/null 2>&1 || {
    echo "error: required local command is missing: $command_name" >&2
    exit 69
  }
done

cd "$repo_dir"
if [[ "$allow_dirty" != true ]] && [[ -n "$(git status --porcelain)" ]]; then
  echo "error: tracked working tree is dirty; commit/review it or pass --allow-dirty for a non-release test build" >&2
  exit 65
fi

version="$(tr -d '[:space:]' < VERSION)"
commit="$(git rev-parse --verify HEAD)"
if [[ -n "$(git status --porcelain)" ]]; then
  commit="${commit}-dirty"
fi

if [[ -z "$output" ]]; then
  output="$repo_dir/.state/build/alwaysdata/sova-linux-$arch"
elif [[ "$output" != /* ]]; then
  output="$repo_dir/$output"
fi
mkdir -p "$(dirname "$output")"
temporary="${output}.new.$$"
trap 'rm -f "$temporary"' EXIT

echo "Building Sova $version for linux/$arch (CGO_ENABLED=0, timetzdata, stripped)..."
CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build \
  -tags timetzdata \
  -trimpath \
  -buildvcs=false \
  -ldflags "-s -w -X github.com/SevastyanovYE/Sova/internal/buildinfo.Version=$version -X github.com/SevastyanovYE/Sova/internal/buildinfo.Commit=$commit" \
  -o "$temporary" ./cmd/sova

description="$(file "$temporary")"
if [[ "$description" != *ELF* ]] || [[ "$description" != *"statically linked"* ]]; then
  echo "error: build is not a static Linux ELF binary: $description" >&2
  exit 70
fi
if [[ "$description" == *"dynamically linked"* ]] || [[ "$description" == *"interpreter"* ]]; then
  echo "error: dynamic loader dependency detected: $description" >&2
  exit 70
fi

chmod 0755 "$temporary"
mv -f "$temporary" "$output"
trap - EXIT

if command -v sha256sum >/dev/null 2>&1; then
  checksum="$(sha256sum "$output" | awk '{print $1}')"
else
  checksum="$(shasum -a 256 "$output" | awk '{print $1}')"
fi
printf '%s  %s\n' "$checksum" "$(basename "$output")" > "${output}.sha256"

size_bytes="$(wc -c < "$output" | tr -d '[:space:]')"
echo "Built: $output"
echo "Size: $size_bytes bytes"
echo "SHA-256: $checksum"
