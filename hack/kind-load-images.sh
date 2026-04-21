#!/usr/bin/env bash
set -euo pipefail

KIND_CLUSTER_NAME="${KIND_CLUSTER_NAME:-llm-inference}"

echo "Building images..."
make docker-build docker-build-router docker-build-mock-worker

echo "Loading images into kind..."
kind load docker-image --name "${KIND_CLUSTER_NAME}" controller:latest llm-inference-router:latest llm-inference-mock-worker:latest

