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

build_linux_amd64() {
    GOOS=linux GOARCH=amd64 \
    go build -ldflags "-s -w -X github.com/cprobe/catpaw/config.Version=${VERSION}-${GIT_COMMIT}" -o ${APP_NAME} .

    RELEASE_DIR="${APP_NAME}-${VERSION}-linux-amd64-${TIMESTAMP}"
    mkdir -p "${RELEASE_DIR}"

    cp ${APP_NAME} "${RELEASE_DIR}/"
    cp -r conf.d   "${RELEASE_DIR}/"

    zip -rq "${RELEASE_DIR}.zip" "${RELEASE_DIR}"
    rm -rf "${RELEASE_DIR}" ${APP_NAME}

    ossutil cp "${RELEASE_DIR}.zip" oss://flashcat-public/ulrictmp/
    echo "Download link: https://flashcat-public.oss-cn-beijing.aliyuncs.com/ulrictmp/${RELEASE_DIR}.zip"

    echo "==> Done: ${RELEASE_DIR}.zip"

}

usage() {
    echo "Usage: $0 {local|release}"
    echo ""
    echo "  local    Build for current platform"
    echo "  release  Cross-compile linux/amd64 and package as zip"
}

case "${1}" in
    local)
        build_local
        ;;
    build_linux_amd64)
        echo "==> Building linux/amd64 release package ..."
        build_linux_amd64
        ;;
    *)
        usage
        exit 1
        ;;
esac
