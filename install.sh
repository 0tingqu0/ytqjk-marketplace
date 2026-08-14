#!/usr/bin/env sh
set -eu
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
python_bin=${PYTHON:-python3}
if [ "$#" -eq 0 ]; then
  printf '%s\n' 'YTQJK: Starting full installation; first dependency download may take a few minutes.' >&2
  set -- --mode all \
    --target-root "$script_dir" \
    --project-root "$script_dir" \
    --apply --yes --json
fi
exec "$python_bin" "$script_dir/setup.py" "$@"
