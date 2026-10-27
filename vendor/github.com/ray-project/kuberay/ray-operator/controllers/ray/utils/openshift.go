// TODO: This file is a transitional shim for OpenShift-specific Route creation. Once Gateway API
// support is mature and the Route-based backward compatibility window closes, this file and all
// associated OpenShift-specific code paths should be removed. See the roadmap summary at:
// https://github.com/ray-project/kuberay/pull/4365#issuecomment-4143407845

package utils

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
)

// openShiftAPIGroups lists the API groups used to detect an OpenShift cluster.
// We check for specific well-known groups rather than using a broad suffix match
// (e.g. strings.HasSuffix(".openshift.io")) to avoid false positives from custom
// CRDs that happen to use the openshift.io domain but don't indicate a real
// OpenShift installation. Any one of these groups being present is sufficient.
var openShiftAPIGroups = []string{
	"route.openshift.io",
	"security.openshift.io",
	"config.openshift.io",
}

// IsOpenShiftCluster detects if the cluster is OpenShift by checking for OpenShift-specific API groups.
// This function is called once at operator startup and the result is stored in reconciler options.
// Returns an error if cluster type cannot be determined, since downstream behavior (e.g. Route
// vs Ingress creation) depends on this check being accurate.
func IsOpenShiftCluster(config *rest.Config) (bool, error) {
	if config == nil {
		return false, fmt.Errorf("REST config is nil")
	}

	discoveryClient, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return false, fmt.Errorf("failed to create discovery client: %w", err)
	}

	apiGroups, err := discoveryClient.ServerGroups()
	if err != nil {
		return false, fmt.Errorf("failed to retrieve server API groups: %w", err)
	}

	for _, group := range apiGroups.Groups {
		if slices.Contains(openShiftAPIGroups, group.Name) {
			return true, nil
		}
	}

	return false, nil
}

// ShouldUseIngressOnOpenShift determines if Ingress should be used instead of Route on OpenShift.
func ShouldUseIngressOnOpenShift() bool {
	return strings.ToLower(os.Getenv(USE_INGRESS_ON_OPENSHIFT)) == "true"
}
