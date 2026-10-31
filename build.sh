#!/usr/bin/env bash
# Builds and pushes the image tagged with the current commit, then writes that tag
# to the manifests in AldiDevOps, where ArgoCD picks it up.
set -euo pipefail

IMAGE=ghcr.io/aldicovo/cv-aldi-lukito
CONFIG_REPO=${CONFIG_REPO:-$HOME/aldi/AldiDevOps}
KUSTOMIZATION=CV_Aldi_Lukito/kustomization.yaml

cd "$(dirname "$0")"

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

TAG=$(git rev-parse --short=7 HEAD)

# The Mac is arm64 and AldiServer is amd64.
docker build --platform linux/amd64 -t "$IMAGE:$TAG" .
docker push "$IMAGE:$TAG"

# Quoted, because a tag made only of digits would otherwise be a YAML number.
sed -i '' "s/^\( *newTag:\).*/\1 \"$TAG\"/" "$CONFIG_REPO/$KUSTOMIZATION"
if git -C "$CONFIG_REPO" diff --quiet; then
  echo "$TAG is already deployed."
  exit 0
fi
git -C "$CONFIG_REPO" commit -q -m "Deploy CV image $TAG" "$KUSTOMIZATION"
git -C "$CONFIG_REPO" push -q origin main
echo "Deployed $IMAGE:$TAG"
