package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/cloud-provider-azure/pkg/provider"
)

type HostNetworkNsgController struct {
	informerFactory informers.SharedInformerFactory
	// podInformer     v1informers.PodInformer
	clientset              *kubernetes.Clientset
	azConfig               *provider.Config
	azCreds                azcore.TokenCredential
	limitTimer             *time.Timer
	needsUpdate            *int32
	updateChan             chan struct{}
	limitTimerAllowsUpdate *int32
}

func NewHostNetworkNsgController(k8sConfig *rest.Config, azConfig *provider.Config, azCreds azcore.TokenCredential) *HostNetworkNsgController {
	clientset, err := kubernetes.NewForConfig(k8sConfig)
	if err != nil {
		panic(err.Error())
	}

	informerFactory := informers.NewSharedInformerFactory(clientset, time.Second*60)
	podInformer := informerFactory.Core().V1().Pods()

	c := &HostNetworkNsgController{
		informerFactory:        informerFactory,
		clientset:              clientset,
		azConfig:               azConfig,
		azCreds:                azCreds,
		limitTimer:             time.NewTimer(time.Second * 10),
		limitTimerAllowsUpdate: new(int32),
		needsUpdate:            new(int32),
		updateChan:             make(chan struct{}),
	}

	podInformer.Informer().AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc:    c.podAdd,
			UpdateFunc: c.podUpdate,
			DeleteFunc: c.podDelete,
		},
	)

	go func() {
		for {
			select {
			case <-c.limitTimer.C:
				fmt.Println("Timer cleared for takeoff")
				atomic.StoreInt32(c.limitTimerAllowsUpdate, 1)
			case <-c.updateChan:
				fmt.Println("Update signaled!")
				atomic.StoreInt32(c.needsUpdate, 1)
			}

			// if time has elapsed and there's a pending update - do it
			if atomic.LoadInt32(c.needsUpdate) == 1 && atomic.LoadInt32(c.limitTimerAllowsUpdate) == 1 {
				c.UpdateNSG()
			}
		}
	}()

	return c
}

func (c *HostNetworkNsgController) FlagForUpdate() {
	fmt.Println("Flagging for update")
	c.updateChan <- struct{}{}
}

func (c *HostNetworkNsgController) UpdateNSG() {
	fmt.Println("NSG needs an update!")

	// block updates while we run
	atomic.StoreInt32(c.limitTimerAllowsUpdate, 0)

	nsgId := c.getNsgId()
	fmt.Printf("NSG ID: %s\n", nsgId)

	nsgClient, err := armnetwork.NewSecurityGroupsClient(c.azConfig.SubscriptionID, c.azCreds, nil)
	if err != nil {
		panic(err.Error())
	}

	re := regexp.MustCompile(`^/subscriptions/(?P<subscriptionId>.*)/resourceGroups/(?P<resourceGroup>.*)/providers/Microsoft.Network/networkSecurityGroups/(?P<name>.*)$`)
	match := re.FindStringSubmatch(nsgId)
	result := make(map[string]string)
	for i, name := range re.SubexpNames() {
		if i != 0 && name != "" {
			result[name] = match[i]
		}
	}

	nsg, err := nsgClient.Get(context.TODO(), result["resourceGroup"], result["name"], nil)
	if err != nil {
		panic(err.Error())
	}

	fmt.Printf("existing nsg: %v\n", nsg)

	pods, err := c.clientset.CoreV1().Pods("").List(context.TODO(), metav1.ListOptions{LabelSelector: "updateNSG=true"})
	if err != nil {
		panic(err.Error())
	}
	// we have point-in-time pod info, so we don't have any new data that needs an update yet
	atomic.StoreInt32(c.needsUpdate, 0)

	var rules []*armnetwork.SecurityRule
	
	// keep existing rules that don't start with hostNetwork-
	for _, rule := range nsg.Properties.SecurityRules {
		if !strings.HasPrefix(*rule.Name, "hostNetwork-") {
			rules = append(rules, rule)
		}
	}

	// add calculated hostNetwork rules
	var priority int32 = 2000
	for _, pod := range pods.Items {
		nodeIp, ports := c.getNodeIpAndPorts(&pod)
		if nodeIp == "" {
			fmt.Printf("skipping pod %s/%s, no nodeIP\n", pod.Namespace, pod.Name)
			continue
		}
		fmt.Printf("Adding rule for pod %s/%s - %s:%v\n", pod.Namespace, pod.Name, nodeIp, ports)

		var portRanges []*string
		for _, p := range ports {
			portRanges = append(portRanges, to.Ptr(strconv.Itoa(int(p))))
		}

		rules = append(rules, &armnetwork.SecurityRule{
			Name: to.Ptr(fmt.Sprintf("hostNetwork-%s-%s", pod.Namespace, pod.Name)),
			Properties: &armnetwork.SecurityRulePropertiesFormat{
				Access:                   to.Ptr(armnetwork.SecurityRuleAccessAllow),
				Direction:                to.Ptr(armnetwork.SecurityRuleDirectionInbound),
				Protocol:                 to.Ptr(armnetwork.SecurityRuleProtocolTCP),
				Description:              to.Ptr("hostNetwork for pod X"),
				DestinationAddressPrefix: to.Ptr(nodeIp),
				DestinationPortRanges:    portRanges,
				Priority:                 to.Ptr(priority),
				SourceAddressPrefix:      to.Ptr("*"),
				SourcePortRange:          to.Ptr("*"),
			},
		})

		priority += 10
	}
	nsg.SecurityGroup.Properties.SecurityRules = rules

	// PUT nsg with new rules
	pollerResp, err := nsgClient.BeginCreateOrUpdate(context.TODO(), result["resourceGroup"], result["name"], nsg.SecurityGroup, nil)
	if err != nil {
		panic(err.Error())
	}

	_, err = pollerResp.PollUntilDone(context.TODO(), nil)
	if err != nil {
		panic(err.Error())
	}
	fmt.Println("Updated NSG")

	// we can now retry NSG operations in N seconds
	c.limitTimer.Reset(time.Second * 10)
}

