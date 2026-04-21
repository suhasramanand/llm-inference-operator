#!/usr/bin/env bash
set -euo pipefail

echo "Installing CRDs..."
make install

echo "Deploying controller..."
make deploy IMG=controller:latest

