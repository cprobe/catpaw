#!/bin/sh
set -e

export GOPROXY=https://goproxy.cn
export CGO_ENABLED=0

APP_NAME="catpaw"
VERSION=$(grep 'var Version' config/version.go | awk -F'"' '{print $2}')
TIMESTAMP=$(date +%Y%m%d%H%M%S)
GIT_COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")

build_local() {
    echo "==> Building for local platform ..."
    go build -ldflags "-s -w -X github.com/cprobe/catpaw/config.Version=${VERSION}-${GIT_COMMIT}" -o ${APP_NAME} .
    echo "==> Done: ./${APP_NAME}"
}

# _compile_linux_amd64: build the binary for linux/amd64 and echo the output path.
_compile_linux_amd64() {
    GOOS=linux GOARCH=amd64 \
    go build -ldflags "-s -w -X github.com/cprobe/catpaw/config.Version=${VERSION}-${GIT_COMMIT}" -o ${APP_NAME} .
}

# _upload: upload a zip to OSS and print the download link.
_upload() {
    ZIP_NAME="${1}"
    ossutil cp "${ZIP_NAME}" oss://flashcat-public/ulrictmp/
    echo "Download link: https://flashcat-public.oss-cn-beijing.aliyuncs.com/ulrictmp/${ZIP_NAME}"
    echo "==> Done: ${ZIP_NAME}"
}

build_linux_amd64() {
    _compile_linux_amd64

    RELEASE_DIR="${APP_NAME}-${VERSION}-linux-amd64-${TIMESTAMP}"
    mkdir -p "${RELEASE_DIR}"
    cp ${APP_NAME} "${RELEASE_DIR}/"
    cp -r conf.d   "${RELEASE_DIR}/"

    zip -rq "${RELEASE_DIR}.zip" "${RELEASE_DIR}"
    rm -rf "${RELEASE_DIR}" ${APP_NAME}

    _upload "${RELEASE_DIR}.zip"
}

build_linux_amd64_bin() {
    _compile_linux_amd64

    RELEASE_NAME="${APP_NAME}-${VERSION}-linux-amd64-bin-${TIMESTAMP}.zip"
    zip -q "${RELEASE_NAME}" ${APP_NAME}
    rm -f ${APP_NAME}

    _upload "${RELEASE_NAME}"
}

usage() {
    echo "Usage: $0 {build_local|build_linux_amd64|build_linux_amd64_bin}"
    echo ""
    echo "  build_local          Build for current platform"
    echo "  build_linux_amd64    Cross-compile linux/amd64 and package binary + conf.d as zip"
    echo "  build_linux_amd64_bin Cross-compile linux/amd64 and package binary only as zip"
}

case "${1}" in
    build_local)
        build_local
        ;;
    build_linux_amd64)
        echo "==> Building linux/amd64 release package ..."
        build_linux_amd64
        ;;
    build_linux_amd64_bin)
        echo "==> Building linux/amd64 binary-only package ..."
        build_linux_amd64_bin
        ;;
    *)
        usage
        exit 1
        ;;
esac
