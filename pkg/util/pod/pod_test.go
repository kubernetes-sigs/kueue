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
	"math"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/component-base/featuregate"
	"k8s.io/component-base/metrics/testutil"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	testingmetrics "sigs.k8s.io/kueue/pkg/util/testing/metrics"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
)

func TestHasGate(t *testing.T) {
	basePod := testingpod.MakePod("", "")

	testCases := map[string]struct {
		gateName string
		pod      *corev1.Pod
		want     bool
	}{
		"scheduling gate present": {
			gateName: "example.com/gate",
			pod: basePod.Clone().
				Gate("example.com/gate").
				Obj(),
			want: true,
		},
		"another gate present": {
			gateName: "example.com/gate",
			pod: basePod.Clone().
				Gate("example.com/gate2").
				Obj(),
			want: false,
		},
		"no scheduling gates": {
			pod:  basePod.DeepCopy(),
			want: false,
		},
	}

	for desc, tc := range testCases {
		t.Run(desc, func(t *testing.T) {
			got := HasGate(tc.pod, tc.gateName)
			if got != tc.want {
				t.Errorf("Unexpected result: want=%v, got=%v", tc.want, got)
			}
		})
	}
}

func TestUngate(t *testing.T) {
	basePod := testingpod.MakePod("", "")

	testCases := map[string]struct {
		gateName string
		pod      *corev1.Pod
		wantPod  *corev1.Pod
		want     bool
	}{
		"ungate when scheduling gate present": {
			gateName: "example.com/gate",
			pod: basePod.Clone().
				Gate("example.com/gate").
				Obj(),
			wantPod: basePod.DeepCopy(),
			want:    true,
		},
		"ungate when scheduling gate missing": {
			gateName: "example.com/gate",
			pod: basePod.Clone().
				Gate("example.com/gate2").
				Obj(),
			wantPod: basePod.Clone().
				Gate("example.com/gate2").
				Obj(),
			want: false,
		},
	}
	for desc, tc := range testCases {
		t.Run(desc, func(t *testing.T) {
			got := Ungate(tc.pod, tc.gateName)
			if got != tc.want {
				t.Errorf("Unexpected result: want=%v, got=%v", tc.want, got)
			}
			if diff := cmp.Diff(tc.wantPod.Spec.SchedulingGates, tc.pod.Spec.SchedulingGates, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Unexpected scheduling gates\ndiff=%s", diff)
			}
		})
	}
}

func TestGate(t *testing.T) {
	basePod := testingpod.MakePod("", "")

	testCases := map[string]struct {
		gateName string
		pod      *corev1.Pod
		wantPod  *corev1.Pod
		want     bool
	}{
		"gate when scheduling gate present": {
			gateName: "example.com/gate",
			pod: basePod.Clone().
				Gate("example.com/gate").
				Obj(),
			wantPod: basePod.Clone().
				Gate("example.com/gate").
				Obj(),
			want: false,
		},
		"gate when scheduling gate missing": {
			gateName: "example.com/gate",
			pod: basePod.Clone().
				Gate("example.com/gate2").
				Obj(),
			wantPod: basePod.Clone().
				Gate("example.com/gate", "example.com/gate2").
				Obj(),
			want: true,
		},
	}

	for desc, tc := range testCases {
		t.Run(desc, func(t *testing.T) {
			got := Gate(tc.pod, tc.gateName)
			if got != tc.want {
				t.Errorf("Unexpected result: want=%v, got=%v", tc.want, got)
			}
			if diff := cmp.Diff(tc.wantPod.Spec.SchedulingGates, tc.pod.Spec.SchedulingGates, cmpopts.SortSlices(func(a, b corev1.PodSchedulingGate) bool {
				return a.Name < b.Name
			})); diff != "" {
				t.Errorf("Unexpected scheduling gates\ndiff=%s", diff)
			}
		})
	}
}

