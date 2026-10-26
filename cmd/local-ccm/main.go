/*
Copyright 2025 The local-ccm Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"slices"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	"github.com/cozystack/local-ccm/pkg/detector"
	"github.com/cozystack/local-ccm/pkg/node"
)

var (
	nodeName          string
	kubeconfig        string
	internalIPTarget  string
	externalIPTarget  string
	runOnce           bool
	removeTaint       bool
	reconcileInterval time.Duration
)

func init() {
	flag.StringVar(&nodeName, "node-name", os.Getenv("NODE_NAME"), "Name of the node to update (env: NODE_NAME)")
	flag.StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig file (for local testing)")
	flag.StringVar(&internalIPTarget, "internal-ip-target", "", "Target IP for internal IP detection via 'ip route get'. If empty, internal IP detection is disabled")
	flag.StringVar(&externalIPTarget, "external-ip-target", "8.8.8.8", "Target IP for external IP detection via 'ip route get'")
	flag.BoolVar(&runOnce, "run-once", false, "Run once and exit instead of running in a loop")
	flag.BoolVar(&removeTaint, "remove-taint", true, "Remove node.cloudprovider.kubernetes.io/uninitialized taint")
	flag.DurationVar(&reconcileInterval, "reconcile-interval", 10*time.Second, "Interval between reconciliation loops")

	klog.InitFlags(nil)
}

func main() {
	flag.Parse()

	if nodeName == "" {
		klog.Fatal("--node-name or NODE_NAME environment variable must be set")
	}

	klog.Infof("Starting local-ccm for node %s", nodeName)
	klog.V(2).Infof("Configuration: internalIPTarget=%q externalIPTarget=%q",
		internalIPTarget, externalIPTarget)

	// Create Kubernetes client
	k8sClient, err := createKubernetesClient(kubeconfig)
	if err != nil {
		klog.Fatalf("Failed to create Kubernetes client: %v", err)
	}

	// Create node updater
	nodeUpdater := node.NewUpdater(k8sClient, nodeName)

	ctx := context.Background()

	// Main reconciliation loop
	for {
		if err := reconcile(ctx, nodeUpdater); err != nil {
			klog.Errorf("Reconciliation failed: %v", err)
			if runOnce {
				os.Exit(1)
			}
		} else {
			klog.Infof("Reconciliation completed successfully")
			if runOnce {
				os.Exit(0)
			}
		}

		klog.V(2).Infof("Sleeping for %v until next reconciliation", reconcileInterval)
		time.Sleep(reconcileInterval)
	}
}

func reconcile(ctx context.Context, nodeUpdater *node.Updater) error {
	klog.V(2).Infof("Starting reconciliation for node %s", nodeName)

	// Get current node
	currentNode, err := nodeUpdater.GetNode(ctx)
	if err != nil {
		return fmt.Errorf("failed to get node: %w", err)
	}

	// Detect Internal IP if configured
	var detectedInternalIP string
	if internalIPTarget != "" {
		klog.V(3).Infof("Detecting internal IP using target %s", internalIPTarget)
		detectedInternalIP, err = detector.DetectIP(internalIPTarget)
		if err != nil {
			return fmt.Errorf("failed to detect internal IP: %w", err)
		}
		klog.V(2).Infof("Detected internal IP: %s", detectedInternalIP)
	}
	// If internalIPTarget is not set, preserve existing InternalIP (e.g., set by kubelet)

	// Always detect and update External IP
	klog.V(3).Infof("Detecting external IP using target %s", externalIPTarget)
	detectedExternalIP, err := detector.DetectIP(externalIPTarget)
	if err != nil {
		return fmt.Errorf("failed to detect external IP: %w", err)
	}
	klog.V(2).Infof("Detected external IP: %s", detectedExternalIP)

	// Build the desired address list, preserving all existing addresses
	// (including duplicate InternalIP entries on dual-stack nodes) and only
	// managing InternalIP (when internalIPTarget is set) and ExternalIP.
	addresses := computeAddresses(currentNode.Status.Addresses, internalIPTarget, detectedInternalIP, detectedExternalIP)

	// Check if addresses changed
	if addressesEqual(currentNode.Status.Addresses, addresses) {
		klog.V(3).Info("Addresses unchanged, skipping update")
	} else {
		klog.Info("Addresses changed, updating node")
		if err := nodeUpdater.UpdateAddresses(ctx, addresses); err != nil {
			return fmt.Errorf("failed to update addresses: %w", err)
		}
	}

	// Remove taint if requested
	if removeTaint {
		if err := nodeUpdater.RemoveTaint(ctx); err != nil {
			return fmt.Errorf("failed to remove taint: %w", err)
		}
	}

	return nil
}

func createKubernetesClient(kubeconfigPath string) (kubernetes.Interface, error) {
	var restConfig *rest.Config
	var err error

	if kubeconfigPath != "" {
		klog.V(2).Infof("Using kubeconfig from %s", kubeconfigPath)
		restConfig, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	} else {
		klog.V(2).Info("Using in-cluster config")
		restConfig, err = rest.InClusterConfig()
	}

	if err != nil {
		return nil, fmt.Errorf("failed to create rest config: %w", err)
	}

	client, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	return client, nil
}

// computeAddresses builds the desired node address list from the current
// addresses plus the detected IPs. It preserves all existing address entries
// (including duplicate InternalIP values present on dual-stack nodes and any
// Hostname entry) and only manages InternalIP (when internalIPTarget is set)
// and ExternalIP.
//
//   - When internalIPTarget != "", local-ccm owns InternalIP: all existing
//     InternalIP entries are removed and replaced by a single entry holding
//     detectedInternalIP. When internalIPTarget == "", the kubelet's
//     InternalIP entries are preserved as-is.
//   - When detectedExternalIP equals any InternalIP present in the result,
//     any existing ExternalIP entry is removed and none is added. Otherwise
//     exactly one ExternalIP entry with detectedExternalIP is ensured
//     (existing ExternalIP entries are replaced).
//   - All other address types (Hostname, etc.) are left untouched.
func computeAddresses(current []v1.NodeAddress, internalIPTarget, detectedInternalIP, detectedExternalIP string) []v1.NodeAddress {
	result := make([]v1.NodeAddress, 0, len(current)+1)

	// Copy through everything except InternalIP/ExternalIP, which we manage.
	for _, addr := range current {
		if addr.Type == v1.NodeInternalIP || addr.Type == v1.NodeExternalIP {
			continue
		}
		result = append(result, addr)
	}

	// InternalIP: when internalIPTarget is set we own it (single entry).
	// Otherwise preserve all existing InternalIP entries (dual-stack safe).
	var internalIPs []string
	if internalIPTarget != "" {
		internalIPs = []string{detectedInternalIP}
		result = append(result, v1.NodeAddress{Type: v1.NodeInternalIP, Address: detectedInternalIP})
	} else {
		for _, addr := range current {
			if addr.Type == v1.NodeInternalIP {
				internalIPs = append(internalIPs, addr.Address)
				result = append(result, addr)
			}
		}
	}

	// ExternalIP: only add when it doesn't duplicate an InternalIP. Any
	// pre-existing ExternalIP was already dropped above, so a duplicate
	// case leaves no ExternalIP in the result.
	if !slices.Contains(internalIPs, detectedExternalIP) {
		result = append(result, v1.NodeAddress{Type: v1.NodeExternalIP, Address: detectedExternalIP})
	}

	return result
}

// addressesEqual reports whether two address slices contain the same
// addresses regardless of order. It performs a proper multiset comparison
// so that duplicate NodeAddressType values (e.g. two InternalIP entries on a
// dual-stack node) are compared correctly.
func addressesEqual(a, b []v1.NodeAddress) bool {
	if len(a) != len(b) {
		return false
	}

	counts := make(map[[2]string]int, len(a))
	for _, addr := range a {
		counts[[2]string{string(addr.Type), addr.Address}]++
	}
	for _, addr := range b {
		key := [2]string{string(addr.Type), addr.Address}
		counts[key]--
		if counts[key] < 0 {
			return false
		}
	}

	return true
}
