#!/usr/bin/env sh
set -eu

plugin_root=${PLUGIN_ROOT:?PLUGIN_ROOT is required}
binary="$plugin_root/bin/ytqjk"
if [ ! -x "$binary" ]; then
  data_root=${XDG_DATA_HOME:-"$HOME/.local/share"}
  local_binary="$data_root/ytqjk/runtime/bin/ytqjk"
  if [ -x "$local_binary" ]; then
    binary=$local_binary
  elif command -v ytqjk >/dev/null 2>&1; then
    binary=$(command -v ytqjk)
  else
    printf '%s\n' 'YTQJK Go runtime is unavailable.' >&2
    exit 1
  fi
fi
exec "$binary" hook session-start
