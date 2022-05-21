package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"sigs.k8s.io/cloud-provider-azure/pkg/provider"
)

const NsgUpdateRateLimit = time.Second * 10
const TargetPodLabel = "updateNSG"
const NSGRulePrefix = "hostNetwork"
const InitialNsgRulePriority = 2000
const NsgRulePriorityStep = 10

type NsgController struct {
	k8s     *kubernetes.Clientset
	network *NetworkClient
	updates chan struct{}
}

func NewNsgController(k8sConfig *rest.Config, azureConfig *provider.Config, azureCredential azcore.TokenCredential) *NsgController {
	client, err := kubernetes.NewForConfig(k8sConfig)
	if err != nil {
		// failure to build a k8s client is a fatal error
		panic(err.Error())
	}

	controller := &NsgController{
		k8s:     client,
		network: NewNetworkClient(azureConfig, azureCredential),
		updates: make(chan struct{}),
	}
	return controller
}

func (c *NsgController) Run() {
	fmt.Println("NsgController started")

	ctx := context.TODO()
	c.watchPods(ctx)
	c.listenForEvents()
}

func (c *NsgController) watchPods(ctx context.Context) {
	informerFactory := informers.NewSharedInformerFactory(c.k8s, time.Second*60)
	podInformer := informerFactory.Core().V1().Pods()
	podInformer.Informer().AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc:    c.podAdd,
			UpdateFunc: c.podUpdate,
			DeleteFunc: c.podDelete,
		},
	)

	informerFactory.Start(ctx.Done())
	informerFactory.WaitForCacheSync(ctx.Done())
}

func (c *NsgController) listenForEvents() {
	timer := time.NewTimer(NsgUpdateRateLimit)
	go func() {
		needsUpdate := true
		updateAllowed := true

		for {
			select {
			case <-timer.C:
				fmt.Println("NsgUpdateRateLimit cleared")
				updateAllowed = true
			case <-c.updates:
				fmt.Println("Update signaled!")
				needsUpdate = true
			}

			// if time has elapsed and there's a pending update - do it
			if needsUpdate && updateAllowed {
				// block updates while we process rules
				updateAllowed = false
				needsUpdate = false

				fmt.Println("Updating NSG...")
				c.updateNSG()

				// we can perform more updates after the rate limit
				timer.Reset(NsgUpdateRateLimit)
			}
		}
	}()

	c.signalUpdate("Updating rules at startup")
}

func (c *NsgController) podAdd(obj interface{}) {
	pod := obj.(*v1.Pod)
	if usesHostNetwork(pod) {
		c.signalUpdate("Added: %s/%s", pod.Namespace, pod.Name)
	}
}

func (c *NsgController) podUpdate(old, new interface{}) {
	oldPod := old.(*v1.Pod)
	newPod := new.(*v1.Pod)

	// only run for newly scheduled pods that match our filter
	if usesHostNetwork(newPod) && (oldPod.Spec.NodeName != newPod.Spec.NodeName) {
		c.signalUpdate("Updated: %s/%s", newPod.Namespace, newPod.Name)
	}
}

func (c *NsgController) podDelete(obj interface{}) {
	pod := obj.(*v1.Pod)
	if usesHostNetwork(pod) {
		c.signalUpdate("Deleted: %s/%s", pod.Namespace, pod.Name)
	}
}

func (c *NsgController) signalUpdate(reasonFormat string, a ...any) {
	fmt.Printf(reasonFormat, a...)
	fmt.Print("\n")
	c.updates <- struct{}{}
}

func (c *NsgController) updateNSG() {
	nsg := c.network.GetNsg()
	nsg.Properties.SecurityRules = filterPrefixedRules(nsg.Properties.SecurityRules)
	pods := getTargetPods(c)
	nsg.Properties.SecurityRules = append(nsg.Properties.SecurityRules, generateRules(pods)...)
	c.network.UpdateNsg(nsg)
}

func getTargetPods(c *NsgController) *v1.PodList {
	labelSelector := fmt.Sprintf("%s=true", TargetPodLabel)
	pods, err := c.k8s.CoreV1().Pods("").List(context.TODO(), metav1.ListOptions{LabelSelector: labelSelector})
	if err != nil {
		// can't update NSG if we can't list pods
		panic(err.Error())
	}
	return pods
}

func usesHostNetwork(pod *v1.Pod) bool {
	return pod.Spec.HostNetwork && pod.Labels[TargetPodLabel] == "true" && pod.Spec.NodeName != ""
}

func filterPrefixedRules(current []*armnetwork.SecurityRule) []*armnetwork.SecurityRule {
	// keep existing rules that don't start with our prefix
	rules := []*armnetwork.SecurityRule{}
	for _, rule := range rules {
		if !strings.HasPrefix(*rule.Name, NSGRulePrefix) {
			rules = append(rules, rule)
		}
	}
	return rules
}

func generateRules(pods *v1.PodList) []*armnetwork.SecurityRule {
	rules := []*armnetwork.SecurityRule{}
	// add calculated hostNetwork rules
	var priority int32 = InitialNsgRulePriority
	for _, pod := range pods.Items {
		if !usesHostNetwork(&pod) {
			// a pod used our label but doesn't actually use hostNetwork: true
			continue
		}

		nodeIp, ports := getNodeIpAndPorts(&pod)
		if nodeIp == "" {
			// TODO: convert all printf's to logging calls
			fmt.Printf("skipping pod %s/%s, no nodeIP\n", pod.Namespace, pod.Name)
			continue
		}

		// TODO: convert all printf's to logging calls
		fmt.Printf("Adding rule for pod %s/%s - %s:%v\n", pod.Namespace, pod.Name, nodeIp, ports)

		var portRanges []*string
		for _, p := range ports {
			portRanges = append(portRanges, to.Ptr(strconv.Itoa(int(p))))
		}

		rules = append(rules, &armnetwork.SecurityRule{
			Name: to.Ptr(fmt.Sprintf("%s-%s-%s", NSGRulePrefix, pod.Namespace, pod.Name)),
			Properties: &armnetwork.SecurityRulePropertiesFormat{
				Access:                   to.Ptr(armnetwork.SecurityRuleAccessAllow),
				Direction:                to.Ptr(armnetwork.SecurityRuleDirectionInbound),
				Protocol:                 to.Ptr(armnetwork.SecurityRuleProtocolUDP),
				Description:              to.Ptr(fmt.Sprintf("hostNetwork for pod %s/%s", pod.Namespace, pod.Name)),
				DestinationAddressPrefix: to.Ptr(nodeIp),
				DestinationPortRanges:    portRanges,
				Priority:                 to.Ptr(priority),
				SourceAddressPrefix:      to.Ptr("*"),
				SourcePortRange:          to.Ptr("*"),
			},
		})

		priority += NsgRulePriorityStep
	}
	return rules
}

func getNodeIpAndPorts(pod *v1.Pod) (string, []int32) {
	var ports []int32
	for _, container := range pod.Spec.Containers {
		for _, port := range container.Ports {
			ports = append(ports, port.ContainerPort)
		}
	}

	externalIp := pod.Status.HostIP

	return externalIp, ports
}
