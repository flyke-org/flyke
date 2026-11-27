#!/bin/sh
# Installs the flyke CLI from GitHub Releases (Linux / macOS).
#
#   curl -fsSL https://raw.githubusercontent.com/flyke-org/flyke/main/install.sh | sh
#
# Options (environment variables):
#   FLYKE_VERSION=0.1.0-dev.0   install this version instead of the newest release
#   FLYKE_INSTALL_DIR=~/bin     install here instead of /usr/local/bin
set -eu

repo="flyke-org/flyke"
install_dir="${FLYKE_INSTALL_DIR:-/usr/local/bin}"

fail() { echo "flyke install: $*" >&2; exit 1; }

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) fail "unsupported OS $(uname -s) (Linux and macOS only)" ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) fail "unsupported CPU $(uname -m) (amd64 and arm64 only)" ;;
esac

version="${FLYKE_VERSION:-}"
if [ -z "$version" ]; then
  # Newest release, pre-releases included (releases/latest would skip them).
  version="$(curl -fsSL "https://api.github.com/repos/$repo/releases?per_page=1" |
    sed -n 's/.*"tag_name": *"v\([^"]*\)".*/\1/p' | head -n 1)" ||
    fail "cannot reach GitHub releases for $repo"
  [ -n "$version" ] || fail "no release found for $repo"
fi
version="${version#v}"

file="flyke_${version}_${os}_${arch}.tar.gz"
url="https://github.com/$repo/releases/download/v$version"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

echo "Downloading flyke $version ($os/$arch)"
curl -fsSL -o "$tmp/$file" "$url/$file" || fail "download failed: $url/$file"
curl -fsSL -o "$tmp/checksums.txt" "$url/checksums.txt" || fail "download failed: $url/checksums.txt"

expected="$(grep " $file\$" "$tmp/checksums.txt" | cut -d' ' -f1)"
if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "$tmp/$file" | cut -d' ' -f1)"
else
  actual="$(shasum -a 256 "$tmp/$file" | cut -d' ' -f1)"
fi
[ -n "$expected" ] && [ "$expected" = "$actual" ] || fail "checksum mismatch for $file"

tar -xzf "$tmp/$file" -C "$tmp" flyke

if [ -w "$install_dir" ] || { [ ! -e "$install_dir" ] && mkdir -p "$install_dir" 2>/dev/null; }; then
  mv "$tmp/flyke" "$install_dir/flyke"
else
  echo "Installing to $install_dir (needs sudo)"
  sudo mv "$tmp/flyke" "$install_dir/flyke"
fi

echo "Installed $("$install_dir/flyke" --version) to $install_dir/flyke"
