# nsg-updater

This is a hack project. It updates Azure NSGs to expose pods that are running with host networking

## Behavior 

* Watches for Pods with `spec.hostNetwork=true` and `labels.updateNSG=true`
* Waits for a rate limit period as to not thrash NSGs in Azure
* When an update is flagged, list eligible Pods and build up a list of desired NSG rules
* Merge with the existing NSG rules
* Update the NSG in Azure

## Configuration

* --kubeconfig: path to a kubeconfig for cluster api access (defaults to ~/.kube/config)
* --azureconfig: path to an azure cloud provider JSON config file (defaults to /etc/kubernetes/azure.json)

## TODO

* run it from inside an AKS cluster
  * configure RBAC / ServiceAccount
  * mount /etc/kubernetes/azure.json and pass to the --azureconfig param
