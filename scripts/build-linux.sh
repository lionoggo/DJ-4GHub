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

if [ "$(uname -s)" != "Linux" ]; then
  echo "Linux USB build must run on a Linux builder. For QNAP, use packaging/qnap/docker-compose.yml." >&2
  exit 1
fi

HOST_ARCH=$(${GO_BIN} env GOARCH)
if [ "${TARGET_ARCH}" != "${HOST_ARCH}" ]; then
  echo "Cross-compiling the libusb USB transport is not supported by this script. Build on matching Linux ${TARGET_ARCH} hardware." >&2
  exit 1
fi

if ! command -v pkg-config >/dev/null 2>&1 || ! pkg-config --exists libusb-1.0; then
  echo "libusb-1.0 development files and pkg-config are required (for example: libusb-1.0-0-dev)." >&2
  exit 1
fi

OUTPUT_DIR="${ROOT_DIR}/dist/linux-${TARGET_ARCH}"
mkdir -p "${OUTPUT_DIR}"

cd "${ROOT_DIR}"
CGO_ENABLED=1 GOOS=linux GOARCH="${TARGET_ARCH}" "${GO_BIN}" build \
  -trimpath -buildvcs=false -ldflags="-s -w" \
  -o "${OUTPUT_DIR}/dj4ghub" ./cmd/dj4ghub-macos

chmod 755 "${OUTPUT_DIR}/dj4ghub"
echo "Linux binary: ${OUTPUT_DIR}/dj4ghub"
