#!/usr/bin/env bash
set -euo pipefail

KIND_CLUSTER_NAME="${KIND_CLUSTER_NAME:-llm-inference}"

kind get clusters | grep -q "^${KIND_CLUSTER_NAME}$" && {
  echo "kind cluster ${KIND_CLUSTER_NAME} already exists"
  exit 0
}

kind create cluster --name "${KIND_CLUSTER_NAME}"
echo "created kind cluster ${KIND_CLUSTER_NAME}"

