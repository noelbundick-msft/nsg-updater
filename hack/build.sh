#!/bin/bash
set -euo pipefail

pushd hack
kubectl apply -f rbac.yaml

# # build and run controller
# IMAGE="k3d-registry.localhost:12345/app"
# docker build -t k3d-registry.localhost:12345/nsg-updater .
# docker push k3d-registry.localhost:12345/nsg-updater
# kubectl delete pod nsg-updater --ignore-not-found=true
# kubectl run nsg-updater --image=k3d-registry.localhost:12345/nsg-updater

# run app
kubectl delete pod app --ignore-not-found=true --grace-period=0 --force
kubectl apply -f app.yaml
