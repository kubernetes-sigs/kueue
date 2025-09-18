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

package trainjob

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	kftrainerapi "github.com/kubeflow/trainer/v2/pkg/apis/trainer/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	jobsetapi "sigs.k8s.io/jobset/api/jobset/v1alpha2"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/podset"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingjobset "sigs.k8s.io/kueue/pkg/util/testingjobs/jobset"
	testingtrainjob "sigs.k8s.io/kueue/pkg/util/testingjobs/trainjob"
)

var (
	tjobCmpOpts = cmp.Options{
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion"),
	}
	workloadCmpOpts = cmp.Options{
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreFields(kueue.Workload{}, "TypeMeta"),
		cmpopts.IgnoreFields(metav1.ObjectMeta{}, "Name", "Labels", "ResourceVersion", "OwnerReferences", "Finalizers"),
		cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion", "OwnerReferences", "Finalizers"),
		cmpopts.IgnoreFields(kueue.WorkloadSpec{}, "Priority"),
		cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
		cmpopts.IgnoreFields(kueue.PodSet{}, "Template"),
	}
)

func TestRunWithPodsetsInfo(t *testing.T) {
	testTrainJob := testingtrainjob.MakeTrainJob("trainjob", "ns")
	toleration1 := corev1.Toleration{
		Key:      "t1k",
		Operator: corev1.TolerationOpEqual,
		Value:    "t1v",
		Effect:   corev1.TaintEffectNoExecute,
	}
	toleration2 := corev1.Toleration{
		Key:      "t2k",
		Operator: corev1.TolerationOpExists,
		Effect:   corev1.TaintEffectNoSchedule,
	}

	cases := map[string]struct {
		trainJob            *kftrainerapi.TrainJob
		childJobSet         *jobsetapi.JobSet
		podsetsInfo         []podset.PodSetInfo
		addTrainJobToClient bool
		addJobSetToClient   bool
		wantTrainJob        *kftrainerapi.TrainJob
		wantErr             bool
	}{
		"should add to the TrainJob the config specified in the PodSet info": {
			trainJob: testTrainJob.Clone().Obj(),
			childJobSet: testingjobset.MakeJobSet(testTrainJob.Name, testTrainJob.Namespace).ReplicatedJobs(
				testingjobset.ReplicatedJobRequirements{
					Name: "replicated-job-1",
				},
				testingjobset.ReplicatedJobRequirements{
					Name: "replicated-job-2",
				}).Obj(),
			podsetsInfo: []podset.PodSetInfo{
				{
					Name:            "replicated-job-1",
					NodeSelector:    map[string]string{"disktype": "ssd"},
					Tolerations:     []corev1.Toleration{*toleration1.DeepCopy()},
					SchedulingGates: []corev1.PodSchedulingGate{{Name: "test-scheduling-gate-1"}},
				},
				{
					Name:            "replicated-job-2",
					NodeSelector:    map[string]string{"gpu": "nvidia"},
					Tolerations:     []corev1.Toleration{*toleration2.DeepCopy()},
					SchedulingGates: []corev1.PodSchedulingGate{{Name: "test-scheduling-gate-2"}},
				},
			},
			wantTrainJob: testTrainJob.Clone().
				Annotation(FirstOverrideIdx, "0").
				PodSpecOverrides([]kftrainerapi.PodSpecOverride{
					{
						TargetJobs: []kftrainerapi.PodSpecOverrideTargetJob{
							{Name: "replicated-job-1"},
						},
						NodeSelector:    map[string]string{"disktype": "ssd"},
						Tolerations:     []corev1.Toleration{*toleration1.DeepCopy()},
						SchedulingGates: []corev1.PodSchedulingGate{{Name: "test-scheduling-gate-1"}},
					},
					{
						TargetJobs: []kftrainerapi.PodSpecOverrideTargetJob{
							{Name: "replicated-job-2"},
						},
						NodeSelector:    map[string]string{"gpu": "nvidia"},
						Tolerations:     []corev1.Toleration{*toleration2.DeepCopy()},
						SchedulingGates: []corev1.PodSchedulingGate{{Name: "test-scheduling-gate-2"}},
					},
				}).
				Suspend(false).
				Obj(),
			addTrainJobToClient: true,
			addJobSetToClient:   true,
			wantErr:             false,
		},
		"should respect user provided PodSpecOverrides when adding PodSet info config to the trainjob": {
			trainJob: testTrainJob.Clone().
				PodSpecOverrides([]kftrainerapi.PodSpecOverride{
					{
						TargetJobs: []kftrainerapi.PodSpecOverrideTargetJob{
							{Name: "replicated-job-1"},
							{Name: "replicated-job-2"},
						},
						NodeSelector:    map[string]string{"disktype": "sdd"},
						Tolerations:     []corev1.Toleration{*toleration1.DeepCopy()},
						SchedulingGates: []corev1.PodSchedulingGate{{Name: "test-scheduling-gate-4"}},
					},
				}).Obj(),
			childJobSet: testingjobset.MakeJobSet(testTrainJob.Name, testTrainJob.Namespace).ReplicatedJobs(
				testingjobset.ReplicatedJobRequirements{
					Name: "replicated-job-1",
				},
				testingjobset.ReplicatedJobRequirements{
					Name: "replicated-job-2",
				}).Obj(),
			podsetsInfo: []podset.PodSetInfo{
				{
					Name:            "replicated-job-1",
					NodeSelector:    map[string]string{"disktype": "ssd"},
					Tolerations:     []corev1.Toleration{*toleration1.DeepCopy()},
					SchedulingGates: []corev1.PodSchedulingGate{{Name: "test-scheduling-gate-1"}},
				},
				{
					Name:            "replicated-job-2",
					NodeSelector:    map[string]string{"gpu": "nvidia"},
					Tolerations:     []corev1.Toleration{*toleration2.DeepCopy()},
					SchedulingGates: []corev1.PodSchedulingGate{{Name: "test-scheduling-gate-2"}},
				},
			},
			wantTrainJob: testTrainJob.Clone().
				Annotation(FirstOverrideIdx, "1").
				PodSpecOverrides([]kftrainerapi.PodSpecOverride{
					{
						TargetJobs: []kftrainerapi.PodSpecOverrideTargetJob{
							{Name: "replicated-job-1"},
							{Name: "replicated-job-2"},
						},
						NodeSelector:    map[string]string{"disktype": "sdd"},
						Tolerations:     []corev1.Toleration{*toleration1.DeepCopy()},
						SchedulingGates: []corev1.PodSchedulingGate{{Name: "test-scheduling-gate-4"}},
					},
					{
						TargetJobs: []kftrainerapi.PodSpecOverrideTargetJob{
							{Name: "replicated-job-1"},
						},
						NodeSelector:    map[string]string{"disktype": "ssd"},
						Tolerations:     []corev1.Toleration{*toleration1.DeepCopy()},
						SchedulingGates: []corev1.PodSchedulingGate{{Name: "test-scheduling-gate-1"}},
					},
					{
						TargetJobs: []kftrainerapi.PodSpecOverrideTargetJob{
							{Name: "replicated-job-2"},
						},
						NodeSelector:    map[string]string{"gpu": "nvidia"},
						Tolerations:     []corev1.Toleration{*toleration2.DeepCopy()},
						SchedulingGates: []corev1.PodSchedulingGate{{Name: "test-scheduling-gate-2"}},
					},
				}).
				Suspend(false).
				Obj(),
			addTrainJobToClient: true,
			addJobSetToClient:   true,
			wantErr:             false,
		},
		"should not modify the TrainJob if the wrong number of PodSet infos is provided": {
			trainJob: testTrainJob.Clone().Obj(),
			childJobSet: testingjobset.MakeJobSet(testTrainJob.Name, testTrainJob.Namespace).ReplicatedJobs(
				testingjobset.ReplicatedJobRequirements{
					Name: "replicated-job-1",
				}).Obj(),
			podsetsInfo: []podset.PodSetInfo{
				{
					Name:            "replicated-job-1",
					NodeSelector:    map[string]string{"disktype": "ssd"},
					Tolerations:     []corev1.Toleration{*toleration1.DeepCopy()},
					SchedulingGates: []corev1.PodSchedulingGate{{Name: "test-scheduling-gate-1"}},
				},
				{
					Name:            "replicated-job-2",
					NodeSelector:    map[string]string{"gpu": "nvidia"},
					Tolerations:     []corev1.Toleration{*toleration2.DeepCopy()},
					SchedulingGates: []corev1.PodSchedulingGate{{Name: "test-scheduling-gate-2"}},
				},
			},
			wantTrainJob:        testTrainJob.Clone().Obj(),
			addTrainJobToClient: true,
			addJobSetToClient:   true,
			wantErr:             true,
		},
		"should return an error if the child jobset is not available": {
			trainJob:            testTrainJob.Clone().Obj(),
			addTrainJobToClient: true,
			addJobSetToClient:   false,
			wantErr:             true,
		},
		"should return an error if the trainjob can't be updated": {
			trainJob:            testTrainJob.Clone().Obj(),
			addTrainJobToClient: true,
			addJobSetToClient:   false,
			wantErr:             true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			clientBuilder := utiltesting.NewClientBuilder(kftrainerapi.AddToScheme, jobsetapi.AddToScheme)
			if tc.addTrainJobToClient {
				clientBuilder.WithObjects(tc.trainJob)
			}
			if tc.addJobSetToClient {
				clientBuilder.WithObjects(tc.childJobSet)
			}
			kClient := clientBuilder.Build()
			recorder := record.NewBroadcaster().NewRecorder(kClient.Scheme(), corev1.EventSource{Component: "test"})
			_ = NewReconciler(kClient, recorder, jobframework.WithManageJobsWithoutQueueName(true))

			kTrainJob := (*TrainJob)(tc.trainJob)
			err := kTrainJob.RunWithPodSetsInfo(tc.podsetsInfo)
			if err != nil {
				if !tc.wantErr {
					t.Errorf("unexpected RunWithPodSetsInfo() error: %v", err)
				}
				// Ensure that neither the podSpecOverrides nor the suspend fields were modified
				if diff := cmp.Diff(tc.trainJob, testTrainJob.Obj(), tjobCmpOpts); diff != "" {
					t.Errorf("the original trainJob was modified during a failed RunWithPodSetsInfo() (-want,+got):\n%s", diff)
				}
				return
			}
			if tc.wantErr {
				t.Errorf("expected RunWithPodSetsInfo() to fail")
			}
			if diff := cmp.Diff(tc.wantTrainJob, tc.trainJob, tjobCmpOpts); diff != "" {
				t.Errorf("RunWithPodSetsInfo() mismatch (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestRestorePodSetsInfo(t *testing.T) {
	testTrainJob := testingtrainjob.MakeTrainJob("trainjob", "ns")

	cases := map[string]struct {
		trainJob     *kftrainerapi.TrainJob
		wantTrainJob *kftrainerapi.TrainJob
		wantReturn   bool
	}{
		"should not modify the trainjob if it doesn't have the first override index annotation": {
			trainJob:     testTrainJob.Clone().Obj(),
			wantTrainJob: testTrainJob.Clone().Obj(),
			wantReturn:   true,
		},
		"should not modify the trainjob if it fails parsing the annotation": {
			trainJob: testTrainJob.Clone().
				Annotation(FirstOverrideIdx, "+").
				Obj(),
			wantTrainJob: testTrainJob.Clone().
				Annotation(FirstOverrideIdx, "+").
				Obj(),
			wantReturn: false,
		},
		"should remove all the podSpecOverrides starting from the index specified in the annotation": {
			trainJob: testTrainJob.Clone().
				Annotation(FirstOverrideIdx, "2").
				PodSpecOverrides([]kftrainerapi.PodSpecOverride{
					{
						TargetJobs: []kftrainerapi.PodSpecOverrideTargetJob{
							{Name: "user-provided-1"},
						},
						NodeSelector: map[string]string{"disktype": "sdd"},
					},
					{
						TargetJobs: []kftrainerapi.PodSpecOverrideTargetJob{
							{Name: "user-provided-2"},
						},
						NodeSelector: map[string]string{"disktype": "sdd"},
					},
					{
						TargetJobs: []kftrainerapi.PodSpecOverrideTargetJob{
							{Name: "kueue-provided-1"},
						},
						NodeSelector: map[string]string{"disktype": "sdd"},
					},
					{
						TargetJobs: []kftrainerapi.PodSpecOverrideTargetJob{
							{Name: "kueue-provided-2"},
						},
						NodeSelector: map[string]string{"disktype": "sdd"},
					},
				}).
				Obj(),
			wantTrainJob: testTrainJob.Clone().
				Annotation(FirstOverrideIdx, "2").
				PodSpecOverrides([]kftrainerapi.PodSpecOverride{
					{
						TargetJobs: []kftrainerapi.PodSpecOverrideTargetJob{
							{Name: "user-provided-1"},
						},
						NodeSelector: map[string]string{"disktype": "sdd"},
					},
					{
						TargetJobs: []kftrainerapi.PodSpecOverrideTargetJob{
							{Name: "user-provided-2"},
						},
						NodeSelector: map[string]string{"disktype": "sdd"},
					},
				}).
				Obj(),
			wantReturn: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			kTrainJob := (*TrainJob)(tc.trainJob)
			ret := kTrainJob.RestorePodSetsInfo([]podset.PodSetInfo{})
			if ret != tc.wantReturn {
				t.Errorf("RunWithPodSetsInfo() unexpected return value. got: %v. want :%v", ret, tc.wantReturn)
			}
			if diff := cmp.Diff(tc.wantTrainJob, tc.trainJob, tjobCmpOpts); diff != "" {
				t.Errorf("RunWithPodSetsInfo() mismatch (-want,+got):\n%s", diff)
			}
		})
	}
}
func TestReconciler(t *testing.T) {
	testNamespace := utiltesting.MakeNamespaceWrapper("ns").Label(corev1.LabelMetadataName, "ns").Obj()
	testTrainJob := testingtrainjob.MakeTrainJob("trainjob", testNamespace.Name)

	cases := map[string]struct {
		reconcilerOptions []jobframework.Option
		trainJob          *kftrainerapi.TrainJob
		childJobSet       *jobsetapi.JobSet
		wantTrainJob      *kftrainerapi.TrainJob
		wantWorkloads     []kueue.Workload
	}{
		"workload is created with the corresponding podsets": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(true),
				jobframework.WithManagedJobsNamespaceSelector(labels.Everything()),
			},
			trainJob: testTrainJob.Clone().Obj(),
			childJobSet: testingjobset.MakeJobSet(testTrainJob.Name, testTrainJob.Namespace).ReplicatedJobs(
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
				Obj(),
			wantTrainJob: testTrainJob.Clone().Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload(testTrainJob.Name, testTrainJob.Namespace).
					PodSets(
						*utiltesting.MakePodSet("replicated-job-1", 1).
							PodIndexLabel(ptr.To("batch.kubernetes.io/job-completion-index")).
							SubGroupIndexLabel(ptr.To("jobset.sigs.k8s.io/job-index")).
							SubGroupCount(ptr.To[int32](1)).
							Obj(),
						*utiltesting.MakePodSet("replicated-job-2", 4).
							PodIndexLabel(ptr.To("batch.kubernetes.io/job-completion-index")).
							SubGroupIndexLabel(ptr.To("jobset.sigs.k8s.io/job-index")).
							SubGroupCount(ptr.To[int32](2)).
							Obj(),
					).
					Obj(),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			clientBuilder := utiltesting.NewClientBuilder(kftrainerapi.AddToScheme, jobsetapi.AddToScheme)
			kClient := clientBuilder.WithObjects(tc.trainJob, tc.childJobSet, testNamespace).Build()
			if err := SetupIndexes(ctx, utiltesting.AsIndexer(clientBuilder)); err != nil {
				t.Fatalf("Could not setup indexes: %v", err)
			}
			recorder := record.NewBroadcaster().NewRecorder(kClient.Scheme(), corev1.EventSource{Component: "test"})
			reconciler := NewReconciler(kClient, recorder, tc.reconcilerOptions...)

			tJobKey := client.ObjectKeyFromObject(tc.trainJob)
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: tJobKey,
			})
			if err != nil {
				t.Errorf("Reconcile returned error: %v", err)
			}

			var gotTrainJob kftrainerapi.TrainJob
			if err := kClient.Get(ctx, tJobKey, &gotTrainJob); err != nil {
				t.Fatalf("Could not get Job after reconcile: %v", err)
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
