#!/bin/bash
set -euo pipefail

# run app
pushd hack

# single pod
# kubectl delete pod app --ignore-not-found=true --grace-period=0 --force
# kubectl apply -f serve.yaml

# run a deployment
kubectl apply -f servedeploy.yaml
