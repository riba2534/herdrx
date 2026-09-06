#!/usr/bin/env bash
# 初始化可见的宿主机目录。镜像以 distroless nonroot (65532:65532) 运行。
set -euo pipefail

if [[ $(id -u) -ne 0 ]]; then
  echo '请使用 sudo bash prepare-data.sh [部署目录] 初始化目录权限。' >&2
  exit 1
fi

root=${1:-$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)}
mkdir -p -- "$root"
for directory in "$root/data" "$root/derp/certs"; do
  if [[ -L "$directory" ]]; then
    echo "拒绝修改符号链接目录：$directory" >&2
    exit 1
  fi
  if [[ -d "$directory" && $(stat -c '%u:%g' "$directory") != 65532:65532 ]] &&
      [[ -n $(find "$directory" -mindepth 1 -maxdepth 1 ! -name .keep -print -quit) ]]; then
    echo "已有数据目录的属主不同：$directory；请按运维文档备份并迁移，脚本不会递归修改。" >&2
    exit 1
  fi
  install -d -m 0700 -- "$directory"
  chown 65532:65532 -- "$directory"
done
echo "目录已就绪：$root/data、$root/derp/certs"
