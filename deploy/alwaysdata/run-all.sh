#!/usr/bin/env bash
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
cd "$root_dir"
umask 077

# The Go runtime reads these before main starts, so set the alwaysdata defaults
# here (or override them in Advanced -> Services), not only in .env.
export GOMEMLIMIT="${GOMEMLIMIT:-160MiB}"
export GOGC="${GOGC:-75}"

exec "$root_dir/bin/sova" serve-all
