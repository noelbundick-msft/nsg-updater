package main

import (
	"encoding/json"
	"os"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/cloud-provider-azure/pkg/provider"
)

func getK8sConfig(kubeconfig string) *rest.Config {
	k8sConfig, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		// failure to get k8s config is a fatal error
		panic(err.Error())
	}
	return k8sConfig
}

func getAzureConfig(azureconfig string) *provider.Config {
	var azConfig *provider.Config

	configFile, err := os.Open(azureconfig)
	if err != nil {
		// failure to get azure config is a fatal error
		panic(err.Error())
	}
	defer configFile.Close()

	jsonParser := json.NewDecoder(configFile)
	err = jsonParser.Decode(&azConfig)
	if err != nil {
		// failure to get azure config is a fatal error
		panic(err.Error())
	}

	return azConfig
}

func getAzureCredential() azcore.TokenCredential {
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		// failure to get Azure credential is a fatal error
		panic(err.Error())
	}
	return cred
}
