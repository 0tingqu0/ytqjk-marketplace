#!/usr/bin/env sh
set -eu
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
python_bin=${PYTHON:-python3}
if [ "$#" -eq 0 ]; then
  set -- --mode all --target-root "$script_dir" --apply --yes --json
fi
exec "$python_bin" "$script_dir/setup.py" "$@"
