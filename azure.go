package main

import (
	"context"
	"fmt"
	"regexp"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork"
	"sigs.k8s.io/cloud-provider-azure/pkg/provider"
)

type NetworkClient struct {
	config *provider.Config
	credential azcore.TokenCredential
}

func NewNetworkClient(config *provider.Config, credential azcore.TokenCredential) *NetworkClient {
	return &NetworkClient{
		config: config,
		credential: credential,
	}
}

func (c *NetworkClient) GetNsg() armnetwork.SecurityGroup {
	ctx := context.TODO()
	subnetsClient, err := armnetwork.NewSubnetsClient(c.config.SubscriptionID, c.credential, nil)
	if err != nil {
		// failure to build a client is a fatal error
		panic(err.Error())
	}

	// TODO: investigate multiple node pools and differences in azure.json. We may need multiple configs
	// TODO: handle VnetResourceGroup
	subnet, err := subnetsClient.Get(ctx, c.config.ResourceGroup, c.config.VnetName, c.config.SubnetName, &armnetwork.SubnetsClientGetOptions{})
	if err != nil {
		// failure to read the subnet is a fatal error
		panic(err.Error())
	}

	// TODO: move to verbose logs
	fmt.Printf("Found nsgId: %s\n", *subnet.Properties.NetworkSecurityGroup.ID)

	nsgClient, err := armnetwork.NewSecurityGroupsClient(c.config.SubscriptionID, c.credential, nil)
	if err != nil {
		// failure to build a client is a fatal error
		panic(err.Error())
	}

	resourceGroup, name := parseResourceId(*subnet.Properties.NetworkSecurityGroup.ID)
	nsg, err := nsgClient.Get(ctx, resourceGroup, name, nil)
	if err != nil {
		// failure to get the NSG is a fatal error
		panic(err.Error())
	}

	return nsg.SecurityGroup
}

func (c *NetworkClient) UpdateNsg(nsg armnetwork.SecurityGroup) {
	ctx := context.TODO()

	nsgClient, err := armnetwork.NewSecurityGroupsClient(c.config.SubscriptionID, c.credential, nil)
	if err != nil {
		// failure to build a client is a fatal error
		panic(err.Error())
	}

	// PUT nsg with new rules
	resourceGroup, name := parseResourceId(*nsg.ID)
	pollerResp, err := nsgClient.BeginCreateOrUpdate(ctx, resourceGroup, name, nsg, nil)
	if err != nil {
		// failure to update the NSG is a fatal error
		panic(err.Error())
	}
	_, err = pollerResp.PollUntilDone(ctx, nil)
	if err != nil {
		// failure to update the NSG is a fatal error
		panic(err.Error())
	}

	fmt.Println("Updated NSG")
}

func parseResourceId(resourceId string) (resourceGroup string, name string) {
	re := regexp.MustCompile(`^/subscriptions/(?P<subscriptionId>.*)/resourceGroups/(?P<resourceGroup>.*)/providers/(?P<provider>.*)/(?P<type>.*)/(?P<name>.*)$`)
	match := re.FindStringSubmatch(resourceId)
	result := make(map[string]string)
	for i, name := range re.SubexpNames() {
		if i != 0 && name != "" {
			result[name] = match[i]
		}
	}
	return result["resourceGroup"], result["name"]
}
