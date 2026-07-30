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

package jobframework_test

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"go.uber.org/mock/gomock"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/component-base/metrics/testutil"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	mocks "sigs.k8s.io/kueue/internal/mocks/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	leaderworkersettesting "sigs.k8s.io/kueue/pkg/util/testingjobs/leaderworkerset"
	statefulsettesting "sigs.k8s.io/kueue/pkg/util/testingjobs/statefulset"
)

func TestSanitizePodSets(t *testing.T) {
	testCases := map[string]struct {
		podSets         []kueue.PodSet
		expectedPodSets []kueue.PodSet
	}{
		"init containers and containers": {
			podSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet("test", 1).
					Containers(*utiltesting.MakeContainer().
						Name("c1").
						WithEnvVar(corev1.EnvVar{Name: "ENV1", Value: "value1"}).
						WithEnvVar(corev1.EnvVar{Name: "ENV1", Value: "value2"}).
						Obj()).
					InitContainers(*utiltesting.MakeContainer().
						Name("init1").
						WithEnvVar(corev1.EnvVar{Name: "ENV2", Value: "value3"}).
						WithEnvVar(corev1.EnvVar{Name: "ENV2", Value: "value4"}).
						Obj()).
					Obj(),
			},
			expectedPodSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet("test", 1).
					Containers(*utiltesting.MakeContainer().
						Name("c1").
						WithEnvVar(corev1.EnvVar{Name: "ENV1", Value: "value2"}).
						Obj()).
					InitContainers(*utiltesting.MakeContainer().
						Name("init1").
						WithEnvVar(corev1.EnvVar{Name: "ENV2", Value: "value4"}).
						Obj()).
					Obj(),
			},
		},
		"containers only": {
			podSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet("test", 1).
					Containers(*utiltesting.MakeContainer().
						Name("c1").
						WithEnvVar(corev1.EnvVar{Name: "ENV1", Value: "value1"}).
						WithEnvVar(corev1.EnvVar{Name: "ENV1", Value: "value2"}).
						Obj()).
					Obj(),
			},
			expectedPodSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet("test", 1).
					Containers(*utiltesting.MakeContainer().
						Name("c1").
						WithEnvVar(corev1.EnvVar{Name: "ENV1", Value: "value2"}).
						Obj()).
					Obj(),
			},
		},
		"empty podsets": {
			podSets:         []kueue.PodSet{},
			expectedPodSets: []kueue.PodSet{},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			jobframework.SanitizePodSets(tc.podSets)

			if diff := cmp.Diff(tc.expectedPodSets, tc.podSets); diff != "" {
				t.Errorf("unexpected difference: %s", diff)
			}
		})
	}
}

func TestRecordWorkloadCreationLatency(t *testing.T) {
	var (
		testJobName        = "test-job"
		testLocalQueueName = kueue.LocalQueueName("test-lq")
	)

	testCases := map[string]struct {
		jobKind         string
		makeJob         func() client.Object
		expectedLatency float64
	}{
		"LeaderWorkerSet": {
			jobKind: "LeaderWorkerSet",
			makeJob: func() client.Object {
				return leaderworkersettesting.MakeLeaderWorkerSet(testJobName, metav1.NamespaceDefault).
					UID(testJobName).
					Queue(string(testLocalQueueName)).
					Obj()
			},
			expectedLatency: 5.0,
		},
		"GenericJob": {
			jobKind: "Job",
			makeJob: func() client.Object {
				return testingjob.MakeJob(testJobName, metav1.NamespaceDefault).
					UID(testJobName).
					Queue(testLocalQueueName).
					Obj()
			},
			expectedLatency: 5.0,
		},
		"StatefulSet": {
			jobKind: "StatefulSet",
			makeJob: func() client.Object {
				return statefulsettesting.MakeStatefulSet(testJobName, metav1.NamespaceDefault).
					UID(testJobName).
					Queue(string(testLocalQueueName)).
					Obj()
			},
			expectedLatency: 5.0,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			metrics.WorkloadCreationLatency.Reset()

			baseTime := time.Now().Truncate(time.Second)
			jobCreationTime := baseTime.Add(-5 * time.Second)

			job := tc.makeJob()
			job.SetCreationTimestamp(metav1.NewTime(jobCreationTime))

			wl := utiltestingapi.MakeWorkload("job-test-job", metav1.NamespaceDefault).Obj()
			wl.CreationTimestamp = metav1.NewTime(baseTime)

			job.SetGeneration(2)
			jobframework.RecordWorkloadCreationLatency(t.Context(), job, tc.jobKind, wl, nil, nil)
			if count, err := testutil.GetHistogramMetricCount(metrics.WorkloadCreationLatency.WithLabelValues(tc.jobKind, roletracker.RoleStandalone)); err != nil || count != 0 {
				t.Errorf("Expecting metric count 0 for generation > 1, got count %d, err %v", count, err)
			}

			job.SetGeneration(1)
			jobframework.RecordWorkloadCreationLatency(t.Context(), job, tc.jobKind, wl, nil, nil)

			val, err := testutil.GetHistogramMetricValue(metrics.WorkloadCreationLatency.WithLabelValues(tc.jobKind, roletracker.RoleStandalone))
			if err != nil {
				t.Fatalf("Failed to get histogram metric value: %v", err)
			}
			if val != tc.expectedLatency {
				t.Errorf("Expecting metric value %f, got %f", tc.expectedLatency, val)
			}
		})
	}
}

