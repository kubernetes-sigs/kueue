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

package provisioning

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	autoscaling "k8s.io/autoscaler/cluster-autoscaler/apis/provisioningrequest/autoscaling.x-k8s.io/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	"sigs.k8s.io/kueue/pkg/util/slices"
)

const (
	RequestsOwnedByWorkloadKey     = "metadata.ownedByWorkload"
	WorkloadsWithAdmissionCheckKey = "status.admissionChecks"
	AdmissionCheckUsingConfigKey   = "spec.provisioningRequestConfig"
)

var (
	configGVK = kueue.GroupVersion.WithKind(ConfigKind)
)

func indexRequestsOwner(obj client.Object) []string {
	refs := obj.GetOwnerReferences()
	if len(refs) == 0 {
		return nil
	}
	return slices.Map(refs, func(r *metav1.OwnerReference) string { return r.Name })
}

func indexWorkloadsChecks(obj client.Object) []string {
	wl, isWl := obj.(*kueue.Workload)
	if !isWl || len(wl.Status.AdmissionChecks) == 0 {
		return nil
	}
	return slices.Map(wl.Status.AdmissionChecks, func(c *kueue.AdmissionCheckState) string { return string(c.Name) })
}

func SetupIndexer(ctx context.Context, indexer client.FieldIndexer) error {
	if err := indexer.IndexField(ctx, &autoscaling.ProvisioningRequest{}, RequestsOwnedByWorkloadKey, indexRequestsOwner); err != nil {
		return fmt.Errorf("setting index on provisionRequest owner: %w", err)
	}
	if err := indexer.IndexField(ctx, &kueue.Workload{}, WorkloadsWithAdmissionCheckKey, indexWorkloadsChecks); err != nil {
		return fmt.Errorf("setting index on workloads checks: %w", err)
	}

	if err := indexer.IndexField(ctx, &kueue.AdmissionCheck{}, AdmissionCheckUsingConfigKey, admissioncheck.IndexerByConfigFunction(kueue.ProvisioningRequestControllerName, configGVK)); err != nil {
		return fmt.Errorf("setting index on admission checks config: %w", err)
	}
	return nil
}

func ServerSupportsProvisioningRequest(mgr manager.Manager) error {
	gvk, err := apiutil.GVKForObject(&autoscaling.ProvisioningRequest{}, mgr.GetScheme())
	if err != nil {
		return err
	}
	if _, err = mgr.GetRESTMapper().RESTMapping(gvk.GroupKind(), gvk.Version); err != nil {
		return err
	}
	return nil
}
