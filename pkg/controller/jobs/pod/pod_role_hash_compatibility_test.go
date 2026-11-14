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

package pod

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/podset"
	"sigs.k8s.io/kueue/pkg/util/expectations"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
)

func TestFindMatchingWorkloadsLegacySidecarRoles(t *testing.T) {
	base := testingpod.MakePod("old", "ns").UID("old-uid").
		GroupNameLabel("group").GroupTotalCount("1").
		Request(corev1.ResourceCPU, "500m").KueueFinalizer()
	base.Spec.InitContainers = []corev1.Container{{
		Name: "helper", RestartPolicy: ptr.To(corev1.ContainerRestartPolicyAlways),
		Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
			corev1.ResourceCPU: resource.MustParse("500m"),
		}},
	}}
	legacySpec := base.Spec.DeepCopy()
	legacySpec.InitContainers[0].RestartPolicy = nil
	legacyRole, err := utilpod.GenerateRoleHash(legacySpec)
	if err != nil {
		t.Fatal(err)
	}
	failed := base.Clone().RoleHash(legacyRole).StatusPhase(corev1.PodFailed)
	replacement := base.Clone().Name("replacement").UID("replacement-uid").KueueSchedulingGate()
	changed := replacement.Clone()
	changed.Spec.InitContainers[0].Resources.Requests[corev1.ResourceCPU] = resource.MustParse("600m")
	ungated := replacement.Clone()
	ungated.Spec.SchedulingGates = nil

	cases := map[string]struct {
		oldPod          *corev1.Pod
		replacement     *corev1.Pod
		count           int32
		wantReplacement bool
		templateSpec    *corev1.PodSpec
		workloadRole    string
		prebuilt        bool
		patchConflict   bool
		skipDefault     bool
	}{
		"failed legacy sidecar is replaced": {
			oldPod: failed.Clone().Obj(), replacement: replacement.Clone().Obj(), count: 1, wantReplacement: true,
		},
		"running legacy sibling keeps its reservation": {
			oldPod:      base.Clone().RoleHash(legacyRole).StatusPhase(corev1.PodRunning).Obj(),
			replacement: replacement.Clone().Obj(), count: 2, wantReplacement: true,
		},
		"replacement works after every legacy pod disappears": {
			replacement: replacement.Clone().Obj(), count: 1, wantReplacement: true,
		},
		"explicit custom role is not rewritten": {
			oldPod: failed.Clone().Obj(), replacement: replacement.Clone().RoleHash("custom-role").Obj(), count: 1,
		},
		"different sidecar requests are not matched": {
			oldPod: failed.Clone().Obj(), replacement: changed.Obj(), count: 1,
		},
		"an already ungated pod is not rewritten": {
			oldPod: failed.Clone().Obj(), replacement: ungated.Obj(), count: 1,
		},
		"sidecar cannot inherit a regular init reservation": {
			oldPod: failed.Clone().Obj(), replacement: replacement.Clone().Obj(), count: 1, templateSpec: legacySpec,
		},
		"an explicit Workload role is not treated as a legacy automatic role": {
			oldPod: failed.Clone().RoleHash("custom-role").Obj(), replacement: replacement.Clone().Obj(), count: 1, workloadRole: "custom-role",
		},
		"prebuilt equivalent role is preserved": {
			replacement: replacement.Clone().Obj(), count: 1, wantReplacement: true, prebuilt: true,
		},
		"unannotated replacement inherits the persisted role": {
			replacement: replacement.Clone().Obj(), count: 1, wantReplacement: true, skipDefault: true,
		},
		"unannotated running Pod keeps its existing reservation": {
			replacement: ungated.Clone().StatusPhase(corev1.PodRunning).Obj(), count: 1, wantReplacement: true, skipDefault: true,
		},
		"fast admission replacement uses the legacy role": {
			replacement: replacement.Clone().
				Annotation(podconstants.GroupFastAdmissionAnnotationKey, podconstants.GroupFastAdmissionAnnotationValue).Obj(),
			count: 1, wantReplacement: true, prebuilt: true,
		},
		"a conflicting Pod update stops matching before ungating": {
			oldPod: failed.Clone().Obj(), replacement: replacement.Clone().Obj(), count: 1, patchConflict: true,
		},
	}
	for _, verifyRequests := range []bool{false, true} {
		for name, tc := range cases {
			t.Run(name+"/verifyRequests="+strconv.FormatBool(verifyRequests), func(t *testing.T) {
				features.SetFeatureGateDuringTest(t, features.PodIntegrationVerifyRoleRequests, verifyRequests)
				ctx, _ := utiltesting.ContextWithLog(t)
				pod := FromObject(tc.replacement.DeepCopy())
				if !tc.skipDefault {
					if err := pod.addRoleHash(); err != nil {
						t.Fatal(err)
					}
				}
				originalRole := pod.pod.Annotations[podconstants.RoleHashAnnotation]
				pod.isGroup = true
				pod.key = types.NamespacedName{Namespace: "ns", Name: "group"}
				pod.satisfiedExcessPods = true
				pod.excessPodExpectations = expectations.NewStore("legacySidecarRoles")
				pod.list.Items = []corev1.Pod{pod.pod}
				if tc.oldPod != nil {
					pod.list.Items = append(pod.list.Items, *tc.oldPod.DeepCopy())
				}
				workloadRole := legacyRole
				if tc.workloadRole != "" {
					workloadRole = tc.workloadRole
				}
				templateSpec := base.Spec.DeepCopy()
				if tc.templateSpec != nil {
					templateSpec = tc.templateSpec.DeepCopy()
				}
				wl := utiltestingapi.MakeWorkload("group", "ns").UID("existing-workload").Group().
					PodSets(*utiltestingapi.MakePodSet(kueue.NewPodSetReference(workloadRole), int(tc.count)).
						PodSpec(*templateSpec).Obj()).Obj()
				cl := utiltesting.NewClientBuilder().WithObjects(wl).
					WithLists(&corev1.PodList{Items: pod.list.Items}).
					WithInterceptorFuncs(interceptor.Funcs{Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
						if tc.patchConflict && obj.GetName() == "replacement" {
							return apierrors.NewConflict(schema.GroupResource{Resource: "pods"}, obj.GetName(), errors.New("changed concurrently"))
						}
						return c.Patch(ctx, obj, patch, opts...)
					}}).Build()
				recorder := &utiltesting.EventRecorder{}
				var matched *kueue.Workload
				var toDelete []*kueue.Workload
				var err error
				if tc.prebuilt {
					var equivalent bool
					equivalent, err = pod.EquivalentToWorkload(ctx, cl, wl)
					if equivalent {
						matched = wl
					}
				} else {
					matched, toDelete, err = pod.FindMatchingWorkloads(ctx, cl, recorder)
				}
				if tc.patchConflict {
					if !apierrors.IsConflict(err) {
						t.Fatalf("error = %v, want Conflict", err)
					}
					persisted := &corev1.Pod{}
					if err := cl.Get(ctx, client.ObjectKeyFromObject(tc.replacement), persisted); err != nil {
						t.Fatal(err)
					}
					if !isGated(persisted) || persisted.Annotations[podconstants.RoleHashAnnotation] != originalRole {
						t.Fatal("conflicting update changed or ungated the persisted Pod")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				keptReplacement := slices.ContainsFunc(pod.list.Items, func(p corev1.Pod) bool { return p.Name == "replacement" })
				if keptReplacement != tc.wantReplacement {
					t.Fatalf("replacement kept = %t, want %t (old role %s, new role %s)", keptReplacement, tc.wantReplacement, legacyRole, originalRole)
				}
				persisted := &corev1.Pod{}
				if err := cl.Get(ctx, client.ObjectKeyFromObject(tc.replacement), persisted); err != nil {
					t.Fatal(err)
				}
				wantRole := originalRole
				if tc.wantReplacement {
					wantRole = legacyRole
					if matched == nil || matched.UID != wl.UID || len(toDelete) != 0 {
						t.Fatalf("existing Workload must remain matched, got workload %v, toDelete %v", matched, toDelete)
					}
					if err := pod.Run(ctx, cl, matched, []podset.PodSetInfo{{Name: kueue.NewPodSetReference(legacyRole)}}, recorder, ""); err != nil {
						t.Fatal(err)
					}
					if err := cl.Get(ctx, client.ObjectKeyFromObject(tc.replacement), persisted); err != nil {
						t.Fatal(err)
					}
					if isGated(persisted) {
						t.Error("equivalent replacement remains gated")
					}
					if tc.oldPod != nil && tc.oldPod.Status.Phase == corev1.PodFailed {
						old := &corev1.Pod{}
						if err := cl.Get(ctx, client.ObjectKeyFromObject(tc.oldPod), old); err != nil {
							t.Fatal(err)
						}
						if slices.Contains(old.Finalizers, podconstants.PodFinalizer) {
							t.Error("replaced Failed Pod still has its Kueue finalizer")
						}
					}
				}
				if got := persisted.Annotations[podconstants.RoleHashAnnotation]; got != wantRole {
					t.Errorf("persisted role = %q, want %q", got, wantRole)
				}
			})
		}
	}
}
