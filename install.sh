#!/bin/sh
# Installs TRACE on macOS or Linux: downloads the latest release for this machine, checks its SHA-256, puts it in
# ~/.local/bin (or $TRACE_BIN_DIR), then runs `trace install`, which adds the hooks to Claude Code's user settings
# and does the first import.
#
#   curl -fsSL https://raw.githubusercontent.com/diegolinan/trace/main/install.sh | sh
set -eu

repo=diegolinan/trace
bin_dir=${TRACE_BIN_DIR:-"$HOME/.local/bin"}

case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) echo "trace: this script is for macOS and Linux; on Windows use install.ps1" >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) echo "trace: no build for $(uname -m)" >&2; exit 1 ;;
esac

asset="trace_${os}_${arch}"
base="https://github.com/$repo/releases/latest/download"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "downloading $asset..."
curl -fsSL "$base/$asset" -o "$tmp/$asset"
curl -fsSL "$base/checksums.txt" -o "$tmp/checksums.txt"

want=$(grep " $asset\$" "$tmp/checksums.txt" | cut -d' ' -f1)
if command -v sha256sum >/dev/null 2>&1; then
  got=$(sha256sum "$tmp/$asset" | cut -d' ' -f1)
else
  got=$(shasum -a 256 "$tmp/$asset" | cut -d' ' -f1)
fi
if [ -z "$want" ] || [ "$want" != "$got" ]; then
  echo "trace: checksum mismatch for $asset, not installed" >&2
  exit 1
fi

mkdir -p "$bin_dir"
mv "$tmp/$asset" "$bin_dir/trace"
chmod +x "$bin_dir/trace"
echo "installed $bin_dir/trace"

"$bin_dir/trace" install

case ":$PATH:" in
  *":$bin_dir:"*) ;;
  *) echo "note: $bin_dir is not on your PATH; add it to run 'trace search' and 'trace stats' by name" ;;
esac
