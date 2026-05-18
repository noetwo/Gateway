#!/usr/bin/env bash
# Linux / macOS 编译脚本
# 需要本机已安装 Go >= 1.21（没装的话用包管理装一下：apt install golang / brew install go）
set -e
cd "$(dirname "$0")/.."
go build -o main .
echo "BUILD_OK: $(pwd)/main"
echo "运行: ./main"
