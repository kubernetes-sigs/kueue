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

package disaggregatedset

import (
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/component-base/featuregate"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	disaggregatedsetv1 "sigs.k8s.io/lws/api/disaggregatedset/v1"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	utiltestingjobs "sigs.k8s.io/kueue/pkg/util/testingjobs"
	dstesting "sigs.k8s.io/kueue/pkg/util/testingjobs/disaggregatedset"
	testingjobspod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
)

var (
	baseCmpOpts = cmp.Options{
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion"),
	}
)

const (
	testNS = "test-ns"
	testDS = "test-ds"
)

func TestPodSets(t *testing.T) {
	cases := map[string]struct {
		featureGates map[featuregate.Feature]bool
		ds           *disaggregatedsetv1.DisaggregatedSet
		wantPodSets  []kueue.PodSet
	}{
		"basic 2-role DS (prefill worker-only, decode worker-only)": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,
			},
			ds: dstesting.MakeDisaggregatedSet(testDS, testNS).
				Role("prefill", 2, 4).
				Role("decode", 3, 2).
				Obj(),
			wantPodSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet("decode-main", 6).
					RestartPolicy("").
					Image(utiltestingjobs.TestDefaultContainerImage).
					Obj(),
				*utiltestingapi.MakePodSet("prefill-main", 8).
					RestartPolicy("").
					Image(utiltestingjobs.TestDefaultContainerImage).
					Obj(),
			},
		},
		"leader+worker roles (using LeaderTemplate)": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,
			},
			ds: dstesting.MakeDisaggregatedSet(testDS, testNS).
				RoleWithLeader("prefill", 2, 4).
				Obj(),
			wantPodSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet("prefill-leader", 2).
					RestartPolicy("").
					Containers(corev1.Container{
						Name:      "leader",
						Image:     utiltestingjobs.TestDefaultContainerImage,
						Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
					}).
					Obj(),
				*utiltestingapi.MakePodSet("prefill-worker", 6).
					RestartPolicy("").
					Containers(corev1.Container{
						Name:      "worker",
						Image:     utiltestingjobs.TestDefaultContainerImage,
						Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
					}).
					Obj(),
			},
		},
		"slices > 1 multiplies counts": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,
			},
			ds: dstesting.MakeDisaggregatedSet(testDS, testNS).
				Slices(3).
				Role("decode", 2, 4).
				Obj(),
			wantPodSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet("decode-main", 24).
					RestartPolicy("").
					Image(utiltestingjobs.TestDefaultContainerImage).
					Obj(),
			},
		},
		"slices > 1 with leader template": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,
			},
			ds: dstesting.MakeDisaggregatedSet(testDS, testNS).
				Slices(2).
				RoleWithLeader("prefill", 3, 5).
				Obj(),
			wantPodSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet("prefill-leader", 6).
					RestartPolicy("").
					Containers(corev1.Container{
						Name:      "leader",
						Image:     utiltestingjobs.TestDefaultContainerImage,
						Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
					}).
					Obj(),
				*utiltestingapi.MakePodSet("prefill-worker", 24).
					RestartPolicy("").
					Containers(corev1.Container{
						Name:      "worker",
						Image:     utiltestingjobs.TestDefaultContainerImage,
						Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
					}).
					Obj(),
			},
		},
		"mixed roles (one with leader, one without)": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,
			},
			ds: dstesting.MakeDisaggregatedSet(testDS, testNS).
				RoleWithLeader("prefill", 2, 3).
				Role("decode", 4, 2).
				Obj(),
			wantPodSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet("decode-main", 8).
					RestartPolicy("").
					Image(utiltestingjobs.TestDefaultContainerImage).
					Obj(),
				*utiltestingapi.MakePodSet("prefill-leader", 2).
					RestartPolicy("").
					Containers(corev1.Container{
						Name:      "leader",
						Image:     utiltestingjobs.TestDefaultContainerImage,
						Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
					}).
					Obj(),
				*utiltestingapi.MakePodSet("prefill-worker", 4).
					RestartPolicy("").
					Containers(corev1.Container{
						Name:      "worker",
						Image:     utiltestingjobs.TestDefaultContainerImage,
						Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
					}).
					Obj(),
			},
		},
		"leader+worker with size=1 produces only leader PodSet": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,
			},
			ds: dstesting.MakeDisaggregatedSet(testDS, testNS).
				RoleWithLeader("prefill", 2, 1).
				Obj(),
			wantPodSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet("prefill-leader", 2).
					RestartPolicy("").
					Containers(corev1.Container{
						Name:      "leader",
						Image:     utiltestingjobs.TestDefaultContainerImage,
						Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
					}).
					Obj(),
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)

			got, err := podSets(tc.ds)
			if err != nil {
				t.Fatalf("podSets() returned error: %v", err)
			}

			if diff := cmp.Diff(tc.wantPodSets, got, baseCmpOpts...); diff != "" {
				t.Errorf("podSets() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestSetDefault(t *testing.T) {
	const testUID = "test-uid-123"
	wlName := GetWorkloadName(types.UID(testUID), testDS)

	cases := map[string]struct {
		featureGates map[featuregate.Feature]bool
		ds           *disaggregatedsetv1.DisaggregatedSet
		pod          *corev1.Pod
		wantUpdated  bool
		wantPod      *corev1.Pod
	}{
		"sets managed-by label, workload name, role hash for worker-only role": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling:       false,
				features.WorkloadIdentifierAnnotations: false,
			},
			ds: dstesting.MakeDisaggregatedSet(testDS, testNS).
				UID(testUID).
				Role("decode", 2, 4).
				Obj(),
			pod: testingjobspod.MakePod("pod1", testNS).
				Label(disaggregatedsetv1.SetNameLabelKey, testDS).
				Label(disaggregatedsetv1.RoleLabelKey, "decode").
				Obj(),
			wantUpdated: true,
			wantPod: testingjobspod.MakePod("pod1", testNS).
				Label(disaggregatedsetv1.SetNameLabelKey, testDS).
				Label(disaggregatedsetv1.RoleLabelKey, "decode").
				ManagedByKueueLabel().
				GroupNameLabel(wlName).
				PrebuiltWorkloadLabel(wlName).
				GroupTotalCount("8").
				RoleHash("decode-main").
				Obj(),
		},
		"sets leader role hash for leader pod (no LeaderPodNameAnnotation)": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling:       false,
				features.WorkloadIdentifierAnnotations: false,
			},
			ds: dstesting.MakeDisaggregatedSet(testDS, testNS).
				UID(testUID).
				RoleWithLeader("prefill", 2, 3).
				Obj(),
			pod: testingjobspod.MakePod("pod-leader", testNS).
				Label(disaggregatedsetv1.SetNameLabelKey, testDS).
				Label(disaggregatedsetv1.RoleLabelKey, "prefill").
				Obj(),
			wantUpdated: true,
			wantPod: testingjobspod.MakePod("pod-leader", testNS).
				Label(disaggregatedsetv1.SetNameLabelKey, testDS).
				Label(disaggregatedsetv1.RoleLabelKey, "prefill").
				ManagedByKueueLabel().
				GroupNameLabel(wlName).
				PrebuiltWorkloadLabel(wlName).
				GroupTotalCount("6").
				RoleHash("prefill-leader").
				Obj(),
		},
		"sets worker role hash for worker pod (has LeaderPodNameAnnotation)": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling:       false,
				features.WorkloadIdentifierAnnotations: false,
			},
			ds: dstesting.MakeDisaggregatedSet(testDS, testNS).
				UID(testUID).
				RoleWithLeader("prefill", 2, 3).
				Obj(),
			pod: testingjobspod.MakePod("pod-worker", testNS).
				Label(disaggregatedsetv1.SetNameLabelKey, testDS).
				Label(disaggregatedsetv1.RoleLabelKey, "prefill").
				Annotation(leaderworkersetv1.LeaderPodNameAnnotationKey, "pod-leader").
				Obj(),
			wantUpdated: true,
			wantPod: testingjobspod.MakePod("pod-worker", testNS).
				Label(disaggregatedsetv1.SetNameLabelKey, testDS).
				Label(disaggregatedsetv1.RoleLabelKey, "prefill").
				ManagedByKueueLabel().
				GroupNameLabel(wlName).
				PrebuiltWorkloadLabel(wlName).
				Annotation(leaderworkersetv1.LeaderPodNameAnnotationKey, "pod-leader").
				GroupTotalCount("6").
				RoleHash("prefill-worker").
				Obj(),
		},
		"skips pods without role label (LeaderReady tolerance)": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling:       false,
				features.WorkloadIdentifierAnnotations: false,
			},
			ds: dstesting.MakeDisaggregatedSet(testDS, testNS).
				UID(testUID).
				Role("decode", 2, 4).
				Obj(),
			pod: testingjobspod.MakePod("pod-no-role", testNS).
				Label(disaggregatedsetv1.SetNameLabelKey, testDS).
				Obj(),
			wantUpdated: false,
			wantPod: testingjobspod.MakePod("pod-no-role", testNS).
				Label(disaggregatedsetv1.SetNameLabelKey, testDS).
				Obj(),
		},
		"skips already-managed pods": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling:       false,
				features.WorkloadIdentifierAnnotations: false,
			},
			ds: dstesting.MakeDisaggregatedSet(testDS, testNS).
				UID(testUID).
				Role("decode", 2, 4).
				Obj(),
			pod: testingjobspod.MakePod("pod-managed", testNS).
				Label(disaggregatedsetv1.SetNameLabelKey, testDS).
				Label(disaggregatedsetv1.RoleLabelKey, "decode").
				ManagedByKueueLabel().
				Obj(),
			wantUpdated: false,
			wantPod: testingjobspod.MakePod("pod-managed", testNS).
				Label(disaggregatedsetv1.SetNameLabelKey, testDS).
				Label(disaggregatedsetv1.RoleLabelKey, "decode").
				ManagedByKueueLabel().
				Obj(),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)

			r := &Reconciler{}
			pod := tc.pod.DeepCopy()
			got := r.setDefault(tc.ds, pod)

			if got != tc.wantUpdated {
				t.Errorf("setDefault() returned %v, want %v", got, tc.wantUpdated)
			}
			if diff := cmp.Diff(tc.wantPod, pod, baseCmpOpts...); diff != "" {
				t.Errorf("pod after setDefault (-want +got):\n%s", diff)
			}
		})
	}
}

