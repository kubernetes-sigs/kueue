/*
Copyright 2023 The Kubernetes Authors.

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

package jobset

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	jobset "sigs.k8s.io/jobset/api/jobset/v1alpha2"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/constants"
	controllerconsts "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingjobset "sigs.k8s.io/kueue/pkg/util/testingjobs/jobset"
)

func TestPodsReady(t *testing.T) {
	testcases := map[string]struct {
		jobSet jobset.JobSet
		want   bool
	}{
		"all jobs are ready": {
			jobSet: *testingjobset.MakeJobSet("jobset", "ns").ReplicatedJobs(
				testingjobset.ReplicatedJobRequirements{
					Name:        "replicated-job-1",
					Replicas:    2,
					Parallelism: 1,
					Completions: 1,
				},
				testingjobset.ReplicatedJobRequirements{
					Name:        "replicated-job-2",
					Replicas:    3,
					Parallelism: 1,
					Completions: 1,
				},
			).JobsStatus(
				jobset.ReplicatedJobStatus{
					Name:      "replicated-job-1",
					Ready:     1,
					Succeeded: 1,
				},
				jobset.ReplicatedJobStatus{
					Name:      "replicated-job-2",
					Ready:     3,
					Succeeded: 0,
				},
			).Obj(),
			want: true,
		},
		"not all jobs are ready": {
			jobSet: *testingjobset.MakeJobSet("jobset", "ns").ReplicatedJobs(
				testingjobset.ReplicatedJobRequirements{
					Name:        "replicated-job-1",
					Replicas:    2,
					Parallelism: 1,
					Completions: 1,
				},
				testingjobset.ReplicatedJobRequirements{
					Name:        "replicated-job-2",
					Replicas:    3,
					Parallelism: 1,
					Completions: 1,
				},
			).JobsStatus(
				jobset.ReplicatedJobStatus{
					Name:      "replicated-job-1",
					Ready:     1,
					Succeeded: 0,
				},
				jobset.ReplicatedJobStatus{
					Name:      "replicated-job-2",
					Ready:     1,
					Succeeded: 2,
				},
			).Obj(),
			want: false,
		},
	}

	for name, tc := range testcases {
		t.Run(name, func(t *testing.T) {
			jobSet := (JobSet)(tc.jobSet)
			got := jobSet.PodsReady()
			if tc.want != got {
				t.Errorf("Unexpected response (want: %v, got: %v)", tc.want, got)
			}
		})
	}
}

func TestReclaimablePods(t *testing.T) {
	baseWrapper := testingjobset.MakeJobSet("jobset", "ns").ReplicatedJobs(
		testingjobset.ReplicatedJobRequirements{
			Name:        "replicated-job-1",
			Replicas:    1,
			Parallelism: 2,
			Completions: 2,
		},
		testingjobset.ReplicatedJobRequirements{
			Name:        "replicated-job-2",
			Replicas:    2,
			Parallelism: 3,
			Completions: 6,
		},
	)

	testcases := map[string]struct {
		jobSet *jobset.JobSet
		want   []kueue.ReclaimablePod
	}{
		"no status": {
			jobSet: baseWrapper.DeepCopy().Obj(),
			want:   nil,
		},
		"empty jobs status": {
			jobSet: baseWrapper.DeepCopy().JobsStatus().Obj(),
			want:   nil,
		},
		"single job done": {
			jobSet: baseWrapper.DeepCopy().JobsStatus(jobset.ReplicatedJobStatus{
				Name:      "replicated-job-1",
				Succeeded: 1,
			}).Obj(),
			want: []kueue.ReclaimablePod{{
				Name:  "replicated-job-1",
				Count: 2,
			}},
		},
		"single job partial done": {
			jobSet: baseWrapper.DeepCopy().JobsStatus(jobset.ReplicatedJobStatus{
				Name:      "replicated-job-2",
				Succeeded: 1,
			}).Obj(),
			want: []kueue.ReclaimablePod{{
				Name:  "replicated-job-2",
				Count: 3,
			}},
		},
		"all done": {
			jobSet: baseWrapper.DeepCopy().JobsStatus(
				jobset.ReplicatedJobStatus{
					Name:      "replicated-job-1",
					Succeeded: 1,
				},
				jobset.ReplicatedJobStatus{
					Name:      "replicated-job-2",
					Succeeded: 2,
				},
			).Obj(),
			want: []kueue.ReclaimablePod{
				{
					Name:  "replicated-job-1",
					Count: 2,
				},
				{
					Name:  "replicated-job-2",
					Count: 6,
				},
			},
		},
	}

	for name, tc := range testcases {
		t.Run(name, func(t *testing.T) {
			jobSet := (*JobSet)(tc.jobSet)
			got, err := jobSet.ReclaimablePods()
			if err != nil {
				t.Fatalf("Unexpected error: %s", err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Unexpected Reclaimable pods (-want +got):\n%s", diff)
			}
		})
	}
}

func TestPodSets(t *testing.T) {
	jobSetTemplate := testingjobset.MakeJobSet("jobset", "ns")

	testCases := map[string]struct {
		jobSet      *JobSet
		wantPodSets func(jobSet *JobSet) []kueue.PodSet
	}{
		"no annotations": {
			jobSet: (*JobSet)(jobSetTemplate.DeepCopy().
				ReplicatedJobs(
					testingjobset.ReplicatedJobRequirements{Name: "job1", Replicas: 2, Parallelism: 1, Completions: 1},
					testingjobset.ReplicatedJobRequirements{Name: "job2", Replicas: 3, Parallelism: 2, Completions: 3},
				).
				Obj()),
			wantPodSets: func(jobSet *JobSet) []kueue.PodSet {
				return []kueue.PodSet{
					{
						Name:     jobSet.Spec.ReplicatedJobs[0].Name,
						Template: *jobSet.Spec.ReplicatedJobs[0].Template.Spec.Template.DeepCopy(),
						Count:    2,
					},
					{
						Name:     jobSet.Spec.ReplicatedJobs[1].Name,
						Template: *jobSet.Spec.ReplicatedJobs[1].Template.Spec.Template.DeepCopy(),
						Count:    6,
					},
				}
			},
		},
		"with required topology annotation": {
			jobSet: (*JobSet)(jobSetTemplate.DeepCopy().
				ReplicatedJobs(
					testingjobset.ReplicatedJobRequirements{
						Name:        "job1",
						Replicas:    2,
						Parallelism: 1,
						Completions: 1,
						PodAnnotations: map[string]string{
							kueuealpha.PodSetRequiredTopologyAnnotation: "cloud.com/block",
						},
					},
					testingjobset.ReplicatedJobRequirements{Name: "job2", Replicas: 3, Parallelism: 2, Completions: 3},
				).
				Obj()),
			wantPodSets: func(jobSet *JobSet) []kueue.PodSet {
				return []kueue.PodSet{
					{
						Name:     jobSet.Spec.ReplicatedJobs[0].Name,
						Template: *jobSet.Spec.ReplicatedJobs[0].Template.Spec.Template.DeepCopy(),
						Count:    2,
						TopologyRequest: &kueue.PodSetTopologyRequest{Required: ptr.To("cloud.com/block"),
							PodIndexLabel:      ptr.To(batchv1.JobCompletionIndexAnnotation),
							SubGroupIndexLabel: ptr.To(jobset.JobIndexKey),
							SubGroupCount:      ptr.To[int32](2),
						},
					},
					{
						Name:     jobSet.Spec.ReplicatedJobs[1].Name,
						Template: *jobSet.Spec.ReplicatedJobs[1].Template.Spec.Template.DeepCopy(),
						Count:    6,
					},
				}
			},
		},
		"with preferred topology annotation": {
			jobSet: (*JobSet)(jobSetTemplate.DeepCopy().
				ReplicatedJobs(
					testingjobset.ReplicatedJobRequirements{Name: "job1", Replicas: 2, Parallelism: 1, Completions: 1},
					testingjobset.ReplicatedJobRequirements{
						Name:        "job2",
						Replicas:    3,
						Parallelism: 2,
						Completions: 3,
						PodAnnotations: map[string]string{
							kueuealpha.PodSetPreferredTopologyAnnotation: "cloud.com/block",
						},
					},
				).
				Obj()),
			wantPodSets: func(jobSet *JobSet) []kueue.PodSet {
				return []kueue.PodSet{
					{
						Name:     jobSet.Spec.ReplicatedJobs[0].Name,
						Template: *jobSet.Spec.ReplicatedJobs[0].Template.Spec.Template.DeepCopy(),
						Count:    2,
					},
					{
						Name:     jobSet.Spec.ReplicatedJobs[1].Name,
						Template: *jobSet.Spec.ReplicatedJobs[1].Template.Spec.Template.DeepCopy(),
						Count:    6,
						TopologyRequest: &kueue.PodSetTopologyRequest{Preferred: ptr.To("cloud.com/block"),
							PodIndexLabel:      ptr.To(batchv1.JobCompletionIndexAnnotation),
							SubGroupIndexLabel: ptr.To(jobset.JobIndexKey),
							SubGroupCount:      ptr.To[int32](3),
						},
					},
				}
			},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			gotPodSets := tc.jobSet.PodSets()
			if diff := cmp.Diff(tc.wantPodSets(tc.jobSet), gotPodSets); diff != "" {
				t.Errorf("pod sets mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

var (
	jobCmpOpts = []cmp.Option{
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreFields(jobset.JobSet{}, "TypeMeta", "ObjectMeta"),
	}
	workloadCmpOpts = []cmp.Option{
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreFields(kueue.Workload{}, "TypeMeta"),
		cmpopts.IgnoreFields(metav1.ObjectMeta{}, "Name", "Labels", "ResourceVersion", "OwnerReferences", "Finalizers"),
		cmpopts.IgnoreFields(kueue.WorkloadSpec{}, "Priority"),
		cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
		cmpopts.IgnoreFields(kueue.PodSet{}, "Template"),
	}
)

func TestReconciler(t *testing.T) {
	baseWPCWrapper := utiltesting.MakeWorkloadPriorityClass("test-wpc").
		PriorityValue(100)
	basePCWrapper := utiltesting.MakePriorityClass("test-pc").
		PriorityValue(200)

	testNamespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "ns",
			Labels: map[string]string{
				"kubernetes.io/metadata.name": "ns",
			},
		},
	}

	cases := map[string]struct {
		reconcilerOptions []jobframework.Option
		job               *jobset.JobSet
		priorityClasses   []client.Object
		wantJob           *jobset.JobSet
		wantWorkloads     []kueue.Workload
		wantErr           error
	}{
		"workload is created with podsets and a ProvReq annotation": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(true),
				jobframework.WithManagedJobsNamespaceSelector(labels.Everything()),
			},
			job: testingjobset.MakeJobSet("jobset", "ns").ReplicatedJobs(
				testingjobset.ReplicatedJobRequirements{
					Name:        "replicated-job-1",
					Replicas:    1,
					Completions: 1,
					Parallelism: 1,
				},
				testingjobset.ReplicatedJobRequirements{
					Name:        "replicated-job-2",
					Replicas:    2,
					Completions: 2,
					Parallelism: 2,
				}).
				Annotations(map[string]string{
					controllerconsts.ProvReqAnnotationPrefix + "test-annotation": "test-val",
					"invalid-provreq-prefix/test-annotation-2":                   "test-val-2",
				}).
				Obj(),
			wantJob: testingjobset.MakeJobSet("jobset", "ns").ReplicatedJobs(
				testingjobset.ReplicatedJobRequirements{
					Name:        "replicated-job-1",
					Replicas:    1,
					Completions: 1,
					Parallelism: 1,
				},
				testingjobset.ReplicatedJobRequirements{
					Name:        "replicated-job-2",
					Replicas:    2,
					Completions: 2,
					Parallelism: 2,
				}).
				Annotations(map[string]string{
					controllerconsts.ProvReqAnnotationPrefix + "test-annotation": "test-val",
					"invalid-provreq-prefix/test-annotation-2":                   "test-val-2",
				}).
				Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("jobset", "ns").
					Annotations(map[string]string{controllerconsts.ProvReqAnnotationPrefix + "test-annotation": "test-val"}).
					PodSets(
						*utiltesting.MakePodSet("replicated-job-1", 1).Obj(),
						*utiltesting.MakePodSet("replicated-job-2", 4).Obj(),
					).
					Obj(),
			},
		},
		"workload is created with podsets and workloadPriorityClass": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(true),
				jobframework.WithManagedJobsNamespaceSelector(labels.Everything()),
			},
			job: testingjobset.MakeJobSet("jobset", "ns").ReplicatedJobs(
				testingjobset.ReplicatedJobRequirements{
					Name:        "replicated-job-1",
					Replicas:    1,
					Completions: 1,
					Parallelism: 1,
				},
			).WorkloadPriorityClass("test-wpc").Obj(),
			priorityClasses: []client.Object{
				baseWPCWrapper.Obj(),
			},
			wantJob: testingjobset.MakeJobSet("jobset", "ns").ReplicatedJobs(
				testingjobset.ReplicatedJobRequirements{
					Name:        "replicated-job-1",
					Replicas:    1,
					Completions: 1,
					Parallelism: 1,
				},
			).WorkloadPriorityClass("test-wpc").Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("jobset", "ns").
					PriorityClass("test-wpc").
					Priority(100).
					PriorityClassSource(constants.WorkloadPriorityClassSource).
					PodSets(
						*utiltesting.MakePodSet("replicated-job-1", 1).Obj(),
					).
					Obj(),
			},
		},
		"workload is created with podsets and PriorityClass": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(true),
				jobframework.WithManagedJobsNamespaceSelector(labels.Everything()),
			},
			job: testingjobset.MakeJobSet("jobset", "ns").ReplicatedJobs(
				testingjobset.ReplicatedJobRequirements{
					Name:        "replicated-job-1",
					Replicas:    1,
					Completions: 1,
					Parallelism: 1,
				},
			).PriorityClass("test-pc").Obj(),
			priorityClasses: []client.Object{
				basePCWrapper.Obj(),
			},
			wantJob: testingjobset.MakeJobSet("jobset", "ns").ReplicatedJobs(
				testingjobset.ReplicatedJobRequirements{
					Name:        "replicated-job-1",
					Replicas:    1,
					Completions: 1,
					Parallelism: 1,
				},
			).PriorityClass("test-pc").Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("jobset", "ns").
					PriorityClass("test-pc").
					Priority(200).
					PriorityClassSource(constants.PodPriorityClassSource).
					PodSets(
						*utiltesting.MakePodSet("replicated-job-1", 1).Obj(),
					).
					Obj(),
			},
		},
		"workload is created with podsets, workloadPriorityClass and PriorityClass": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(true),
				jobframework.WithManagedJobsNamespaceSelector(labels.Everything()),
			},
			job: testingjobset.MakeJobSet("jobset", "ns").ReplicatedJobs(
				testingjobset.ReplicatedJobRequirements{
					Name:        "replicated-job-1",
					Replicas:    1,
					Completions: 1,
					Parallelism: 1,
				},
			).PriorityClass("test-pc").WorkloadPriorityClass("test-wpc").Obj(),
			priorityClasses: []client.Object{
				basePCWrapper.Obj(), baseWPCWrapper.Obj(),
			},
			wantJob: testingjobset.MakeJobSet("jobset", "ns").ReplicatedJobs(
				testingjobset.ReplicatedJobRequirements{
					Name:        "replicated-job-1",
					Replicas:    1,
					Completions: 1,
					Parallelism: 1,
				},
			).PriorityClass("test-pc").WorkloadPriorityClass("test-wpc").Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("jobset", "ns").
					PriorityClass("test-wpc").
					Priority(100).
					PriorityClassSource(constants.WorkloadPriorityClassSource).
					PodSets(
						*utiltesting.MakePodSet("replicated-job-1", 1).Obj(),
					).
					Obj(),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			clientBuilder := utiltesting.NewClientBuilder(jobset.AddToScheme)
			if err := SetupIndexes(ctx, utiltesting.AsIndexer(clientBuilder)); err != nil {
				t.Fatalf("Could not setup indexes: %v", err)
			}
			objs := append(tc.priorityClasses, tc.job, testNamespace)
			kClient := clientBuilder.WithObjects(objs...).Build()
			recorder := record.NewBroadcaster().NewRecorder(kClient.Scheme(), corev1.EventSource{Component: "test"})
			reconciler := NewReconciler(kClient, recorder, tc.reconcilerOptions...)

			jobKey := client.ObjectKeyFromObject(tc.job)
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: jobKey,
			})
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Reconcile returned error (-want,+got):\n%s", diff)
			}

			var gotJobSet jobset.JobSet
			if err := kClient.Get(ctx, jobKey, &gotJobSet); err != nil {
				t.Fatalf("Could not get Job after reconcile: %v", err)
			}
			if diff := cmp.Diff(tc.wantJob, &gotJobSet, jobCmpOpts...); diff != "" {
				t.Errorf("Job after reconcile (-want,+got):\n%s", diff)
			}
			var gotWorkloads kueue.WorkloadList
			if err := kClient.List(ctx, &gotWorkloads); err != nil {
				t.Fatalf("Could not get Workloads after reconcile: %v", err)
			}
			if diff := cmp.Diff(tc.wantWorkloads, gotWorkloads.Items, workloadCmpOpts...); diff != "" {
				t.Errorf("Workloads after reconcile (-want,+got):\n%s", diff)
			}
		})
	}
}
