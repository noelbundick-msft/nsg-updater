# Demo

From the repo root

```shell
# Create a debug pod to capture the azure.json from a node
kubectl get node
kubectl debug node/aks-nodepool1-33214860-vmss000000 -it --image=alpine
```

Inside the debug pod

```shell
# chroot to the host file system
chroot /host

# Dump the azure.json. Capture this and overwrite the sample one in the repo /hack folder
cat /etc/kubernetes/azure.json
exit  # exit chroot
exit  # exit debug container
```

In your terminal again

```shell
# delete the debug container
kubectl delete pod node-debugger-aks-nodepool1-33214860-vmss000000-8v6bc

# Create a service principal
SP=$(az ad sp create-for-rbac --role 'Network Contributor' --scopes '/subscriptions/09b47131-a043-4831-bbf4-df9aa0c8f970/resourceGroups/MC_trash1-aks_trash1_westus3')

# use the following values for /deploy/nsg-updater.yaml
echo AZURE_TENANT_ID: $(echo $SP | jq -j .tenant | base64)
echo AZURE_CLIENT_ID: $(echo $SP | jq -j .appId | base64)
echo AZURE_CLIENT_SECRET: $(echo $SP | jq -j .password | base64)

# deploy nsg-updater
kubectl apply -f ./deploy/nsg-updater.yaml

# check on the controller - make sure it hasn't crashed
kubectl get pod -l=app=nsg-updater

# watch controller logs
kubectl logs nsg-updater-64b549b9d9-sg62r -f

# deploy an app that uses hostNetwork: true
kubectl apply -f ./hack/serve.yaml

# notice that the NSG rules have been instantly updated both in logs and in Azure
# go change the replica count in serve.yaml and deploy again
kubectl apply -f ./hack/serve.yaml
```