func TestJobPodSets_BringYourOwnPodGroup(t *testing.T) {
	workloadGVK := schema.GroupVersionKind{Group: "scheduling.k8s.io", Version: "v1alpha2", Kind: "Workload"}
	jobGVK := batchv1.SchemeGroupVersion.WithKind("Job")

	restMapper := apimeta.NewDefaultRESTMapper([]schema.GroupVersion{workloadGVK.GroupVersion()})
	restMapper.Add(workloadGVK, apimeta.RESTScopeNamespace)

	newWorkload := func(ownerKind, ownerName string, templates ...map[string]any) *unstructured.Unstructured {
		items := make([]any, 0, len(templates))
		for _, tmpl := range templates {
			items = append(items, tmpl)
		}
		obj := &unstructured.Unstructured{Object: map[string]any{
			"metadata": map[string]any{"namespace": metav1.NamespaceDefault, "name": "was-workload"},
			"spec": map[string]any{
				"controllerRef":     map[string]any{"apiGroup": jobGVK.Group, "kind": ownerKind, "name": ownerName},
				"podGroupTemplates": items,
			},
		}}
		obj.SetGroupVersionKind(workloadGVK)
		return obj
	}
	newTemplate := func(name string, minCount int64) map[string]any {
		tmpl := map[string]any{"name": name}
		_ = unstructured.SetNestedField(tmpl, minCount, "schedulingPolicy", "gang", "minCount")
		return tmpl
	}

	job := testingjob.MakeJob("test-job", metav1.NamespaceDefault).Obj()
	basePodSets := []kueue.PodSet{
		*utiltestingapi.MakePodSet("main", 1).Obj(),
	}

	testCases := map[string]struct {
		gateEnabled bool
		workload    *unstructured.Unstructured
		wantCount   int32
	}{
		"gate disabled, matching workload exists": {
			gateEnabled: false,
			workload:    newWorkload("Job", "test-job", newTemplate("main", 5)),
			wantCount:   1,
		},
		"gate enabled, no workload": {
			gateEnabled: true,
			wantCount:   1,
		},
		"gate enabled, workload with non-matching controllerRef": {
			gateEnabled: true,
			workload:    newWorkload("Job", "other-job", newTemplate("main", 5)),
			wantCount:   1,
		},
		"gate enabled, workload with no matching PodGroupTemplate name": {
			gateEnabled: true,
			workload:    newWorkload("Job", "test-job", newTemplate("other-role", 5)),
			wantCount:   1,
		},
		"gate enabled, matching workload overrides count": {
			gateEnabled: true,
			workload:    newWorkload("Job", "test-job", newTemplate("main", 5)),
			wantCount:   5,
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.BringYourOwnPodGroup, tc.gateEnabled)

			builder := utiltesting.NewClientBuilder(batchv1.AddToScheme).WithRESTMapper(restMapper)
			if tc.workload != nil {
				builder = builder.WithObjects(tc.workload)
			}
			cl := builder.Build()

			mockctrl := gomock.NewController(t)
			mgj := mocks.NewMockGenericJob(mockctrl)
			mgj.EXPECT().Object().Return(job).AnyTimes()
			mgj.EXPECT().GVK().Return(jobGVK).AnyTimes()
			mgj.EXPECT().PodSets(gomock.Any(), gomock.Any()).Return(
				[]kueue.PodSet{*basePodSets[0].DeepCopy()}, nil,
			)

			gotPodSets, err := jobframework.JobPodSets(t.Context(), mgj, cl)
			if err != nil {
				t.Fatalf("JobPodSets() error = %v", err)
			}
			if len(gotPodSets) != 1 {
				t.Fatalf("JobPodSets() returned %d pod sets, want 1", len(gotPodSets))
			}
			if gotPodSets[0].Count != tc.wantCount {
				t.Errorf("JobPodSets() count = %d, want %d", gotPodSets[0].Count, tc.wantCount)
			}
		})
	}
}

func TestJobPodSets_BringYourOwnPodGroup_ExcludesPlainPod(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.BringYourOwnPodGroup, true)

	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "test-pod", Namespace: metav1.NamespaceDefault}}
	cl := utiltesting.NewClientBuilder().Build()

	mockctrl := gomock.NewController(t)
	mgj := mocks.NewMockGenericJob(mockctrl)
	mgj.EXPECT().Object().Return(pod).AnyTimes()
	mgj.EXPECT().GVK().Return(corev1.SchemeGroupVersion.WithKind("Pod")).AnyTimes()
	wantPodSets := []kueue.PodSet{*utiltestingapi.MakePodSet("main", 1).Obj()}
	mgj.EXPECT().PodSets(gomock.Any(), gomock.Any()).Return(wantPodSets, nil)

	// No Workload object is registered at all, and the RESTMapper doesn't even
	// know about the WAS API: if the plain-Pod exclusion didn't work, this
	// would surface as an error from the WAS lookup rather than being silently
	// skipped.
	gotPodSets, err := jobframework.JobPodSets(t.Context(), mgj, cl)
	if err != nil {
		t.Fatalf("JobPodSets() error = %v", err)
	}
	if diff := cmp.Diff(wantPodSets, gotPodSets); diff != "" {
		t.Errorf("JobPodSets() mismatch (-want +got):\n%s", diff)
	}
}
