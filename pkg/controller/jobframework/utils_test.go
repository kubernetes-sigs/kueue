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
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/component-base/metrics/testutil"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	leaderworkersettesting "sigs.k8s.io/kueue/pkg/util/testingjobs/leaderworkerset"
	statefulsettesting "sigs.k8s.io/kueue/pkg/util/testingjobs/statefulset"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

// A Workload must not inherit a marker that something downstream reads to
// decide what it may act on, however the configuration came to ask for it. Each
// case keeps an ordinary key alongside, so a test cannot pass by copying
// nothing at all.
func TestNewWorkloadFiltersNonInheritableMetadata(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.CustomMetricLabels, true)

	cases := map[string]struct {
		job             *batchv1.Job
		labelKeys       sets.Set[string]
		annotationKeys  sets.Set[string]
		wantLabels      map[string]string
		wantAnnotations map[string]string
	}{
		// MultiKueue reads this back to decide which Workloads its watches,
		// garbage collection and ownership checks may act on.
		"the MultiKueue origin label": {
			job: testingjob.MakeJob("job", "ns").
				Label("team", "physics").
				Label(kueue.MultiKueueOriginLabel, "multikueue").
				Obj(),
			labelKeys:  sets.New("team", kueue.MultiKueueOriginLabel),
			wantLabels: map[string]string{"team": "physics"},
		},
		// MultiKueue reads the owner pair ahead of the Workload's own owner
		// reference, so a Job naming a job it does not own would be answered.
		"the job-owner annotations": {
			job: testingjob.MakeJob("job", "ns").
				SetAnnotation("team", "physics").
				SetAnnotation(controllerconstants.JobOwnerGVKAnnotation, "batch/v1, Kind=Job").
				SetAnnotation(controllerconstants.JobOwnerNameAnnotation, "someone-elses-job").
				Obj(),
			annotationKeys:  sets.New("team", controllerconstants.JobOwnerGVKAnnotation, controllerconstants.JobOwnerNameAnnotation),
			wantAnnotations: map[string]string{"team": "physics"},
		},
		// The scheduler finishes the slice this names rather than preempting it,
		// and prepareWorkloadSlice writes the real one from the slices it finds.
		"the slice replacement key": {
			job: testingjob.MakeJob("job", "ns").
				SetAnnotation("team", "physics").
				SetAnnotation(workloadslicing.WorkloadSliceReplacementFor, "ns/someone-elses-workload").
				Obj(),
			annotationKeys:  sets.New("team", workloadslicing.WorkloadSliceReplacementFor),
			wantAnnotations: map[string]string{"team": "physics"},
		},
		// The job UID names who a Workload speaks for, and each integration
		// writes the real one as soon as it has built the Workload.
		"the job UID label": {
			job: testingjob.MakeJob("job", "ns").
				Label("team", "physics").
				Label(controllerconstants.JobUIDLabel, "someone-elses-uid").
				Obj(),
			labelKeys:  sets.New("team", controllerconstants.JobUIDLabel),
			wantLabels: map[string]string{"team": "physics"},
		},
		// The variant controller creates variants for whatever carries this,
		// without repeating the eligibility checked before it was written.
		"the concurrent-admission parent label": {
			job: testingjob.MakeJob("job", "ns").
				Label("team", "physics").
				Label(controllerconstants.ConcurrentAdmissionParentLabelKey, "true").
				Obj(),
			labelKeys:  sets.New("team", controllerconstants.ConcurrentAdmissionParentLabelKey),
			wantLabels: map[string]string{"team": "physics"},
		},
		// SliceName answers with this over the Workload's own name, and the
		// topology ungater lists Pods by it.
		"the slice name": {
			job: testingjob.MakeJob("job", "ns").
				SetAnnotation("team", "physics").
				SetAnnotation(kueue.WorkloadSliceNameAnnotation, "someone-elses-workload").
				Obj(),
			annotationKeys:  sets.New("team", kueue.WorkloadSliceNameAnnotation),
			wantAnnotations: map[string]string{"team": "physics"},
		},
		// OwnedBySinglePod reads this to leave a Workload no future Pod can
		// consume out of reassignment.
		"the pod-group marker": {
			job: testingjob.MakeJob("job", "ns").
				SetAnnotation("team", "physics").
				SetAnnotation(podconstants.IsGroupWorkloadAnnotationKey, podconstants.IsGroupWorkloadAnnotationValue).
				Obj(),
			annotationKeys:  sets.New("team", podconstants.IsGroupWorkloadAnnotationKey),
			wantAnnotations: map[string]string{"team": "physics"},
		},
		// The flavor assigner honours these on any Workload carrying them.
		"the allowed flavors": {
			job: testingjob.MakeJob("job", "ns").
				SetAnnotation("team", "physics").
				SetAnnotation(controllerconstants.WorkloadAllowedResourceFlavorAnnotation, "expensive").
				Obj(),
			annotationKeys:  sets.New("team", controllerconstants.WorkloadAllowedResourceFlavorAnnotation),
			wantAnnotations: map[string]string{"team": "physics"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// The reconciler options hold these for every reconcile.
			labelKeys, annotationKeys := tc.labelKeys.Clone(), tc.annotationKeys.Clone()

			wl := jobframework.NewWorkload("wl", tc.job, nil, tc.labelKeys, tc.annotationKeys)

			if tc.wantLabels != nil {
				if diff := cmp.Diff(tc.wantLabels, wl.Labels); diff != "" {
					t.Errorf("workload labels (-want +got):\n%s", diff)
				}
			}
			if tc.wantAnnotations != nil {
				if diff := cmp.Diff(tc.wantAnnotations, wl.Annotations); diff != "" {
					t.Errorf("workload annotations (-want +got):\n%s", diff)
				}
			}
			if diff := cmp.Diff(labelKeys, tc.labelKeys); diff != "" {
				t.Errorf("caller's label keys (-before +after):\n%s", diff)
			}
			if diff := cmp.Diff(annotationKeys, tc.annotationKeys); diff != "" {
				t.Errorf("caller's annotation keys (-before +after):\n%s", diff)
			}
		})
	}
}

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

// The reconciler options hold the caller's set for every reconcile, so filtering
// one Workload's keys must not take a key away from the next one.
func TestCopyableKeysLeaveTheCallersSetAlone(t *testing.T) {
	cases := map[string]struct {
		keys   sets.Set[string]
		filter func(sets.Set[string]) sets.Set[string]
		want   sets.Set[string]
	}{
		"labels": {
			keys:   sets.New("team", controllerconstants.JobUIDLabel),
			filter: jobframework.CopyableLabelKeys,
			want:   sets.New("team"),
		},
		"annotations": {
			keys:   sets.New("team", controllerconstants.JobOwnerNameAnnotation),
			filter: jobframework.CopyableAnnotationKeys,
			want:   sets.New("team"),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			before := tc.keys.Clone()
			if got := tc.filter(tc.keys); !got.Equal(tc.want) {
				t.Errorf("filtered to %v, want %v", sets.List(got), sets.List(tc.want))
			}
			if !tc.keys.Equal(before) {
				t.Errorf("the caller's set became %v, want %v", sets.List(tc.keys), sets.List(before))
			}
		})
	}
}
