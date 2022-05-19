package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strconv"
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
	clientset   *kubernetes.Clientset
	azConfig    *provider.Config
	azCreds     azcore.TokenCredential
	limitTimer *time.Timer
	needsUpdate *int32
	updateChan chan struct{}
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
		informerFactory: informerFactory,
		clientset:   clientset,
		azConfig:    azConfig,
		azCreds:     azCreds,
		limitTimer: time.NewTimer(time.Second * 10),
		limitTimerAllowsUpdate: new(int32),
		needsUpdate: new(int32),
		updateChan: make(chan struct{}),
	}

	podInformer.Informer().AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc: c.podAdd,
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

	pods, err := c.clientset.CoreV1().Pods("").List(context.TODO(), metav1.ListOptions{LabelSelector: "updateNSG=true"})
	if err != nil {
		panic(err.Error())
	}
	// we have point-in-time pod info, so we don't have any new data that needs an update yet
	atomic.StoreInt32(c.needsUpdate, 0)

	//TODO: enumerate over all pods, build up desired rules
	for _, pod := range pods.Items {
		nodeIp, ports := c.getNodeIpAndPorts(&pod)
		fmt.Printf("Updating NSG for pod %s/%s - %s:%v\n", pod.Namespace, pod.Name, nodeIp, ports)
	}

	nsgId := c.getNsgId()
	fmt.Printf("NSG ID: %s\n", nsgId)

	// c.updateNsg(nsgId, nodeIp, ports)
	// // TODO: evaluate nodes & see what needs NSG updates added or removed

	// we can now retry NSG operations in N seconds
	c.limitTimer.Reset(time.Second * 10)
	atomic.StoreInt32(c.limitTimerAllowsUpdate, 0)

	// // TODO: calculate nsg rules
	// // TODO: execute NSG update (needs to be batched / buffered / rate-limited)

}

func (c *HostNetworkNsgController) Run(stopCh chan struct{}) {
	c.informerFactory.Start(stopCh)
	c.informerFactory.WaitForCacheSync(stopCh)
}

func usesHostNetwork(pod *v1.Pod) bool {
	return pod.Spec.HostNetwork && pod.Annotations["updateNSG"] == "true" && pod.Spec.NodeName != ""
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

func (c *HostNetworkNsgController) updateNsg(nsgId string, host string, ports []int32) {
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

	// TODO: compare current rules vs new/expected
	rules := nsg.SecurityGroup.Properties.SecurityRules
	var priority int32 = 2000
	for _, p := range ports {
		rules = append(rules, &armnetwork.SecurityRule{
			Name: to.Ptr(fmt.Sprintf("hostNetwork-%s-%d", host, p)),
			Properties: &armnetwork.SecurityRulePropertiesFormat{
				Access:                   to.Ptr(armnetwork.SecurityRuleAccessAllow),
				Direction:                to.Ptr(armnetwork.SecurityRuleDirectionInbound),
				Protocol:                 to.Ptr(armnetwork.SecurityRuleProtocolTCP),
				Description:              to.Ptr("hostNetwork for pod X"),
				DestinationAddressPrefix: to.Ptr(host),
				DestinationPortRange:     to.Ptr(strconv.Itoa(int(p))),
				Priority:                 to.Ptr(priority),
				SourceAddressPrefix:      to.Ptr("*"),
				SourcePortRange:          to.Ptr("*"),
			},
		})
		priority++
	}
	// TODO: PUT nsg with new rules
}

func (c *HostNetworkNsgController) podAdd(obj interface{}) {
	pod := obj.(*v1.Pod)
	if usesHostNetwork(pod) {
		fmt.Printf("Added: %s/%s\n", pod.Namespace, pod.Name)
		c.FlagForUpdate()
	}
}

func (c *HostNetworkNsgController) podUpdate(old, new interface{}) {
	// NOOP: there are no updateable fields on a Pod that would affect NSG rules

	// oldPod := old.(*v1.Pod)
	// newPod := new.(*v1.Pod)

	// if usesHostNetwork(oldPod) || usesHostNetwork(newPod) {
	// 	fmt.Printf("Updated: %s/%s\n", newPod.Namespace, newPod.Name)
	// 	c.FlagForUpdate()
	// }
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