func TestPodSetNameForPod(t *testing.T) {
	cases := map[string]struct {
		roleName string
		role     *disaggregatedsetv1.DisaggregatedRoleSpec
		pod      *corev1.Pod
		want     string
	}{
		"nil role returns main suffix": {
			roleName: "decode",
			role:     nil,
			pod:      testingjobspod.MakePod("pod1", testNS).Obj(),
			want:     "decode-main",
		},
		"role without LeaderTemplate returns main suffix": {
			roleName: "decode",
			role: &disaggregatedsetv1.DisaggregatedRoleSpec{
				Name: "decode",
				LeaderWorkerSetTemplateSpec: leaderworkersetv1.LeaderWorkerSetTemplateSpec{
					Spec: leaderworkersetv1.LeaderWorkerSetSpec{
						LeaderWorkerTemplate: leaderworkersetv1.LeaderWorkerTemplate{
							WorkerTemplate: corev1.PodTemplateSpec{},
						},
					},
				},
			},
			pod:  testingjobspod.MakePod("pod1", testNS).Obj(),
			want: "decode-main",
		},
		"role with LeaderTemplate, worker pod (has LeaderPodNameAnnotation) returns worker suffix": {
			roleName: "prefill",
			role: &disaggregatedsetv1.DisaggregatedRoleSpec{
				Name: "prefill",
				LeaderWorkerSetTemplateSpec: leaderworkersetv1.LeaderWorkerSetTemplateSpec{
					Spec: leaderworkersetv1.LeaderWorkerSetSpec{
						LeaderWorkerTemplate: leaderworkersetv1.LeaderWorkerTemplate{
							LeaderTemplate: &corev1.PodTemplateSpec{},
							WorkerTemplate: corev1.PodTemplateSpec{},
						},
					},
				},
			},
			pod: testingjobspod.MakePod("pod-worker", testNS).
				Annotation(leaderworkersetv1.LeaderPodNameAnnotationKey, "pod-leader").
				Obj(),
			want: "prefill-worker",
		},
		"role with LeaderTemplate, leader pod (no LeaderPodNameAnnotation) returns leader suffix": {
			roleName: "prefill",
			role: &disaggregatedsetv1.DisaggregatedRoleSpec{
				Name: "prefill",
				LeaderWorkerSetTemplateSpec: leaderworkersetv1.LeaderWorkerSetTemplateSpec{
					Spec: leaderworkersetv1.LeaderWorkerSetSpec{
						LeaderWorkerTemplate: leaderworkersetv1.LeaderWorkerTemplate{
							LeaderTemplate: &corev1.PodTemplateSpec{},
							WorkerTemplate: corev1.PodTemplateSpec{},
						},
					},
				},
			},
			pod:  testingjobspod.MakePod("pod-leader", testNS).Obj(),
			want: "prefill-leader",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := podSetNameForPod(tc.roleName, tc.role, tc.pod)
			if got != tc.want {
				t.Errorf("podSetNameForPod() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGetWorkloadName(t *testing.T) {
	name1 := GetWorkloadName("uid-abc", "my-ds")
	name2 := GetWorkloadName("uid-abc", "my-ds")
	if name1 != name2 {
		t.Errorf("GetWorkloadName is not deterministic: %q != %q", name1, name2)
	}

	name3 := GetWorkloadName("uid-xyz", "my-ds")
	if name1 == name3 {
		t.Errorf("GetWorkloadName should produce different names for different UIDs: both = %q", name1)
	}

	name4 := GetWorkloadName("uid-abc", "other-ds")
	if name1 == name4 {
		t.Errorf("GetWorkloadName should produce different names for different owner names: both = %q", name1)
	}
}

func TestReconciler(t *testing.T) {
	const testUID = "test-uid-ds"
	wlName := GetWorkloadName(types.UID(testUID), testDS)
	request := reconcile.Request{NamespacedName: types.NamespacedName{Name: testDS, Namespace: testNS}}

	cases := map[string]struct {
		featureGates     map[featuregate.Feature]bool
		disaggregatedSet *disaggregatedsetv1.DisaggregatedSet
		workloads        []kueue.Workload
		pods             []corev1.Pod
		wantWorkloads    []kueue.Workload
		wantPods         []corev1.Pod
		wantEvents       []utiltesting.EventRecord
		wantErr          error
	}{
		"should create prebuilt workload for worker-only DS": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,
			},
			disaggregatedSet: dstesting.MakeDisaggregatedSet(testDS, testNS).
				UID(testUID).
				Queue("test-queue").
				Role("decode", 2, 4).
				Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(wlName, testNS).
					JobUID(testUID).
					OwnerReference(gvk, testDS, testUID).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testDS).
					Finalizers(kueue.ResourceInUseFinalizerName).
					Queue("test-queue").
					PodSets(
						*utiltestingapi.MakePodSet("decode-main", 8).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj(),
					).
					Priority(0).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: testDS, Namespace: testNS},
					EventType: corev1.EventTypeNormal,
					Reason:    jobframework.ReasonCreatedWorkload,
					Message: fmt.Sprintf(
						"Created Workload: %s/%s",
						testNS,
						wlName,
					),
				},
			},
		},
		"should create prebuilt workload for DS with leader template": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,
			},
			disaggregatedSet: dstesting.MakeDisaggregatedSet(testDS, testNS).
				UID(testUID).
				Queue("test-queue").
				RoleWithLeader("prefill", 2, 3).
				Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(wlName, testNS).
					JobUID(testUID).
					OwnerReference(gvk, testDS, testUID).
					Annotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
					Annotation(constants.JobOwnerGVKAnnotation, gvk.String()).
					Annotation(constants.JobOwnerNameAnnotation, testDS).
					Finalizers(kueue.ResourceInUseFinalizerName).
					Queue("test-queue").
					PodSets(
						*utiltestingapi.MakePodSet("prefill-leader", 2).
							RestartPolicy("").
							Containers(corev1.Container{
								Name:      "leader",
								Image:     utiltestingjobs.TestDefaultContainerImage,
								Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
							}).
							Obj(),
						*utiltestingapi.MakePodSet("prefill-worker", 4).
							RestartPolicy("").
							Containers(corev1.Container{
								Name:      "worker",
								Image:     utiltestingjobs.TestDefaultContainerImage,
								Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
							}).
							Obj(),
					).
					Priority(0).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: testDS, Namespace: testNS},
					EventType: corev1.EventTypeNormal,
					Reason:    jobframework.ReasonCreatedWorkload,
					Message: fmt.Sprintf(
						"Created Workload: %s/%s",
						testNS,
						wlName,
					),
				},
			},
		},
		"should set default labels on pods": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling:       false,
				features.WorkloadIdentifierAnnotations: false,
			},
			disaggregatedSet: dstesting.MakeDisaggregatedSet(testDS, testNS).
				UID(testUID).
				Queue("test-queue").
				Role("decode", 2, 4).
				Obj(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(wlName, testNS).
					JobUID(testUID).
					OwnerReference(gvk, testDS, testUID).
					Finalizers(kueue.ResourceInUseFinalizerName).
					Queue("test-queue").
					PodSets(
						*utiltestingapi.MakePodSet("decode-main", 8).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj(),
					).
					Priority(0).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					Label(disaggregatedsetv1.SetNameLabelKey, testDS).
					Label(disaggregatedsetv1.RoleLabelKey, "decode").
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(wlName, testNS).
					JobUID(testUID).
					OwnerReference(gvk, testDS, testUID).
					Finalizers(kueue.ResourceInUseFinalizerName).
					Queue("test-queue").
					PodSets(
						*utiltestingapi.MakePodSet("decode-main", 8).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj(),
					).
					Priority(0).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod1", testNS).
					Label(disaggregatedsetv1.SetNameLabelKey, testDS).
					Label(disaggregatedsetv1.RoleLabelKey, "decode").
					ManagedByKueueLabel().
					GroupNameLabel(wlName).
					PrebuiltWorkloadLabel(wlName).
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					GroupTotalCount("8").
					RoleHash("decode-main").
					Obj(),
			},
		},
		"should skip pod without role label": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling:       false,
				features.WorkloadIdentifierAnnotations: false,
			},
			disaggregatedSet: dstesting.MakeDisaggregatedSet(testDS, testNS).
				UID(testUID).
				Queue("test-queue").
				Role("decode", 2, 4).
				Obj(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(wlName, testNS).
					JobUID(testUID).
					OwnerReference(gvk, testDS, testUID).
					Finalizers(kueue.ResourceInUseFinalizerName).
					Queue("test-queue").
					PodSets(
						*utiltestingapi.MakePodSet("decode-main", 8).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj(),
					).
					Priority(0).
					Obj(),
			},
			pods: []corev1.Pod{
				*testingjobspod.MakePod("pod-no-role", testNS).
					Label(disaggregatedsetv1.SetNameLabelKey, testDS).
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(wlName, testNS).
					JobUID(testUID).
					OwnerReference(gvk, testDS, testUID).
					Finalizers(kueue.ResourceInUseFinalizerName).
					Queue("test-queue").
					PodSets(
						*utiltestingapi.MakePodSet("decode-main", 8).
							RestartPolicy("").
							Image(utiltestingjobs.TestDefaultContainerImage).
							Obj(),
					).
					Priority(0).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*testingjobspod.MakePod("pod-no-role", testNS).
					Label(disaggregatedsetv1.SetNameLabelKey, testDS).
					Annotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
					Obj(),
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			ctx, _ := utiltesting.ContextWithLog(t)
			clientBuilder := utiltesting.NewClientBuilder(disaggregatedsetv1.AddToScheme)
			indexer := utiltesting.AsIndexer(clientBuilder)

			objs := make([]client.Object, 0, len(tc.workloads)+1)
			if tc.disaggregatedSet != nil {
				objs = append(objs, tc.disaggregatedSet)
			}
			for i := range tc.workloads {
				objs = append(objs, &tc.workloads[i])
			}
			for i := range tc.pods {
				objs = append(objs, &tc.pods[i])
			}

			kClient := clientBuilder.WithObjects(objs...).Build()
			recorder := &utiltesting.EventRecorder{}

			reconciler, err := NewReconciler(ctx, kClient, indexer, recorder)
			if err != nil {
				t.Fatalf("Error creating the reconciler: %v", err)
			}

			_, err = reconciler.Reconcile(ctx, request)
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Reconcile returned error (-want,+got):\n%s", diff)
			}

			gotWorkloads := kueue.WorkloadList{}
			if err := kClient.List(ctx, &gotWorkloads, client.InNamespace(request.Namespace)); err != nil {
				t.Fatalf("Could not list Workloads after reconcile: %v", err)
			}
			if diff := cmp.Diff(tc.wantWorkloads, gotWorkloads.Items, baseCmpOpts...); diff != "" {
				t.Errorf("Workloads after reconcile (-want,+got):\n%s", diff)
			}

			gotPods := corev1.PodList{}
			if err := kClient.List(ctx, &gotPods, client.InNamespace(request.Namespace)); err != nil {
				t.Fatalf("Could not list Pods after reconcile: %v", err)
			}
			if diff := cmp.Diff(tc.wantPods, gotPods.Items, baseCmpOpts...); diff != "" {
				t.Errorf("Pods after reconcile (-want,+got):\n%s", diff)
			}

			if diff := cmp.Diff(tc.wantEvents, recorder.RecordedEvents, cmpopts.SortSlices(utiltesting.SortEvents)); diff != "" {
				t.Errorf("Unexpected events (-want/+got):\n%s", diff)
			}
		})
	}
}