func TestReadUIntFromLabel(t *testing.T) {
	basePod := testingpod.MakePod("pod", "ns")

	testCases := map[string]struct {
		obj     client.Object
		label   string
		max     int
		wantVal *int
		wantErr error
	}{
		"label not found": {
			obj:     basePod.DeepCopy(),
			label:   "label",
			max:     math.MaxInt,
			wantErr: ErrLabelNotFound,
		},
		"valid label value": {
			obj: basePod.Clone().
				Label("label", "1000").
				Obj(),
			label:   "label",
			max:     math.MaxInt,
			wantVal: new(1000),
		},
		"invalid label value": {
			obj: basePod.Clone().
				Label("label", "value").
				Obj(),
			label:   "label",
			wantErr: ErrInvalidUInt,
		},
		"less than zero": {
			obj: basePod.Clone().
				Label("label", "-1").
				Obj(),
			label:   "label",
			wantErr: ErrInvalidUInt,
		},
		"equal to bound": {
			obj: basePod.Clone().
				Label("label", "1001").
				Obj(),
			label:   "label",
			max:     1001,
			wantErr: ErrValidation,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			gotValue, gotErr := ReadUIntFromLabelBelowBound(tc.obj, tc.label, tc.max)

			if diff := cmp.Diff(tc.wantVal, gotValue); diff != "" {
				t.Errorf("Unexpected value (-want,+got):\n%s", diff)
			}

			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Reconcile returned error (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestIsTerminated(t *testing.T) {
	basePod := testingpod.MakePod("", "")

	cases := map[string]struct {
		pod            *corev1.Pod
		wantTerminated bool
	}{
		"pod is failed": {
			pod: basePod.Clone().
				StatusPhase(corev1.PodFailed).
				Obj(),
			wantTerminated: true,
		},
		"pod is succeeded": {
			pod: basePod.Clone().
				StatusPhase(corev1.PodSucceeded).
				Obj(),
			wantTerminated: true,
		},
		"pod is running": {
			pod: basePod.Clone().
				StatusPhase(corev1.PodRunning).
				Obj(),
			wantTerminated: false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := IsTerminated(tc.pod)
			if tc.wantTerminated != got {
				t.Errorf("Unexpected Pod terminal\nwant: %v\ngot: %v\n", tc.wantTerminated, got)
			}
		})
	}
}

func TestSpecShape(t *testing.T) {
	podResources := &corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
		Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")},
	}
	cases := map[string]struct {
		podSpec       *corev1.PodSpec
		wantResources any
		wantPresent   bool
	}{
		"pod-level resources omitted when unset": {
			podSpec:     &corev1.PodSpec{Containers: []corev1.Container{{Name: "c"}}},
			wantPresent: false,
		},
		"pod-level resources included when set": {
			podSpec:       &corev1.PodSpec{Containers: []corev1.Container{{Name: "c"}}, Resources: podResources},
			wantResources: podResources.Requests,
			wantPresent:   true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, present := SpecShape(tc.podSpec)["resources"]
			if present != tc.wantPresent {
				t.Fatalf("Unexpected presence of \"resources\" key: want %v, got %v", tc.wantPresent, present)
			}
			if diff := cmp.Diff(tc.wantResources, got); diff != "" {
				t.Errorf("Unexpected resources shape (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestGenerateRoleHash(t *testing.T) {
	withoutPodResources := &corev1.PodSpec{Containers: []corev1.Container{{Name: "c"}}}
	withPodResources := &corev1.PodSpec{
		Containers: []corev1.Container{{Name: "c"}},
		Resources: &corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
		},
	}

	baseHash, err := GenerateRoleHash(withoutPodResources)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	cases := map[string]struct {
		podSpec      *corev1.PodSpec
		wantBaseHash bool
	}{
		"pod-level resources change the role hash": {
			podSpec: withPodResources,
		},
		"spec without pod-level resources has a stable role hash": {
			podSpec:      withoutPodResources.DeepCopy(),
			wantBaseHash: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			gotHash, err := GenerateRoleHash(tc.podSpec)
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}
			if gotBaseHash := gotHash == baseHash; gotBaseHash != tc.wantBaseHash {
				t.Errorf("Unexpected role hash comparison with base hash: want equality %t, base hash %q, got hash %q", tc.wantBaseHash, baseHash, gotHash)
			}
		})
	}
}

func TestRecordPodSchedulingGateRemovalSecondsReplicaRole(t *testing.T) {
	const (
		gateName = "example.com/gate"
		cqName   = kueue.ClusterQueueReference("cq")
	)

	now := time.Now().Truncate(time.Second)

	cases := map[string]struct {
		admitted  bool
		tracker   *roletracker.RoleTracker
		wantRole  string
		wantCount uint64
	}{
		"nil tracker records the standalone role": {
			admitted:  true,
			tracker:   nil,
			wantRole:  roletracker.RoleStandalone,
			wantCount: 1,
		},
		"leader tracker records the leader role": {
			admitted:  true,
			tracker:   roletracker.NewFakeRoleTracker(roletracker.RoleLeader),
			wantRole:  roletracker.RoleLeader,
			wantCount: 1,
		},
		"follower tracker records the follower role": {
			admitted:  true,
			tracker:   roletracker.NewFakeRoleTracker(roletracker.RoleFollower),
			wantRole:  roletracker.RoleFollower,
			wantCount: 1,
		},
		"no sample is recorded for a non-admitted workload": {
			admitted:  false,
			tracker:   nil,
			wantRole:  roletracker.RoleStandalone,
			wantCount: 0,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			metrics.PodSchedulingGateRemovalSeconds.Reset()

			wl := utiltestingapi.MakeWorkload("wl", corev1.NamespaceDefault).
				ReserveQuotaAt(utiltestingapi.MakeAdmission(cqName).Obj(), now.Add(-2*time.Second)).
				AdmittedAt(tc.admitted, now.Add(-2*time.Second)).
				Obj()

			RecordPodSchedulingGateRemovalSeconds(testingclock.NewFakeClock(now), gateName, wl, false, nil, tc.tracker)

			count, err := testutil.GetHistogramMetricCount(
				metrics.PodSchedulingGateRemovalSeconds.WithLabelValues(gateName, string(cqName), "false", tc.wantRole),
			)
			if err != nil {
				t.Fatalf("Error getting PodSchedulingGateRemovalSeconds metric count: %v", err)
			}
			if count != tc.wantCount {
				t.Errorf("Unexpected metric count for role %q: want %d, got %d", tc.wantRole, tc.wantCount, count)
			}

			if tc.wantCount > 0 {
				seconds, err := testutil.GetHistogramMetricValue(
					metrics.PodSchedulingGateRemovalSeconds.WithLabelValues(gateName, string(cqName), "false", tc.wantRole),
				)
				if err != nil {
					t.Fatalf("Error getting PodSchedulingGateRemovalSeconds metric value: %v", err)
				}
				if seconds != 2 {
					t.Errorf("Unexpected metric value for role %q: want 2, got %f", tc.wantRole, seconds)
				}
			}
		})
	}
}

func TestGetPodGroupName(t *testing.T) {
	cases := map[string]struct {
		featureGates map[featuregate.Feature]bool
		pod          *corev1.Pod
		want         string
	}{
		"pod without group name": {
			pod: testingpod.MakePod("pod", "ns").Obj(),
		},
		"pod with group name label": {
			pod: testingpod.MakePod("pod", "ns").
				GroupNameLabel("group-1").
				Obj(),
			want: "group-1",
		},
		"pod with group name annotation and WorkloadIdentifierAnnotations enabled": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: true},
			pod: testingpod.MakePod("pod", "ns").
				Annotation(podconstants.GroupNameAnnotation, "group-2").
				Obj(),
			want: "group-2",
		},
		"pod with group name annotation and WorkloadIdentifierAnnotations disabled": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			pod: testingpod.MakePod("pod", "ns").
				Annotation(podconstants.GroupNameAnnotation, "group-2").
				Obj(),
			want: "",
		},
		"pod with both label and annotation and WorkloadIdentifierAnnotations enabled prefers annotation": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: true},
			pod: testingpod.MakePod("pod", "ns").
				GroupNameLabel("group-from-label").
				Annotation(podconstants.GroupNameAnnotation, "group-from-annotation").
				Obj(),
			want: "group-from-annotation",
		},
		"pod with both label and empty annotation and WorkloadIdentifierAnnotations enabled falls back to label": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: true},
			pod: testingpod.MakePod("pod", "ns").
				GroupNameLabel("group-from-label").
				Annotation(podconstants.GroupNameAnnotation, "").
				Obj(),
			want: "group-from-label",
		},
		"pod with both label and annotation and WorkloadIdentifierAnnotations disabled uses label": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			pod: testingpod.MakePod("pod", "ns").
				GroupNameLabel("group-from-label").
				Annotation(podconstants.GroupNameAnnotation, "group-from-annotation").
				Obj(),
			want: "group-from-label",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			got := GetPodGroupName(tc.pod)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Unexpected group name (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestRecordPodSchedulingGateRemovalSeconds(t *testing.T) {
	const (
		gateName = "example.com/gate"
		cqName   = kueue.ClusterQueueReference("cq")
	)

	now := time.Now().Truncate(time.Second)

	cases := map[string]struct {
		admittedAt  time.Time
		wantSeconds float64
	}{
		"controller clock ahead of the admitted transition": {
			admittedAt:  now.Add(-3 * time.Second),
			wantSeconds: 3,
		},
		"controller clock behind the admitted transition is clamped to zero": {
			admittedAt:  now.Add(3 * time.Second),
			wantSeconds: 0,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			metrics.PodSchedulingGateRemovalSeconds.Reset()

			wl := utiltestingapi.MakeWorkload("wl", corev1.NamespaceDefault).
				ReserveQuotaAt(utiltestingapi.MakeAdmission(cqName).Obj(), tc.admittedAt).
				AdmittedAt(true, tc.admittedAt).
				Obj()

			RecordPodSchedulingGateRemovalSeconds(testingclock.NewFakeClock(now), gateName, wl, false, nil, nil)

			seconds, err := testutil.GetHistogramMetricValue(
				metrics.PodSchedulingGateRemovalSeconds.WithLabelValues(gateName, string(cqName), "false", roletracker.RoleStandalone),
			)
			if err != nil {
				t.Fatalf("Error getting PodSchedulingGateRemovalSeconds metric value: %v", err)
			}
			if seconds != tc.wantSeconds {
				t.Errorf("Unexpected metric value: want %v, got %v", tc.wantSeconds, seconds)
			}
		})
	}
}

