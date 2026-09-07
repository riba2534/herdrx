#!/usr/bin/env bash
# 只发布当前流水线已测试的两种架构镜像；认证由 GitHub Environment 提供。
set -euo pipefail
: "${DOCKERHUB_REPOSITORY:?missing DOCKERHUB_REPOSITORY}"
: "${IMAGE_REVISION:?missing IMAGE_REVISION}"
: "${PUBLISH_OUTPUT:?missing PUBLISH_OUTPUT}"
: "${GITHUB_REPOSITORY:?missing GITHUB_REPOSITORY}"
# Docker Hub 引用限定为 namespace/repository，不接受其他 registry、标签或 digest。
[[ "$DOCKERHUB_REPOSITORY" =~ ^[a-z0-9]+([_-][a-z0-9]+)*/[a-z0-9]+([._-][a-z0-9]+)*$ ]] || exit 1
[[ "$IMAGE_REVISION" =~ ^[0-9a-f]{40}$ ]] || exit 1
image="docker.io/${DOCKERHUB_REPOSITORY}"
tag="sha-${IMAGE_REVISION}"
declare -a sources=()

for arch in amd64 arm64; do
  source="herdrx-server:ci-${arch}"
  [[ $(docker image inspect --format '{{ index .Config.Labels "org.opencontainers.image.revision" }}' "$source") == "$IMAGE_REVISION" ]]
  [[ $(docker image inspect --format '{{.Os}}/{{.Architecture}}' "$source") == "linux/$arch" ]]
done

for arch in amd64 arm64; do
  target="${image}:${tag}-${arch}"
  source="herdrx-server:ci-${arch}"
  source_id=$(docker image inspect --format '{{.Id}}' "$source")
  docker tag "$source" "$target"
  docker push "$target"
  digest=$(docker buildx imagetools inspect "$target" --format '{{json .Manifest}}' | python3 -c 'import json,sys; print(json.load(sys.stdin)["digest"])')
  [[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]]
  reference="${image}@${digest}"
  docker pull --platform "linux/$arch" "$reference"
  [[ $(docker image inspect --format '{{.Id}}' "$reference") == "$source_id" ]]
  sources+=("$reference")
done

docker buildx imagetools create --tag "${image}:${tag}" "${sources[@]}"
digest=$(docker buildx imagetools inspect "${image}:${tag}" --format '{{json .Manifest}}' | python3 -c 'import json,sys; print(json.load(sys.stdin)["digest"])')
[[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]]
docker buildx imagetools inspect "${image}@${digest}" --raw | python3 -c '
import json,sys
p={(m["platform"]["os"],m["platform"]["architecture"]) for m in json.load(sys.stdin)["manifests"]}
assert p == {("linux","amd64"),("linux","arm64")}, p
'

# 手动重跑旧提交时保留提交镜像，但不能将 latest 倒退到旧 main。
head_revision=$(gh api "repos/${GITHUB_REPOSITORY}/commits/main" --jq .sha)
promoted=false
if [[ "$head_revision" == "$IMAGE_REVISION" ]]; then
  docker buildx imagetools create --tag "${image}:latest" "${image}@${digest}"
  latest_digest=$(docker buildx imagetools inspect "${image}:latest" --format '{{json .Manifest}}' | python3 -c 'import json,sys; print(json.load(sys.stdin)["digest"])')
  [[ "$latest_digest" == "$digest" ]]
  promoted=true
fi

mkdir -p "$PUBLISH_OUTPUT"
# Docker Hub 镜像公开可拉取，附件直接提供可用的固定 digest。
printf 'HERDRX_IMAGE=%s@%s\n' "$image" "$digest" > "$PUBLISH_OUTPUT/release.env"
printf 'Source: %s\nRepository: %s\nCommit tag: %s\nDigest: %s\nPlatforms: linux/amd64, linux/arm64\nUpdated latest: %s\n' \
  "$IMAGE_REVISION" "$image" "$tag" "$digest" "$promoted" > "$PUBLISH_OUTPUT/verification.txt"
tar -C deploy -czf "$PUBLISH_OUTPUT/herdrx-deploy.tar.gz" compose.yml compose.prod.yml .env.example prepare-data.sh
cat "$PUBLISH_OUTPUT/verification.txt"
