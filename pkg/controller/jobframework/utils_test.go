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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/component-base/metrics/testutil"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingmetrics "sigs.k8s.io/kueue/pkg/util/testing/metrics"
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
		jobKind                   string
		job                       client.Object
		localQueue                *kueue.LocalQueue
		expectedLatency           float64
		expectedLocalQueueLatency *float64
	}{
		"LeaderWorkerSet": {
			jobKind: "LeaderWorkerSet",
			job: leaderworkersettesting.MakeLeaderWorkerSet(testJobName, metav1.NamespaceDefault).
				UID(testJobName).
				Queue(string(testLocalQueueName)).
				Obj(),
			localQueue:                utiltestingapi.MakeLocalQueue(string(testLocalQueueName), metav1.NamespaceDefault).Obj(),
			expectedLatency:           5.0,
			expectedLocalQueueLatency: new(5.0),
		},
		"GenericJob": {
			jobKind: "Job",
			job: testingjob.MakeJob(testJobName, metav1.NamespaceDefault).
				UID(testJobName).
				Queue(testLocalQueueName).
				Obj(),
			localQueue:                utiltestingapi.MakeLocalQueue(string(testLocalQueueName), metav1.NamespaceDefault).Obj(),
			expectedLatency:           5.0,
			expectedLocalQueueLatency: new(5.0),
		},
		"StatefulSet": {
			jobKind: "StatefulSet",
			job: statefulsettesting.MakeStatefulSet(testJobName, metav1.NamespaceDefault).
				UID(testJobName).
				Queue(string(testLocalQueueName)).
				Obj(),
			localQueue:                utiltestingapi.MakeLocalQueue(string(testLocalQueueName), metav1.NamespaceDefault).Obj(),
			expectedLatency:           5.0,
			expectedLocalQueueLatency: new(5.0),
		},
		"unknown LocalQueue": {
			jobKind: "Job",
			job: testingjob.MakeJob(testJobName, metav1.NamespaceDefault).
				UID(testJobName).
				Queue(testLocalQueueName).
				Obj(),
			expectedLatency: 5.0,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			metrics.WorkloadCreationLatency.Reset()
			metrics.LocalQueueWorkloadCreationLatency.Reset()

			queues := qcache.NewManagerForUnitTests(utiltesting.NewClientBuilder().Build(), nil)
			if tc.localQueue != nil {
				if err := queues.AddLocalQueue(t.Context(), tc.localQueue); err != nil {
					t.Fatalf("Failed to add LocalQueue: %v", err)
				}
			}

			baseTime := time.Now().Truncate(time.Second)
			jobCreationTime := baseTime.Add(-5 * time.Second)

			tc.job.SetCreationTimestamp(metav1.NewTime(jobCreationTime))

			wl := utiltestingapi.MakeWorkload("job-test-job", metav1.NamespaceDefault).Queue(testLocalQueueName).Obj()
			wl.CreationTimestamp = metav1.NewTime(baseTime)

			// Absence is checked by collecting the series; WithLabelValues would create it.
			lqLabels := map[string]string{"name": string(testLocalQueueName), "namespace": metav1.NamespaceDefault}

			tc.job.SetGeneration(2)
			jobframework.RecordWorkloadCreationLatency(t.Context(), tc.job, tc.jobKind, wl, queues, nil, nil)
			if count, err := testutil.GetHistogramMetricCount(metrics.WorkloadCreationLatency.WithLabelValues(tc.jobKind, roletracker.RoleStandalone)); err != nil || count != 0 {
				t.Errorf("Expecting metric count 0 for generation > 1, got count %d, err %v", count, err)
			}
			if got := testingmetrics.CollectFilteredGaugeVec(metrics.LocalQueueWorkloadCreationLatency, lqLabels); len(got) != 0 {
				t.Errorf("Expecting no LocalQueue series for generation > 1, got %v", got)
			}

			tc.job.SetGeneration(1)
			jobframework.RecordWorkloadCreationLatency(t.Context(), tc.job, tc.jobKind, wl, queues, nil, nil)

			val, err := testutil.GetHistogramMetricValue(metrics.WorkloadCreationLatency.WithLabelValues(tc.jobKind, roletracker.RoleStandalone))
			if err != nil {
				t.Fatalf("Failed to get histogram metric value: %v", err)
			}
			if val != tc.expectedLatency {
				t.Errorf("Expecting metric value %f, got %f", tc.expectedLatency, val)
			}
			if tc.expectedLocalQueueLatency == nil {
				if got := testingmetrics.CollectFilteredGaugeVec(metrics.LocalQueueWorkloadCreationLatency, lqLabels); len(got) != 0 {
					t.Errorf("Expecting no LocalQueue series, got %v", got)
				}
			} else {
				lqVal, err := testutil.GetHistogramMetricValue(metrics.LocalQueueWorkloadCreationLatency.WithLabelValues(
					string(testLocalQueueName), metav1.NamespaceDefault, tc.jobKind, roletracker.RoleStandalone))
				if err != nil {
					t.Fatalf("Failed to get LocalQueue histogram metric value: %v", err)
				}
				if lqVal != *tc.expectedLocalQueueLatency {
					t.Errorf("Expecting LocalQueue metric value %f, got %f", *tc.expectedLocalQueueLatency, lqVal)
				}
			}
		})
	}
}
