#!/bin/sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
GO_BIN=${GO_BIN:-go}
TARGET_ARCH=${1:-}

if [ -z "${TARGET_ARCH}" ]; then
  TARGET_ARCH=$(${GO_BIN} env GOARCH)
fi

case "${TARGET_ARCH}" in
  amd64|arm64) ;;
  *)
    echo "Unsupported Linux architecture: ${TARGET_ARCH} (supported: amd64, arm64)" >&2
    exit 2
    ;;
esac

OUTPUT_DIR="${ROOT_DIR}/dist/linux-${TARGET_ARCH}"
mkdir -p "${OUTPUT_DIR}"

cd "${ROOT_DIR}"
CGO_ENABLED=0 GOOS=linux GOARCH="${TARGET_ARCH}" "${GO_BIN}" build \
  -trimpath -buildvcs=false -ldflags="-s -w" \
  -o "${OUTPUT_DIR}/dj4ghub" ./cmd/dj4ghub-macos

chmod 755 "${OUTPUT_DIR}/dj4ghub"
echo "Linux binary: ${OUTPUT_DIR}/dj4ghub"
