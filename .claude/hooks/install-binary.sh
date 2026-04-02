#!/bin/sh
# Auto-install or update spec-viewer binary.
# Called by SessionStart hook.

REPO="bzon/spec-viewer"
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"

case "$ARCH" in
  x86_64|amd64) ARCH="amd64" ;;
  arm64|aarch64) ARCH="arm64" ;;
  *) exit 0 ;;
esac

case "$OS" in
  darwin|linux) ;;
  *) exit 0 ;;
esac

ASSET="spec-viewer_${OS}_${ARCH}.tar.gz"
INSTALL_DIR="${HOME}/.local/bin"

# Get latest release tag (cached for 24h to avoid API spam)
CACHE_FILE="${HOME}/.cache/spec-viewer/latest-check"
mkdir -p "$(dirname "$CACHE_FILE")"

NEED_CHECK=true
if [ -f "$CACHE_FILE" ]; then
  CACHE_AGE=$(( $(date +%s) - $(stat -f %m "$CACHE_FILE" 2>/dev/null || stat -c %Y "$CACHE_FILE" 2>/dev/null || echo 0) ))
  if [ "$CACHE_AGE" -lt 86400 ]; then
    NEED_CHECK=false
  fi
fi

if [ "$NEED_CHECK" = true ]; then
  TAG=$(curl -sL "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | head -1 | cut -d'"' -f4)
  if [ -n "$TAG" ]; then
    echo "$TAG" > "$CACHE_FILE"
  fi
else
  TAG=$(cat "$CACHE_FILE" 2>/dev/null)
fi

if [ -z "$TAG" ]; then
  exit 0
fi

# Check if already installed and up to date
if command -v spec-viewer >/dev/null 2>&1; then
  CURRENT=$(spec-viewer --version 2>/dev/null | awk '{print $2}')
  LATEST=$(echo "$TAG" | sed 's/^v//')
  if [ "$CURRENT" = "$LATEST" ]; then
    exit 0
  fi
  echo "spec-viewer: updating $CURRENT -> $LATEST"
else
  echo "spec-viewer: installing $TAG"
fi

# Download and install
mkdir -p "$INSTALL_DIR"
URL="https://github.com/${REPO}/releases/download/${TAG}/${ASSET}"
TMPDIR=$(mktemp -d)

if curl -sL "$URL" | tar xz -C "$TMPDIR" 2>/dev/null; then
  mv "$TMPDIR/spec-viewer" "$INSTALL_DIR/spec-viewer"
  chmod +x "$INSTALL_DIR/spec-viewer"
  echo "spec-viewer: installed ${TAG} to ${INSTALL_DIR}/spec-viewer"
  if ! echo "$PATH" | grep -q "$INSTALL_DIR"; then
    echo "spec-viewer: note: add ${INSTALL_DIR} to your PATH"
  fi
else
  echo "spec-viewer: download failed" >&2
fi
rm -rf "$TMPDIR"
