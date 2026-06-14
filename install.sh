#!/bin/sh
# install.sh — download and install the depbisect binary (Linux/macOS).
#
#   curl -fsSL https://raw.githubusercontent.com/skyneticist/depbisect/main/install.sh | sh
#
# Prefer to read before running? Download it first, inspect it, then run `sh install.sh`.
#
# Environment overrides:
#   DEPBISECT_VERSION      version/tag to install (default: latest release)
#   DEPBISECT_INSTALL_DIR  install directory (default: /usr/local/bin if writable,
#                          otherwise $HOME/.local/bin)
#
# The downloaded archive is verified against the release checksums.txt before install.

set -eu

REPO="skyneticist/depbisect"
BIN="depbisect"

err() { printf 'install: %s\n' "$1" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

# --- detect platform ---------------------------------------------------------
os=$(uname -s)
case "$os" in
  Linux)  os="linux" ;;
  Darwin) os="darwin" ;;
  *)      err "unsupported OS '$os' (this script supports Linux and macOS; on Windows use Scoop or a release zip)" ;;
esac

arch=$(uname -m)
case "$arch" in
  x86_64|amd64)  arch="amd64" ;;
  arm64|aarch64) arch="arm64" ;;
  *)             err "unsupported architecture '$arch'" ;;
esac

# --- fetch helpers -----------------------------------------------------------
if have curl; then
  dl() { curl -fsSL "$1" -o "$2"; }
  redirect_url() { curl -fsSLI -o /dev/null -w '%{url_effective}' "$1"; }
elif have wget; then
  dl() { wget -qO "$2" "$1"; }
  redirect_url() { wget -q -S -O /dev/null "$1" 2>&1 | awk '/Location:/{u=$2} END{print u}'; }
else
  err "need curl or wget on PATH"
fi

# --- resolve version ---------------------------------------------------------
tag="${DEPBISECT_VERSION:-}"
if [ -z "$tag" ]; then
  # Follow the /releases/latest redirect to learn the newest tag — no API token,
  # no jq, no rate-limit surprises.
  latest_url=$(redirect_url "https://github.com/$REPO/releases/latest") || true
  tag="${latest_url##*/}"
  [ -n "$tag" ] && [ "$tag" != "latest" ] || err "could not determine the latest release; set DEPBISECT_VERSION"
fi
case "$tag" in v*) version="${tag#v}" ;; *) version="$tag"; tag="v$tag" ;; esac

archive="${BIN}_${version}_${os}_${arch}.tar.gz"
base="https://github.com/$REPO/releases/download/$tag"

# --- download + verify -------------------------------------------------------
tmp=$(mktemp -d 2>/dev/null || mktemp -d -t depbisect)
trap 'rm -rf "$tmp"' EXIT INT TERM

printf 'install: downloading %s (%s)\n' "$archive" "$tag" >&2
dl "$base/$archive" "$tmp/$archive" || err "download failed: $base/$archive"
dl "$base/checksums.txt" "$tmp/checksums.txt" || err "could not download checksums.txt"

printf 'install: verifying checksum\n' >&2
( cd "$tmp"
  if have sha256sum; then
    sha256sum --ignore-missing -c checksums.txt >/dev/null
  elif have shasum; then
    grep " ${archive}\$" checksums.txt | shasum -a 256 -c - >/dev/null
  else
    err "need sha256sum or shasum to verify the download"
  fi
) || err "checksum verification failed for $archive"

# --- extract + install -------------------------------------------------------
tar -xzf "$tmp/$archive" -C "$tmp" "$BIN" || err "could not extract $BIN from $archive"

dir="${DEPBISECT_INSTALL_DIR:-}"
if [ -z "$dir" ]; then
  if [ -w /usr/local/bin ] 2>/dev/null; then dir="/usr/local/bin"; else dir="$HOME/.local/bin"; fi
fi
mkdir -p "$dir"
install -m 0755 "$tmp/$BIN" "$dir/$BIN" 2>/dev/null || { cp "$tmp/$BIN" "$dir/$BIN" && chmod 0755 "$dir/$BIN"; }

printf 'install: installed %s %s to %s\n' "$BIN" "$version" "$dir/$BIN" >&2
case ":$PATH:" in
  *":$dir:"*) ;;
  *) printf 'install: note — %s is not on your PATH; add it to your shell profile.\n' "$dir" >&2 ;;
esac
"$dir/$BIN" version || true
