/*
Copyright The Kubernetes Authors.

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

package capacitybuffer

import (
	"context"
	"fmt"

	autoscaling "k8s.io/autoscaler/cluster-autoscaler/apis/capacitybuffer/autoscaling.x-k8s.io/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
)

const (
	AdmissionCheckUsingConfigKey = "spec.capacityBufferConfig"
)

var configGVK = kueuealpha.SchemeGroupVersion.WithKind("CapacityBufferConfig")

func SetupIndexer(ctx context.Context, indexer client.FieldIndexer) error {
	if err := indexer.IndexField(ctx, &kueue.AdmissionCheck{}, AdmissionCheckUsingConfigKey, admissioncheck.IndexerByConfigFunction(kueuealpha.CapacityBufferControllerName, configGVK)); err != nil {
		return fmt.Errorf("setting index on admission checks config: %w", err)
	}
	return nil
}

// ServerSupportsCapacityBuffer returns an error if the CapacityBuffer API, or
// the CapacityBufferConfig API, is not served. CapacityBufferConfig is an alpha
// API, so its CRD is only installed when the alpha APIs are enabled.
func ServerSupportsCapacityBuffer(mgr manager.Manager) error {
	for _, obj := range []client.Object{&autoscaling.CapacityBuffer{}, &kueuealpha.CapacityBufferConfig{}} {
		gvk, err := apiutil.GVKForObject(obj, mgr.GetScheme())
		if err != nil {
			return err
		}
		if _, err := mgr.GetRESTMapper().RESTMapping(gvk.GroupKind(), gvk.Version); err != nil {
			return err
		}
	}
	return nil
}

// admissionChecksUsingConfig returns the names of the CapacityBuffer
// AdmissionChecks whose parameters reference the given config.
func admissionChecksUsingConfig(ctx context.Context, c client.Client, config string) ([]string, error) {
	acs := &kueue.AdmissionCheckList{}
	if err := c.List(ctx, acs, client.MatchingFields{AdmissionCheckUsingConfigKey: config}); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(acs.Items))
	for i := range acs.Items {
		names = append(names, acs.Items[i].Name)
	}
	return names, nil
}