func (c *HostNetworkNsgController) Run(stopCh chan struct{}) {
	c.informerFactory.Start(stopCh)
	c.informerFactory.WaitForCacheSync(stopCh)
	c.FlagForUpdate()
}

func usesHostNetwork(pod *v1.Pod) bool {
	return pod.Spec.HostNetwork && pod.Labels["updateNSG"] == "true" && pod.Spec.NodeName != ""
}

func (c *HostNetworkNsgController) getNodeIpAndPorts(pod *v1.Pod) (string, []int32) {
	var ports []int32
	for _, container := range pod.Spec.Containers {
		for _, port := range container.Ports {
			ports = append(ports, port.ContainerPort)
		}
	}

	externalIp := pod.Status.HostIP

	return externalIp, ports
}

func (c *HostNetworkNsgController) getNsgId() string {
	subnetsClient, err := armnetwork.NewSubnetsClient(c.azConfig.SubscriptionID, c.azCreds, nil)
	if err != nil {
		panic(err.Error())
	}

	// TODO: handle VnetResourceGroup
	subnet, err := subnetsClient.Get(context.TODO(), c.azConfig.ResourceGroup, c.azConfig.VnetName, c.azConfig.SubnetName, &armnetwork.SubnetsClientGetOptions{})
	if err != nil {
		panic(err.Error())
	}
	return *subnet.Properties.NetworkSecurityGroup.ID
}

func (c *HostNetworkNsgController) podAdd(obj interface{}) {
	pod := obj.(*v1.Pod)
	if usesHostNetwork(pod) {
		fmt.Printf("Added: %s/%s\n", pod.Namespace, pod.Name)
		c.FlagForUpdate()
	}
}

func (c *HostNetworkNsgController) podUpdate(old, new interface{}) {
	oldPod := old.(*v1.Pod)
	newPod := new.(*v1.Pod)

	// only run for newly scheduled pods that match our filter
	if usesHostNetwork(newPod) && (oldPod.Spec.NodeName != newPod.Spec.NodeName) {
		fmt.Printf("Updated: %s/%s\n", newPod.Namespace, newPod.Name)
		c.FlagForUpdate()
	}
}

func (c *HostNetworkNsgController) podDelete(obj interface{}) {
	pod := obj.(*v1.Pod)
	if usesHostNetwork(pod) {
		fmt.Printf("Deleted: %s/%s\n", pod.Namespace, pod.Name)
		c.FlagForUpdate()
	}
}

func main() {
	println("Hello, World!")

	kubeconfig := flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
	azureconfig := flag.String("azureconfig", "/etc/kubernetes/azure.json", "absolute path to the azure.json file")
	flag.Parse()

	k8sConfig, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
	if err != nil {
		panic(err.Error())
	}

	var azConfig *provider.Config
	configFile, err := os.Open(*azureconfig)
	if err != nil {
		fmt.Println(err.Error())
	}
	defer configFile.Close()
	jsonParser := json.NewDecoder(configFile)
	jsonParser.Decode(&azConfig)

	azCreds, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		fmt.Println(err.Error())
	}
	controller := NewHostNetworkNsgController(k8sConfig, azConfig, azCreds)

	stop := make(chan struct{})
	defer close(stop)
	controller.Run(stop)

	select {}
}
