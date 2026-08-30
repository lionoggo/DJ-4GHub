#!/bin/sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
DIST_DIR="${ROOT_DIR}/dist"

mkdir -p "${DIST_DIR}"

cd "${ROOT_DIR}"

if ! command -v swift >/dev/null 2>&1; then
  echo "Swift is required to build the USB audio helper tools." >&2
  exit 1
fi

swift build -c release --package-path "${ROOT_DIR}/mavo" --product GateCPromptPlayer
swift build -c release --package-path "${ROOT_DIR}/mavo" --product GateCCallRecorder

ARCH=$(go env GOARCH)
PKG_CONFIG_PATH="${PKG_CONFIG_PATH:-/opt/homebrew/lib/pkgconfig:/usr/local/lib/pkgconfig}"
export PKG_CONFIG_PATH

CGO_ENABLED=1 GOOS=darwin GOARCH="${ARCH}" go build \
  -p 2 \
  -trimpath -ldflags="-s -w" \
  -o "${DIST_DIR}/dj4ghub-macos-${ARCH}" ./cmd/dj4ghub-macos

cp "${DIST_DIR}/dj4ghub-macos-${ARCH}" "${DIST_DIR}/dj4ghub-macos"
cp "${ROOT_DIR}/mavo/.build/release/GateCPromptPlayer" "${DIST_DIR}/dj4ghub-uac-prompt-player"
cp "${ROOT_DIR}/mavo/.build/release/GateCCallRecorder" "${DIST_DIR}/dj4ghub-uac-call-recorder"

echo "macOS service and USB audio helper binaries written to ${DIST_DIR}"
