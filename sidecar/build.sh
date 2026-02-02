#!/bin/bash
set -e

VERSION=${1:-v2.0.0}
REGISTRY="imageregistry-shared-alpha-registry.ap-southeast-5.cr.aliyuncs.com"
IMAGE_NAME="mekariengineering/traefik-datadog-sidecar"
FULL_IMAGE="${REGISTRY}/${IMAGE_NAME}:${VERSION}"

echo "Building sidecar image: ${FULL_IMAGE}"

cd "$(dirname "$0")"

go mod tidy

docker buildx build --platform linux/amd64 -t "${FULL_IMAGE}" .

echo ""
echo "Build complete! Image: ${FULL_IMAGE}"
echo ""
echo "To push to registry:"
echo "  docker push ${FULL_IMAGE}"
echo ""
echo "To update Traefik deployment:"
echo "  Update values-staging.yaml image tag to: ${VERSION}"
echo "  Then sync ArgoCD application"
