#!/bin/bash
set -euo pipefail

CLUSTERS=$(k3d cluster list -o json | jq 'length')
if [ $CLUSTERS -eq '0' ]; then
  k3d cluster create --registry-create k3d-registry.localhost:12345
else
  k3d cluster start
fi
