#!/usr/bin/env sh
set -eu
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
python_bin=${PYTHON:-python3}
if ! "${python_bin}" -c \
  'import sys; raise SystemExit(sys.version_info < (3, 11))'; then
  printf '%s\n' 'YTQJK: Python 3.11 or newer is required; Python 3.12 is recommended.' >&2
  exit 1
fi
if ! "${python_bin}" -c \
  'import sys; raise SystemExit(sys.version_info < (3, 12))'; then
  printf '%s\n' 'YTQJK: Python 3.12 is recommended.' >&2
fi
if [ "$#" -eq 0 ]; then
  printf '%s\n' 'YTQJK: Starting full installation; first dependency download may take a few minutes.' >&2
  set -- --mode all \
    --target-root "${script_dir}" \
    --project-root "${script_dir}" \
    --apply --yes
fi
exec "${python_bin}" "${script_dir}/setup.py" "$@"
