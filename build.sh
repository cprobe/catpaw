#!/bin/sh
set -e

export CGO_ENABLED=0

APP_NAME="catpaw"
VERSION=$(grep 'var version' version.go | awk -F'"' '{print $2}')
TIMESTAMP=$(date +%Y%m%d%H%M%S)
GIT_COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")
LDFLAGS="-s -w -X main.version=${VERSION}-${GIT_COMMIT}"

_package() {
    GOOS_VAL="${1}"
    GOARCH_VAL="${2}"

    echo "==> Building ${GOOS_VAL}/${GOARCH_VAL} ..."
    GOOS=${GOOS_VAL} GOARCH=${GOARCH_VAL} go build -ldflags "${LDFLAGS}" -o ${APP_NAME} .

    RELEASE_DIR="${APP_NAME}-${VERSION}-${GOOS_VAL}-${GOARCH_VAL}-${TIMESTAMP}"
    mkdir -p "${RELEASE_DIR}"
    cp ${APP_NAME} "${RELEASE_DIR}/"
    cp -r conf.d   "${RELEASE_DIR}/"

    tar czf "${RELEASE_DIR}.tar.gz" "${RELEASE_DIR}"
    rm -rf "${RELEASE_DIR}" ${APP_NAME}

    echo "==> Done: ${RELEASE_DIR}.tar.gz"
}

build_local() {
    echo "==> Building for local platform ..."
    go build -ldflags "${LDFLAGS}" -o ${APP_NAME} .
    echo "==> Done: ./${APP_NAME}"
}

build_linux_amd64() {
    _package linux amd64
}

build_linux_arm64() {
    _package linux arm64
}

build_all() {
    build_linux_amd64
    build_linux_arm64
}

usage() {
    echo "Usage: $0 {local|linux-amd64|linux-arm64|all}"
    echo ""
    echo "  local        Build for current platform"
    echo "  linux-amd64  Cross-compile and package linux/amd64"
    echo "  linux-arm64  Cross-compile and package linux/arm64"
    echo "  all          Build both linux/amd64 and linux/arm64"
}

case "${1}" in
    local)
        build_local
        ;;
    linux-amd64)
        build_linux_amd64
        ;;
    linux-arm64)
        build_linux_arm64
        ;;
    all)
        build_all
        ;;
    "")
        build_local
        ;;
    *)
        usage
        exit 1
        ;;
esac
