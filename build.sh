#!/bin/sh

export GOPROXY=https://goproxy.cn
export CGO_ENABLED=0

if ! go build; then
    echo "build failed"
    exit 1
fi

version=$(./catpaw -version)
echo "version: $version"

rm -rf dist/catpaw-v${version}-linux-amd64
mkdir -p dist/catpaw-v${version}-linux-amd64

cp catpaw dist/catpaw-v${version}-linux-amd64/
cp -r conf.d dist/catpaw-v${version}-linux-amd64/
cp -r scripts dist/catpaw-v${version}-linux-amd64/

cd dist
tar -zcvf catpaw-v${version}-linux-amd64.tar.gz catpaw-v${version}-linux-amd64

echo "build success"