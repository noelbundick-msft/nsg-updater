#!/bin/bash
set -euo pipefail

# run app
pushd hack
kubectl delete pod app --ignore-not-found=true --grace-period=0 --force
kubectl apply -f serve.yaml
