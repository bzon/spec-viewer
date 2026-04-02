#!/bin/sh
# Auto-install spec-viewer binary if not found in PATH.
# Called by SessionStart hook.

if command -v spec-viewer >/dev/null 2>&1; then
  exit 0
fi

REPO="bzon/spec-viewer"
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"

case "$ARCH" in
  x86_64|amd64) ARCH="amd64" ;;
  arm64|aarch64) ARCH="arm64" ;;
  *) echo "spec-viewer: unsupported architecture $ARCH" >&2; exit 0 ;;
esac

case "$OS" in
  darwin|linux) ;;
  *) echo "spec-viewer: unsupported OS $OS" >&2; exit 0 ;;
esac

ASSET="spec-viewer_${OS}_${ARCH}.tar.gz"
INSTALL_DIR="${HOME}/.local/bin"
mkdir -p "$INSTALL_DIR"

TAG=$(curl -sL "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | head -1 | cut -d'"' -f4)
if [ -z "$TAG" ]; then
  echo "spec-viewer: could not determine latest release" >&2
  exit 0
fi

URL="https://github.com/${REPO}/releases/download/${TAG}/${ASSET}"

TMPDIR=$(mktemp -d)
if curl -sL "$URL" | tar xz -C "$TMPDIR" 2>/dev/null; then
  mv "$TMPDIR/spec-viewer" "$INSTALL_DIR/spec-viewer"
  chmod +x "$INSTALL_DIR/spec-viewer"
  echo "spec-viewer: installed ${TAG} to ${INSTALL_DIR}/spec-viewer"
  if ! echo "$PATH" | grep -q "$INSTALL_DIR"; then
    echo "spec-viewer: add ${INSTALL_DIR} to your PATH"
  fi
else
  echo "spec-viewer: download failed from ${URL}" >&2
fi
rm -rf "$TMPDIR"
