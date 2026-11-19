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

package leaderworkerset

import (
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/component-base/featuregate"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingleaderworkerset "sigs.k8s.io/kueue/pkg/util/testingjobs/leaderworkerset"
)

type effectiveRequestsGates struct {
	allowMutatingContainers   bool
	validateEffectiveRequests bool
}

func (g effectiveRequestsGates) String() string {
	return fmt.Sprintf("%s=%t,%s=%t",
		features.AllowMutatingContainersWhenEffectiveRequestsDontChange, g.allowMutatingContainers,
		features.ValidateEffectiveRequestsOnPodTemplateUpdate, g.validateEffectiveRequests)
}

var (
	bothGatesEnabled      = effectiveRequestsGates{allowMutatingContainers: true, validateEffectiveRequests: true}
	allowMutatingDisabled = effectiveRequestsGates{allowMutatingContainers: false, validateEffectiveRequests: true}
	validateDisabled      = effectiveRequestsGates{allowMutatingContainers: true, validateEffectiveRequests: false}
	bothGatesDisabled     = effectiveRequestsGates{allowMutatingContainers: false, validateEffectiveRequests: false}
)

func TestValidateUpdateEffectiveRequests(t *testing.T) {
	workerTemplateForbidden := field.ErrorList{
		&field.Error{
			Type:  field.ErrorTypeForbidden,
			Field: workerTemplatePath.Child("spec").String(),
		},
	}.ToAggregate()
	leaderTemplateForbidden := field.ErrorList{
		&field.Error{
			Type:  field.ErrorTypeForbidden,
			Field: leaderTemplatePath.Child("spec").String(),
		},
	}.ToAggregate()
	workerRequestsImmutable := field.ErrorList{
		&field.Error{
			Type:  field.ErrorTypeInvalid,
			Field: workerTemplatePath.Child("spec", "containers").Index(0).Child("resources", "requests").String(),
		},
	}.ToAggregate()

	testCases := map[string]struct {
		objs    []runtime.Object
		oldObj  *leaderworkersetv1.LeaderWorkerSet
		newObj  *leaderworkersetv1.LeaderWorkerSet
		wantErr map[effectiveRequestsGates]error
	}{
		"raise container request when managed": {
			oldObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				Queue("test-queue").
				Request(corev1.ResourceCPU, "1").
				Obj(),
			newObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				Queue("test-queue").
				Request(corev1.ResourceCPU, "2").
				Obj(),
			wantErr: map[effectiveRequestsGates]error{
				bothGatesEnabled:      workerTemplateForbidden,
				allowMutatingDisabled: workerRequestsImmutable,
				validateDisabled:      workerTemplateForbidden,
				bothGatesDisabled:     workerRequestsImmutable,
			},
		},
		"turn on the host network with a container port when managed": {
			oldObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				Queue("test-queue").
				Request(corev1.ResourceCPU, "1").
				WorkerTemplateSpecContainer(*utiltesting.MakeContainer().Name("proxy").Port(8080, 0, corev1.ProtocolTCP).Obj()).
				Obj(),
			newObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				Queue("test-queue").
				Request(corev1.ResourceCPU, "1").
				WorkerTemplateSpecContainer(*utiltesting.MakeContainer().Name("proxy").Port(8080, 0, corev1.ProtocolTCP).Obj()).
				WorkerTemplateSpecHostNetwork().
				Obj(),
		},
		"change init container to a sidecar container when managed": {
			oldObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				Queue("test-queue").
				Request(corev1.ResourceCPU, "1").
				WorkerTemplateSpecInitContainer(*utiltesting.MakeContainer().Name("init").WithResourceReq(corev1.ResourceCPU, "500m").Obj()).
				Obj(),
			newObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				Queue("test-queue").
				Request(corev1.ResourceCPU, "1").
				WorkerTemplateSpecInitContainer(*utiltesting.MakeContainer().Name("init").AsSidecar().WithResourceReq(corev1.ResourceCPU, "500m").Obj()).
				Obj(),
			wantErr: map[effectiveRequestsGates]error{
				bothGatesEnabled:      workerTemplateForbidden,
				allowMutatingDisabled: workerTemplateForbidden,
			},
		},
		"raise init container limit without request when managed": {
			oldObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				Queue("test-queue").
				Request(corev1.ResourceCPU, "1").
				WorkerTemplateSpecInitContainer(*utiltesting.MakeContainer().Name("init").WithResourceLimit(corev1.ResourceCPU, "1").Obj()).
				Obj(),
			newObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				Queue("test-queue").
				Request(corev1.ResourceCPU, "1").
				WorkerTemplateSpecInitContainer(*utiltesting.MakeContainer().Name("init").WithResourceLimit(corev1.ResourceCPU, "2").Obj()).
				Obj(),
			wantErr: map[effectiveRequestsGates]error{
				bothGatesEnabled:      workerTemplateForbidden,
				allowMutatingDisabled: workerTemplateForbidden,
			},
		},
		"change init container limit without request below the container request when managed": {
			oldObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				Queue("test-queue").
				Request(corev1.ResourceCPU, "2").
				WorkerTemplateSpecInitContainer(*utiltesting.MakeContainer().Name("init").WithResourceLimit(corev1.ResourceCPU, "1").Obj()).
				Obj(),
			newObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				Queue("test-queue").
				Request(corev1.ResourceCPU, "2").
				WorkerTemplateSpecInitContainer(*utiltesting.MakeContainer().Name("init").WithResourceLimit(corev1.ResourceCPU, "1500m").Obj()).
				Obj(),
		},
		"add limit without request to container when managed": {
			oldObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				Queue("test-queue").
				Request(corev1.ResourceCPU, "1").
				Obj(),
			newObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				Queue("test-queue").
				Request(corev1.ResourceCPU, "1").
				Limit(corev1.ResourceMemory, "1Gi").
				Obj(),
			wantErr: map[effectiveRequestsGates]error{
				bothGatesEnabled:      workerTemplateForbidden,
				allowMutatingDisabled: workerTemplateForbidden,
			},
		},
		"remove limit without request from container when managed": {
			oldObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				Queue("test-queue").
				Request(corev1.ResourceCPU, "1").
				Limit(corev1.ResourceMemory, "1Gi").
				Obj(),
			newObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				Queue("test-queue").
				Request(corev1.ResourceCPU, "1").
				Obj(),
			wantErr: map[effectiveRequestsGates]error{
				bothGatesEnabled:      workerTemplateForbidden,
				allowMutatingDisabled: workerTemplateForbidden,
			},
		},
		"change limit of a requested resource in container when managed": {
			oldObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				Queue("test-queue").
				Request(corev1.ResourceCPU, "1").
				Limit(corev1.ResourceCPU, "2").
				Obj(),
			newObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				Queue("test-queue").
				Request(corev1.ResourceCPU, "1").
				Limit(corev1.ResourceCPU, "3").
				Obj(),
		},
		"add limit that equals the LimitRange default request to container when managed": {
			objs: []runtime.Object{
				utiltesting.MakeLimitRange("limits", "").WithValue("DefaultRequest", corev1.ResourceMemory, "1Gi").Obj(),
			},
			oldObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				Queue("test-queue").
				Request(corev1.ResourceCPU, "1").
				Obj(),
			newObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				Queue("test-queue").
				Request(corev1.ResourceCPU, "1").
				Limit(corev1.ResourceMemory, "1Gi").
				Obj(),
		},
		"add pod-level limit without request when managed": {
			oldObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				Queue("test-queue").
				Request(corev1.ResourceCPU, "1").
				PodLevelRequest(corev1.ResourceCPU, "2").
				Obj(),
			newObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				Queue("test-queue").
				Request(corev1.ResourceCPU, "1").
				PodLevelRequest(corev1.ResourceCPU, "2").
				PodLevelLimit(corev1.ResourceMemory, "4Gi").
				Obj(),
			wantErr: map[effectiveRequestsGates]error{
				bothGatesEnabled:      workerTemplateForbidden,
				allowMutatingDisabled: workerTemplateForbidden,
			},
		},
		"raise pod-level limit without request when managed": {
			oldObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				Queue("test-queue").
				Request(corev1.ResourceCPU, "1").
				PodLevelLimit(corev1.ResourceMemory, "4Gi").
				Obj(),
			newObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				Queue("test-queue").
				Request(corev1.ResourceCPU, "1").
				PodLevelLimit(corev1.ResourceMemory, "8Gi").
				Obj(),
			wantErr: map[effectiveRequestsGates]error{
				bothGatesEnabled:      workerTemplateForbidden,
				allowMutatingDisabled: workerTemplateForbidden,
			},
		},
		"raise pod-level limit of a resource that the container requests when managed": {
			oldObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				Queue("test-queue").
				Request(corev1.ResourceCPU, "1").
				PodLevelLimit(corev1.ResourceCPU, "4").
				Obj(),
			newObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				Queue("test-queue").
				Request(corev1.ResourceCPU, "1").
				PodLevelLimit(corev1.ResourceCPU, "6").
				Obj(),
		},
		"change pod-level limit of a requested resource when managed": {
			oldObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				Queue("test-queue").
				Request(corev1.ResourceCPU, "1").
				PodLevelRequest(corev1.ResourceCPU, "2").
				PodLevelLimit(corev1.ResourceCPU, "4").
				Obj(),
			newObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				Queue("test-queue").
				Request(corev1.ResourceCPU, "1").
				PodLevelRequest(corev1.ResourceCPU, "2").
				PodLevelLimit(corev1.ResourceCPU, "6").
				Obj(),
		},
		"add limit without request to the leader container when managed": {
			oldObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{*utiltesting.MakeContainer().Name("leader").WithResourceReq(corev1.ResourceCPU, "1").Obj()}}}).
				Queue("test-queue").
				Obj(),
			newObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{*utiltesting.MakeContainer().Name("leader").WithResourceReq(corev1.ResourceCPU, "1").WithResourceLimit(corev1.ResourceMemory, "1Gi").Obj()}}}).
				Queue("test-queue").
				Obj(),
			wantErr: map[effectiveRequestsGates]error{
				bothGatesEnabled:      leaderTemplateForbidden,
				allowMutatingDisabled: leaderTemplateForbidden,
			},
		},
		"add leader pod-level limit without request when managed": {
			oldObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{Spec: corev1.PodSpec{
					Containers: []corev1.Container{*utiltesting.MakeContainer().Name("leader").WithResourceReq(corev1.ResourceCPU, "1").Obj()},
					Resources:  &corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")}},
				}}).
				Queue("test-queue").
				Obj(),
			newObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{Spec: corev1.PodSpec{
					Containers: []corev1.Container{*utiltesting.MakeContainer().Name("leader").WithResourceReq(corev1.ResourceCPU, "1").Obj()},
					Resources: &corev1.ResourceRequirements{
						Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")},
						Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("4Gi")},
					},
				}}).
				Queue("test-queue").
				Obj(),
			wantErr: map[effectiveRequestsGates]error{
				bothGatesEnabled:      leaderTemplateForbidden,
				allowMutatingDisabled: leaderTemplateForbidden,
			},
		},
		"raise request of a resource with a pod-level limit when managed": {
			oldObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				Queue("test-queue").
				Request(corev1.ResourceCPU, "1").
				PodLevelLimit(corev1.ResourceCPU, "4").
				Obj(),
			newObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				Queue("test-queue").
				Request(corev1.ResourceCPU, "3").
				PodLevelLimit(corev1.ResourceCPU, "4").
				Obj(),
			wantErr: map[effectiveRequestsGates]error{
				bothGatesEnabled:      workerTemplateForbidden,
				allowMutatingDisabled: workerRequestsImmutable,
				bothGatesDisabled:     workerRequestsImmutable,
			},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			for _, gates := range []effectiveRequestsGates{bothGatesEnabled, allowMutatingDisabled, validateDisabled, bothGatesDisabled} {
				t.Run(gates.String(), func(t *testing.T) {
					features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{
						features.AllowMutatingContainersWhenEffectiveRequestsDontChange: gates.allowMutatingContainers,
						features.ValidateEffectiveRequestsOnPodTemplateUpdate:           gates.validateEffectiveRequests,
					})
					client := utiltesting.NewClientBuilder(leaderworkersetv1.AddToScheme).
						WithIndex(&corev1.LimitRange{}, indexer.LimitRangeHasContainerOrPodType, indexer.IndexLimitRangeHasContainerOrPodType).
						WithRuntimeObjects(tc.objs...).
						Build()
					wh := &Webhook{integrationManager: newTestIntegrationManager(t), client: client}

					ctx, _ := utiltesting.ContextWithLog(t)
					warns, err := wh.ValidateUpdate(ctx, tc.oldObj, tc.newObj)
					if diff := cmp.Diff(tc.wantErr[gates], err, cmpopts.IgnoreFields(field.Error{}, "BadValue", "Detail")); diff != "" {
						t.Errorf("Unexpected error (-want,+got):\n%s", diff)
					}
					if len(warns) != 0 {
						t.Errorf("Unexpected warnings: %v", warns)
					}
				})
			}
		})
	}
}