func TestRecordPodSchedulingGateRemovalSecondsCustomLabels(t *testing.T) {
	admittedAt := time.Now().Truncate(time.Second)
	wl := utiltestingapi.MakeWorkload("wl", "default").
		SimpleReserveQuota("cq", "rf", admittedAt).
		AdmittedAt(true, admittedAt).
		Obj()

	cases := map[string]struct {
		entries    []configapi.ControllerMetricsCustomLabel
		stored     map[string]string
		wantLabels map[string]string
	}{
		"none configured": {
			wantLabels: map[string]string{"name": "gate", "cluster_queue": "cq", "is_group": "false"},
		},
		"the admitting cluster queue's value is resolved": {
			entries: []configapi.ControllerMetricsCustomLabel{{Name: "team"}},
			stored:  map[string]string{"team": "red"},
			wantLabels: map[string]string{
				"name": "gate", "cluster_queue": "cq", "is_group": "false", "custom_team": "red",
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.CustomMetricLabels, true)
			cl := metrics.NewCustomLabels(tc.entries)
			t.Cleanup(func() { metrics.InitMetricVectors(nil) })
			if tc.stored != nil {
				cl.CQStore("cq", tc.stored, nil)
			}

			clock := testingclock.NewFakeClock(admittedAt.Add(time.Second))
			RecordPodSchedulingGateRemovalSeconds(clock, "gate", wl, false, cl, nil)

			got := testingmetrics.CollectFilteredGaugeVec(metrics.PodSchedulingGateRemovalSeconds, tc.wantLabels)
			if len(got) != 1 {
				t.Errorf("recorded %d series matching %v, want 1", len(got), tc.wantLabels)
			}
		})
	}
}
