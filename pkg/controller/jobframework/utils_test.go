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

package jobframework

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"

	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/component-base/metrics/testutil"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
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
			SanitizePodSets(tc.podSets)

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

	uniqueJobKind := "TestJobKind"

	testCases := map[string]struct {
		latency       time.Duration
		wantSampleSum float64
	}{
		"10s latency": {
			latency:       10 * time.Second,
			wantSampleSum: 10.0,
		},
		"2s latency": {
			latency:       2 * time.Second,
			wantSampleSum: 2.0,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			metrics.WorkloadCreationLatency.Reset()

			baseTime := time.Now().Truncate(time.Second)
			jobCreationTime := baseTime.Add(-tc.latency)
			job := testingjob.MakeJob(testJobName, metav1.NamespaceDefault).
				UID(testJobName).
				Queue(testLocalQueueName).
				Obj()
			job.CreationTimestamp = metav1.NewTime(jobCreationTime)

			wl := utiltestingapi.MakeWorkload("job-test-job", metav1.NamespaceDefault).Obj()
			wl.CreationTimestamp = metav1.NewTime(baseTime)

			RecordWorkloadCreationLatency(job, uniqueJobKind, wl, nil, nil)

			val, err := testutil.GetHistogramMetricValue(metrics.WorkloadCreationLatency.WithLabelValues(uniqueJobKind, roletracker.RoleStandalone))
			if err != nil {
				t.Fatalf("Failed to get histogram metric value: %v", err)
			}
			if val != tc.wantSampleSum {
				t.Errorf("Expecting metric value %f, got %f", tc.wantSampleSum, val)
			}
		})
	}
}
