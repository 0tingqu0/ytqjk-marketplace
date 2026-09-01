#!/usr/bin/env sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
go_version=1.27.0

go_usable() {
  [ -x "$1" ] || return 1
  version=$("$1" version 2>/dev/null | sed -n 's/.* go\([0-9][0-9]*\)\.\([0-9][0-9]*\).*/\1 \2/p')
  [ -n "$version" ] || return 1
  major=$(printf '%s' "$version" | cut -d' ' -f1)
  minor=$(printf '%s' "$version" | cut -d' ' -f2)
  [ "$major" -gt 1 ] || { [ "$major" -eq 1 ] && [ "$minor" -ge 25 ]; }
}

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    printf '%s\n' 'YTQJK: sha256sum or shasum is required.' >&2
    return 1
  fi
}

os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  *) printf '%s\n' "YTQJK: unsupported architecture: $arch" >&2; exit 1 ;;
esac
if [ "$os-$arch" != linux-amd64 ]; then
  printf '%s\n' "YTQJK: unsupported platform: $os-$arch" >&2
  exit 1
fi

data_root=${XDG_DATA_HOME:-"$HOME/.local/share"}
bundle_binary="$script_dir/bin/ytqjk"
bundle_manifest="$script_dir/release-manifest.json"
bootstrap=

if [ -e "$bundle_binary" ] || [ -e "$bundle_manifest" ]; then
  if [ ! -f "$bundle_manifest" ] || [ -L "$bundle_manifest" ] ||
    [ ! -x "$bundle_binary" ] || [ -L "$bundle_binary" ] ||
    [ ! -d "$script_dir/plugins/ytqjk-agentic-orchestrator" ] ||
    [ ! -d "$script_dir/plugins/ytqjk-knowledge" ]; then
    printf '%s\n' 'YTQJK: release bundle is incomplete or unsafe.' >&2
    exit 1
  fi
  manifest_lines=$(wc -l < "$bundle_manifest" | tr -d '[:space:]')
  manifest_values=$(sed -n \
    's#^{"schema":"ytqjk-release-bundle/v1","version":"\([0-9][0-9]*\.[0-9][0-9]*\.[0-9][0-9]*\)","os":"linux","arch":"amd64","binary_sha256":"\([0-9a-f]\{64\}\)"}$#\1;\2#p' \
    "$bundle_manifest")
  if [ "$manifest_lines" != 1 ] || [ -z "$manifest_values" ]; then
    printf '%s\n' 'YTQJK: release manifest is invalid.' >&2
    exit 1
  fi
  manifest_version=$(printf '%s' "$manifest_values" | cut -d ';' -f 1)
  manifest_sha=$(printf '%s' "$manifest_values" | cut -d ';' -f 2)
  actual_sha=$(sha256_file "$bundle_binary")
  if [ "$actual_sha" != "$manifest_sha" ]; then
    printf '%s\n' 'YTQJK: release bundle verification failed.' >&2
    exit 1
  fi
  binary_version=$("$bundle_binary" version)
  if [ "$binary_version" != "$manifest_version" ]; then
    printf '%s\n' 'YTQJK: release bundle verification failed.' >&2
    exit 1
  fi
  binary=$bundle_binary
else
  system_go=$(command -v go 2>/dev/null || true)
  if go_usable "$system_go"; then
    go_bin=$system_go
  else
    sha=675c26c449cbb18fc24b74650de1eabbae6e16f64326fd85a283fb3b58280685
    runtime_root="$data_root/ytqjk/runtime"
    toolchain="$runtime_root/toolchains/go$go_version"
    go_bin="$toolchain/go/bin/go"
    if ! go_usable "$go_bin"; then
      downloads="$runtime_root/downloads"
      archive="$downloads/go$go_version.$os-$arch.tar.gz"
      partial="$archive.partial"
      mkdir -p "$downloads"
      printf '%s\n' "YTQJK: downloading Go $go_version for $os-$arch." >&2
      if command -v curl >/dev/null 2>&1; then
        curl --fail --location --proto '=https' --tlsv1.2 \
          "https://go.dev/dl/go$go_version.$os-$arch.tar.gz" -o "$partial"
      elif command -v wget >/dev/null 2>&1; then
        wget -O "$partial" "https://go.dev/dl/go$go_version.$os-$arch.tar.gz"
      else
        printf '%s\n' 'YTQJK: curl or wget is required to download Go.' >&2
        exit 1
      fi
      actual=$(sha256_file "$partial")
      if [ "$actual" != "$sha" ]; then
        rm -f "$partial"
        printf '%s\n' 'YTQJK: Go toolchain checksum verification failed.' >&2
        exit 1
      fi
      mv "$partial" "$archive"
      stage="$downloads/go-stage-$$"
      trap 'rm -rf "$stage"' EXIT HUP INT TERM
      mkdir -p "$stage"
      tar -xzf "$archive" -C "$stage"
      rm -rf "$toolchain"
      mkdir -p "$(dirname "$toolchain")"
      mv "$stage" "$toolchain"
      trap - EXIT HUP INT TERM
    fi
  fi
  bootstrap="${TMPDIR:-/tmp}/ytqjk-bootstrap-$$"
  printf '%s\n' 'YTQJK: building the Go runtime.' >&2
  trap 'rm -f "$bootstrap"' EXIT HUP INT TERM
  "$go_bin" -C "$script_dir" build -trimpath -ldflags '-s -w' -o "$bootstrap" ./cmd/ytqjk
  chmod 755 "$bootstrap"
  binary=$bootstrap
fi

if [ "$#" -eq 0 ]; then
  set -- --mode all \
    --target-root "$script_dir" \
    --project-root "$script_dir" \
    --source-root "$script_dir" \
    --apply --yes
else
  has_source=false
  uninstall=false
  has_mode=false
  has_target=false
  has_apply=false
  has_yes=false
  for argument in "$@"; do
    [ "$argument" = "--source-root" ] && has_source=true
    [ "$argument" = "--uninstall" ] && uninstall=true
    [ "$argument" = "--mode" ] && has_mode=true
    [ "$argument" = "--target-root" ] && has_target=true
    [ "$argument" = "--apply" ] && has_apply=true
    [ "$argument" = "--yes" ] && has_yes=true
  done
  if [ "$has_source" = false ]; then
    set -- "$@" --source-root "$script_dir"
  fi
  if [ "$uninstall" = true ]; then
    [ "$has_mode" = true ] || set -- "$@" --mode all
    [ "$has_target" = true ] || set -- "$@" --target-root "$script_dir"
    [ "$has_apply" = true ] || set -- "$@" --apply
    [ "$has_yes" = true ] || set -- "$@" --yes
  fi
fi
"$binary" "$@"
status=$?
if [ -n "$bootstrap" ]; then
  rm -f "$bootstrap"
  trap - EXIT HUP INT TERM
fi
exit "$status"
