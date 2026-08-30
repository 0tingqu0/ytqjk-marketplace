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

system_go=$(command -v go 2>/dev/null || true)
if go_usable "$system_go"; then
  go_bin=$system_go
else
  os=$(uname -s | tr '[:upper:]' '[:lower:]')
  arch=$(uname -m)
  case "$arch" in
    x86_64|amd64) arch=amd64 ;;
    arm64|aarch64) arch=arm64 ;;
    *) printf '%s\n' "YTQJK: unsupported architecture: $arch" >&2; exit 1 ;;
  esac
  case "$os-$arch" in
    linux-amd64) sha=675c26c449cbb18fc24b74650de1eabbae6e16f64326fd85a283fb3b58280685 ;;
    linux-arm64) sha=51798d2c42d0e1c6ed7fd9f48728b4193abac9e8aad6dbac2fe96a81f5909bda ;;
    darwin-amd64) sha=d3314e25496e4381d71a5c51d2907e7af655d199f6780b549f015bd85fef4986 ;;
    darwin-arm64) sha=90493b3bbd5e10f91d12153198bf1994fd756399b4fec93b49b0c6e2acdeeb3e ;;
    *) printf '%s\n' "YTQJK: unsupported platform: $os-$arch" >&2; exit 1 ;;
  esac
  data_root=${XDG_DATA_HOME:-"$HOME/.local/share"}
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
    if command -v sha256sum >/dev/null 2>&1; then
      actual=$(sha256sum "$partial" | awk '{print $1}')
    else
      actual=$(shasum -a 256 "$partial" | awk '{print $1}')
    fi
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

data_root=${XDG_DATA_HOME:-"$HOME/.local/share"}
runtime_bin="$data_root/ytqjk/runtime/bin"
binary="$runtime_bin/ytqjk"
temporary="$binary.partial"
mkdir -p "$runtime_bin"
printf '%s\n' 'YTQJK: building the Go runtime.' >&2
"$go_bin" -C "$script_dir" build -trimpath -ldflags '-s -w' -o "$temporary" ./cmd/ytqjk
chmod 755 "$temporary"
mv "$temporary" "$binary"

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
exec "$binary" "$@"
