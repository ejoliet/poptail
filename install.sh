#!/bin/sh
# poptail installer — https://github.com/ejoliet/poptail
#
#   curl -sSfL https://raw.githubusercontent.com/ejoliet/poptail/main/install.sh | sh
#
# Env overrides:
#   POPTAIL_INSTALL_DIR  install target (default: ~/.local/bin)
#   POPTAIL_VERSION      release tag to install (default: latest)
#
# Downloads the release binary for this OS/arch and verifies it against the
# SHA256SUMS file published in the same release before installing.
set -eu

REPO="ejoliet/poptail"
INSTALL_DIR="${POPTAIL_INSTALL_DIR:-$HOME/.local/bin}"

fail() { echo "poptail-install: $*" >&2; exit 1; }

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
  darwin | linux) ;;
  *) fail "unsupported OS: $os (Windows: grab the .exe from https://github.com/$REPO/releases)" ;;
esac

arch=$(uname -m)
case "$arch" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) fail "unsupported arch: $arch" ;;
esac

command -v curl >/dev/null 2>&1 || fail "curl is required"

tag="${POPTAIL_VERSION:-}"
if [ -z "$tag" ]; then
  tag=$(curl -sSfL "https://api.github.com/repos/$REPO/releases/latest" |
    sed -n 's/^ *"tag_name": *"\([^"]*\)".*/\1/p' | head -1)
fi
[ -n "$tag" ] || fail "could not determine latest release (no releases yet?)"

asset="poptail_${os}_${arch}"
base="https://github.com/$REPO/releases/download/$tag"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "downloading poptail $tag ($os/$arch)..."
curl -sSfL -o "$tmp/$asset" "$base/$asset" || fail "download failed: $base/$asset"
curl -sSfL -o "$tmp/SHA256SUMS" "$base/SHA256SUMS" || fail "download failed: $base/SHA256SUMS"

cd "$tmp"
sums_line=$(grep -E " +$asset\$" SHA256SUMS) || fail "no checksum for $asset in SHA256SUMS"
if command -v sha256sum >/dev/null 2>&1; then
  echo "$sums_line" | sha256sum -c - >/dev/null || fail "checksum mismatch"
elif command -v shasum >/dev/null 2>&1; then
  echo "$sums_line" | shasum -a 256 -c - >/dev/null || fail "checksum mismatch"
else
  fail "need sha256sum or shasum to verify the download"
fi

mkdir -p "$INSTALL_DIR"
install -m 0755 "$asset" "$INSTALL_DIR/poptail"
echo "installed: $INSTALL_DIR/poptail ($("$INSTALL_DIR/poptail" -version))"

case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *) echo "note: $INSTALL_DIR is not on your PATH" ;;
esac
