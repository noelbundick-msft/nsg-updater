package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"k8s.io/client-go/util/homedir"
)

var rootCmd = &cobra.Command{
	Use:   "nsg-updater",
	Short: "Azure NSG utility",
	Long:  `Automates Azure NSG updates based on Kubernetes resources`,
	Run: func(cmd *cobra.Command, args []string) {
		controller := NewNsgController(
			getK8sConfig(kubeconfig),
			getAzureConfig(azureconfig),
			getAzureCredential(),
		)
		controller.Run()
		select {}
	},
}

var kubeconfig string
var azureconfig string

func init() {
	rootCmd.PersistentFlags().StringVar(&kubeconfig, "kubeconfig", filepath.Join(homedir.HomeDir(), ".kube", "config"), "Path to kubeconfig file")
	rootCmd.PersistentFlags().StringVar(&azureconfig, "azureconfig", "/etc/kubernetes/azure.json", "Path to azure.json file")
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
