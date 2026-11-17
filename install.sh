#!/bin/sh
# Installs TRACE on macOS or Linux in two steps. First the binary: downloads the latest release for this machine,
# checks its SHA-256 and puts it in ~/.local/bin (or $TRACE_BIN_DIR). Then the integration for the agent named in
# TRACE_HOST (claude-code or codex): `trace install -host <it>`. For codex, run it from the project folder.
# TRACE_HOME picks the memory's folder (default ~/.trace).
#
#   curl -fsSL https://raw.githubusercontent.com/diegolinan/trace/main/install.sh | TRACE_HOST=claude-code sh
#
# An existing trace that differs is replaced only with TRACE_UPDATE=1.
set -eu

case "${TRACE_HOST:-}" in
  claude-code | codex) ;;
  *) echo "trace: set TRACE_HOST to claude-code or codex first; nothing downloaded" >&2; exit 1 ;;
esac
mode_flag=""
if [ "$TRACE_HOST" = codex ]; then
  case "${TRACE_CODEX_MODE:-}" in
    hooks | agents) mode_flag="-mode $TRACE_CODEX_MODE" ;;
    *) echo "trace: for codex, set TRACE_CODEX_MODE to hooks (Codex hooks, run once you trust them) or agents (a request in AGENTS.md); nothing downloaded" >&2; exit 1 ;;
  esac
fi
case "${TRACE_UPDATE:-}" in
  "" | 1) ;;
  *) echo "trace: TRACE_UPDATE is '$TRACE_UPDATE'; it is 1 to replace an existing trace, or unset; nothing downloaded" >&2; exit 1 ;;
esac
previous="" # the replaced binary, kept until the integration completes

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

exe="$bin_dir/trace"
chmod +x "$tmp/$asset"
if [ -e "$exe" ]; then
  if cmp -s "$tmp/$asset" "$exe"; then
    echo "$exe is already this release"
  elif [ "${TRACE_UPDATE:-}" != 1 ]; then
    echo "trace: $exe exists and is another build; set TRACE_UPDATE=1 to replace it; nothing changed" >&2
    exit 1
  else
    "$tmp/$asset" alarm -stop || true # the alarm also exits on its own once the binary changes
    previous="$exe.previous"
    cp -p "$exe" "$previous"
    mv "$tmp/$asset" "$exe"
    echo "replaced $exe (the previous build is kept as $previous)"
  fi
else
  mkdir -p "$bin_dir"
  mv "$tmp/$asset" "$exe"
  echo "installed $exe"
fi

# shellcheck disable=SC2086 # mode_flag is two words or none, both from the fixed list above
if ! "$exe" install -host "$TRACE_HOST" $mode_flag; then
  echo "trace: the binary is installed at $exe, but the $TRACE_HOST integration did not complete; see the messages above" >&2
  [ -n "$previous" ] && echo "trace: the build it replaced is at $previous; copy it over $exe to go back" >&2
  exit 1
fi
[ -n "$previous" ] && rm -f "$previous"
echo "TRACE is installed for $TRACE_HOST"

case ":$PATH:" in
  *":$bin_dir:"*) ;;
  *) echo "note: $bin_dir is not on your PATH; add it to run 'trace search' and 'trace stats' by name" ;;
esac
