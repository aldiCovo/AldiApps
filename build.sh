#!/usr/bin/env bash
# Builds and pushes one application's image, tagged with the last commit that changed
# its folder, then writes that tag to the manifests in AldiDevOps, where ArgoCD picks it up.
# Usage: ./build.sh <application folder>
set -euo pipefail

if [ $# -ne 1 ]; then
  echo "Usage: ./build.sh <application folder>" >&2
  exit 1
fi
APP=${1%/}
CONFIG_REPO=${CONFIG_REPO:-$HOME/aldi/AldiDevOps}
KUSTOMIZATION=apps/$APP/kustomization.yaml
IMAGE=ghcr.io/aldicovo/$APP

cd "$(dirname "$0")"

if [ ! -f "$APP/Dockerfile" ]; then
  echo "$APP/Dockerfile doesn't exist." >&2
  exit 1
fi
if [ -n "$(git status --porcelain)" ]; then
  echo "Commit your changes first, so the tag matches what gets built." >&2
  exit 1
fi
git fetch -q origin main
if ! git merge-base --is-ancestor HEAD origin/main; then
  echo "Push this commit to main first, so the tag can be found on GitHub." >&2
  exit 1
fi
if [ "$(git -C "$CONFIG_REPO" branch --show-current)" != main ]; then
  echo "Switch $CONFIG_REPO to main first." >&2
  exit 1
fi
git -C "$CONFIG_REPO" pull -q --ff-only origin main
if ! grep -q 'newTag:' "$CONFIG_REPO/$KUSTOMIZATION"; then
  echo "$KUSTOMIZATION has no newTag to update." >&2
  exit 1
fi

TAG=$(git log -1 --format=%h --abbrev=7 -- "$APP")

# The Mac is arm64 and AldiServer is amd64.
# A tag always means one image, so an existing tag is never built again.
if docker manifest inspect "$IMAGE:$TAG" >/dev/null 2>&1; then
  echo "$IMAGE:$TAG already exists, skipping the build."
else
  docker build --platform linux/amd64 -t "$IMAGE:$TAG" "$APP"
  docker push "$IMAGE:$TAG"
fi

# Quoted, because a tag made only of digits would otherwise be a YAML number.
sed -i '' "s/^\( *newTag:\).*/\1 \"$TAG\"/" "$CONFIG_REPO/$KUSTOMIZATION"
if git -C "$CONFIG_REPO" diff --quiet; then
  echo "$APP:$TAG is already deployed."
  exit 0
fi
git -C "$CONFIG_REPO" commit -q -m "Deploy $APP image $TAG" "$KUSTOMIZATION"
git -C "$CONFIG_REPO" push -q origin main
echo "Deployed $IMAGE:$TAG"
