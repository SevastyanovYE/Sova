#!/usr/bin/env bash
set -Eeuo pipefail

cd -- "$(dirname -- "$0")/.."
if [[ -n "$(git status --porcelain)" ]]; then
  echo "Commit the reviewed source changes before building an exact release." >&2
  exit 1
fi
sova_version="$(tr -d '[:space:]' < VERSION)"
sova_commit="$(git rev-parse HEAD)"
if [[ ! "$sova_version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "Invalid VERSION" >&2
  exit 1
fi
sova_kit=".state/build/sova-${sova_version}-gcp"
mkdir -p "$sova_kit"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
  -ldflags "-X github.com/SevastyanovYE/Sova/internal/buildinfo.Version=$sova_version -X github.com/SevastyanovYE/Sova/internal/buildinfo.Commit=$sova_commit" \
  -o "$sova_kit/sova-linux-amd64" ./cmd/sova
cp scripts/deploy-gcp.sh "$sova_kit/deploy-gcp.sh"
sova_sha="$(shasum -a 256 "$sova_kit/sova-linux-amd64" | awk '{print $1}')"
printf '#!/usr/bin/env bash\nset -Eeuo pipefail\ncd -- "$(dirname -- "$0")"\nexec bash ./deploy-gcp.sh ./sova-linux-amd64 %s %s %s\n' \
  "$sova_sha" "$sova_version" "$sova_commit" > "$sova_kit/install.sh"
printf 'Version: %s\nCommit: %s\nBinary SHA-256: %s\n' \
  "$sova_version" "$sova_commit" "$sova_sha" > "$sova_kit/release.txt"
(
  cd "$sova_kit"
  shasum -a 256 sova-linux-amd64 deploy-gcp.sh install.sh release.txt > SHA256SUMS
)
sova_archive=".state/build/sova-${sova_version}-gcp.tar.gz"
COPYFILE_DISABLE=1 tar -czf "$sova_archive" -C "$sova_kit" \
  sova-linux-amd64 deploy-gcp.sh install.sh release.txt SHA256SUMS
printf 'Release archive: %s\n' "$sova_archive"
shasum -a 256 "$sova_archive"
