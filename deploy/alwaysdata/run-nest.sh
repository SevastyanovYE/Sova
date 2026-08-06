#!/usr/bin/env bash
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
cd "$root_dir"
umask 077

export GOMEMLIMIT="${GOMEMLIMIT:-96MiB}"
export GOGC="${GOGC:-75}"

exec "$root_dir/bin/sova" serve
