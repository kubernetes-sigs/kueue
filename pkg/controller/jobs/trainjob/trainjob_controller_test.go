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
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	kftrainerapi "github.com/kubeflow/trainer/v2/pkg/apis/trainer/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	jobsetapi "sigs.k8s.io/jobset/api/jobset/v1alpha2"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/podset"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
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

	// Create and refererence a fake ClusterTrainingRuntime
	testTrainJob := testingtrainjob.MakeTrainJob("trainjob", "ns").RuntimeRef(kftrainerapi.RuntimeRef{
		APIGroup: new(kftrainerapi.GroupVersion.Group),
		Name:     "test",
		Kind:     new(kftrainerapi.ClusterTrainingRuntimeKind),
	})
	testJobset := testingjobset.MakeJobSet("", "").ReplicatedJobs(
		testingjobset.ReplicatedJobRequirements{
			Name: "node",
			Labels: map[string]string{
				"trainer.kubeflow.org/trainjob-ancestor-step": "trainer",
			},
		}).Obj()
	testCtr := testingtrainjob.MakeClusterTrainingRuntime("test", testJobset.Spec)

	// Build a two-replicated-job runtime to exercise name-based matching and
	// duplicate assignment validation.
	multiJobset := testingjobset.MakeJobSet("", "").ReplicatedJobs(
		testingjobset.ReplicatedJobRequirements{Name: "chief"},
		testingjobset.ReplicatedJobRequirements{Name: "worker"},
	).Obj()
	multiJobset.Spec.ReplicatedJobs[0].Template.Spec.Template.Spec.Tolerations = []corev1.Toleration{*toleration1.DeepCopy()}
	multiJobset.Spec.ReplicatedJobs[1].Template.Spec.Template.Spec.Tolerations = []corev1.Toleration{*toleration2.DeepCopy()}
	multiCtr := testingtrainjob.MakeClusterTrainingRuntime("test", multiJobset.Spec)
	chiefAdmissionToleration := corev1.Toleration{
		Key:      "admission.example.com/chief",
		Operator: corev1.TolerationOpEqual,
		Value:    "reserved",
		Effect:   corev1.TaintEffectNoSchedule,
	}
	workerAdmissionToleration := corev1.Toleration{
		Key:      "admission.example.com/worker",
		Operator: corev1.TolerationOpEqual,
		Value:    "spot",
		Effect:   corev1.TaintEffectNoExecute,
	}
	// withTolerations builds the complete replicated-job patch emitted by
	// RunWithPodSetsInfo when only tolerations are configured.
	withTolerations := func(name string, tolerations ...corev1.Toleration) kftrainerapi.ReplicatedJobPatch {
		return kftrainerapi.ReplicatedJobPatch{
			Name: name,
			Template: &kftrainerapi.JobTemplatePatch{
				Spec: &kftrainerapi.JobSpecPatch{
					Template: &kftrainerapi.PodTemplatePatch{
						Metadata: &metav1.ObjectMeta{},
						Spec:     &kftrainerapi.PodSpecPatch{Tolerations: tolerations},
					},
				},
			},
		}
	}
	multiTrainJob := testTrainJob.Clone().RuntimePatches([]kftrainerapi.RuntimePatch{
		testingtrainjob.MakeRuntimePatch(runtimePatchManagerName).EmptyMetadata().Obj(),
	}).Obj()
	multiWantTrainJob := testTrainJob.Clone().RuntimePatches([]kftrainerapi.RuntimePatch{
		testingtrainjob.MakeRuntimePatch(runtimePatchManagerName).
			EmptyMetadata().
			ReplicatedJobs(
				withTolerations("worker", *toleration2.DeepCopy(), workerAdmissionToleration),
				withTolerations("chief", *toleration1.DeepCopy(), chiefAdmissionToleration),
			).
			Obj(),
	}).Suspend(false).Obj()

	staleBaseToleration := corev1.Toleration{
		Key:      "user.example.com/dedicated",
		Operator: corev1.TolerationOpEqual,
		Value:    "training",
		Effect:   corev1.TaintEffectNoSchedule,
	}
	staleJobset := testJobset.DeepCopy()
	staleJobset.Spec.ReplicatedJobs[0].Template.Spec.Template.Spec.Tolerations = []corev1.Toleration{staleBaseToleration}
	staleCtr := testingtrainjob.MakeClusterTrainingRuntime("test", staleJobset.Spec)
	staleKueueToleration := corev1.Toleration{
		Key:      "old.example.com/pool",
		Operator: corev1.TolerationOpEqual,
		Value:    "old",
		Effect:   corev1.TaintEffectNoSchedule,
	}
	staleTrainJob := testTrainJob.Clone().RuntimePatches([]kftrainerapi.RuntimePatch{
		testingtrainjob.MakeRuntimePatch(runtimePatchManagerName).
			EmptyMetadata().
			ReplicatedJobs(
				withTolerations("node", staleKueueToleration),
			).
			Obj(),
	}).Obj()
	staleWantTrainJob := testTrainJob.Clone().RuntimePatches([]kftrainerapi.RuntimePatch{
		testingtrainjob.MakeRuntimePatch(runtimePatchManagerName).
			EmptyMetadata().
			ReplicatedJobs(
				withTolerations("node", staleBaseToleration, *toleration2.DeepCopy()),
			).
			Obj(),
	}).Suspend(false).Obj()
	staleClearedWantTrainJob := testTrainJob.Clone().RuntimePatches([]kftrainerapi.RuntimePatch{
		testingtrainjob.MakeRuntimePatch(runtimePatchManagerName).
			EmptyMetadata().
			ReplicatedJobs(withTolerations("node")).
			Obj(),
	}).Suspend(false).Obj()

	cases := map[string]struct {
		trainJob        *kftrainerapi.TrainJob
		runtime         *kftrainerapi.ClusterTrainingRuntime
		podsetsInfo     []podset.PodSetInfo
		wantTrainJob    *kftrainerapi.TrainJob
		wantErrIs       error
		wantErrContains string
		wantErr         bool
	}{
		"should add to the TrainJob the config specified in the PodSet info": {
			trainJob: testTrainJob.Clone().
				RuntimePatches([]kftrainerapi.RuntimePatch{
					testingtrainjob.MakeRuntimePatch(runtimePatchManagerName).
						EmptyMetadata().
						Obj(),
				}).Obj(),
			podsetsInfo: []podset.PodSetInfo{
				{
					Name: "node",
					Annotations: map[string]string{
						"test-annotation": "test",
					},
					Labels: map[string]string{
						constants.PodSetLabel: "node",
						"test-label":          "label",
					},
					NodeSelector:    map[string]string{"disktype": "ssd"},
					Tolerations:     []corev1.Toleration{*toleration1.DeepCopy()},
					SchedulingGates: []corev1.PodSchedulingGate{{Name: "test-scheduling-gate-1"}},
				},
			},
			wantTrainJob: testTrainJob.Clone().
				RuntimePatches([]kftrainerapi.RuntimePatch{
					testingtrainjob.MakeRuntimePatch(runtimePatchManagerName).
						EmptyMetadata().
						ReplicatedJobs(
							testingtrainjob.MakeReplicatedJobPatch("node").
								PodAnnotation("test-annotation", "test").
								PodLabel(constants.PodSetLabel, "node").
								PodLabel("test-label", "label").
								NodeSelector("disktype", "ssd").
								Toleration(*toleration1.DeepCopy()).
								SchedulingGate("test-scheduling-gate-1").
								Obj(),
						).
						Obj(),
				}).
				Suspend(false).
				Obj(),
			wantErr: false,
		},
		"should respect user provided RuntimePatches when adding PodSet info config to the trainjob": {
			trainJob: testTrainJob.Clone().
				RuntimePatches([]kftrainerapi.RuntimePatch{
					testingtrainjob.MakeRuntimePatch("example.com/user-manager").
						ReplicatedJobs(
							testingtrainjob.MakeReplicatedJobPatch("node").
								NodeSelector("disktype", "sdd").
								Toleration(*toleration1.DeepCopy()).
								SchedulingGate("test-scheduling-gate-4").
								Obj(),
						).
						Obj(),
					testingtrainjob.MakeRuntimePatch(runtimePatchManagerName).
						EmptyMetadata().
						Obj(),
				}).Obj(),
			podsetsInfo: []podset.PodSetInfo{
				{
					Name: "node",
					Annotations: map[string]string{
						"test-annotation": "test",
					},
					Labels: map[string]string{
						constants.PodSetLabel: "node",
						"test-label":          "label",
					},
					NodeSelector:    map[string]string{"gpu": "nvidia"},
					Tolerations:     []corev1.Toleration{*toleration2.DeepCopy()},
					SchedulingGates: []corev1.PodSchedulingGate{{Name: "test-scheduling-gate-2"}},
				},
			},
			wantTrainJob: testTrainJob.Clone().
				RuntimePatches([]kftrainerapi.RuntimePatch{
					testingtrainjob.MakeRuntimePatch("example.com/user-manager").
						ReplicatedJobs(
							testingtrainjob.MakeReplicatedJobPatch("node").
								NodeSelector("disktype", "sdd").
								Toleration(*toleration1.DeepCopy()).
								SchedulingGate("test-scheduling-gate-4").
								Obj(),
						).
						Obj(),
					testingtrainjob.MakeRuntimePatch(runtimePatchManagerName).
						EmptyMetadata().
						ReplicatedJobs(
							testingtrainjob.MakeReplicatedJobPatch("node").
								PodAnnotation("test-annotation", "test").
								PodLabel(constants.PodSetLabel, "node").
								PodLabel("test-label", "label").
								NodeSelector("gpu", "nvidia").
								Toleration(*toleration1.DeepCopy()).
								Toleration(*toleration2.DeepCopy()).
								SchedulingGate("test-scheduling-gate-2").
								Obj(),
						).
						Obj(),
				}).
				Suspend(false).
				Obj(),
			wantErr: false,
		},
		"should match PodSet infos to replicated jobs by name": {
			trainJob: multiTrainJob.DeepCopy(),
			runtime:  multiCtr,
			podsetsInfo: []podset.PodSetInfo{
				{Name: "worker", Tolerations: []corev1.Toleration{workerAdmissionToleration}},
				{Name: "chief", Tolerations: []corev1.Toleration{chiefAdmissionToleration}},
			},
			wantTrainJob: multiWantTrainJob,
			wantErr:      false,
		},
		"should reject duplicate PodSet info names": {
			trainJob:        multiTrainJob.DeepCopy(),
			runtime:         multiCtr,
			podsetsInfo:     []podset.PodSetInfo{{Name: "chief"}, {Name: "chief"}},
			wantTrainJob:    multiTrainJob.DeepCopy(),
			wantErrIs:       podset.ErrInvalidPodsetInfo,
			wantErrContains: "appears more than once",
			wantErr:         true,
		},
		"should preserve runtime tolerations and discard stale Kueue tolerations": {
			trainJob:     staleTrainJob.DeepCopy(),
			runtime:      staleCtr,
			podsetsInfo:  []podset.PodSetInfo{{Name: "node", Tolerations: []corev1.Toleration{*toleration2.DeepCopy()}}},
			wantTrainJob: staleWantTrainJob.DeepCopy(),
			wantErr:      false,
		},
		"should not duplicate runtime tolerations on repeated admission": {
			trainJob:     staleWantTrainJob.DeepCopy(),
			runtime:      staleCtr,
			podsetsInfo:  []podset.PodSetInfo{{Name: "node", Tolerations: []corev1.Toleration{*toleration2.DeepCopy()}}},
			wantTrainJob: staleWantTrainJob.DeepCopy(),
			wantErr:      false,
		},
		"should clear Kueue tolerations when admission has none": {
			trainJob:     staleWantTrainJob.DeepCopy(),
			runtime:      staleCtr,
			podsetsInfo:  []podset.PodSetInfo{{Name: "node"}},
			wantTrainJob: staleClearedWantTrainJob,
			wantErr:      false,
		},
		"should not modify the TrainJob if the wrong number of PodSet infos is provided": {
			trainJob: testTrainJob.DeepCopy(),
			podsetsInfo: []podset.PodSetInfo{
				{
					Name:            "node",
					NodeSelector:    map[string]string{"disktype": "ssd"},
					Tolerations:     []corev1.Toleration{*toleration1.DeepCopy()},
					SchedulingGates: []corev1.PodSchedulingGate{{Name: "test-scheduling-gate-1"}},
				},
				{
					Name:            "non-existent-job",
					NodeSelector:    map[string]string{"gpu": "nvidia"},
					Tolerations:     []corev1.Toleration{*toleration2.DeepCopy()},
					SchedulingGates: []corev1.PodSchedulingGate{{Name: "test-scheduling-gate-2"}},
				},
			},
			wantTrainJob: testTrainJob.DeepCopy(),
			wantErr:      true,
		},
		"should reject a PodSet info with an unknown name": {
			trainJob: testTrainJob.DeepCopy(),
			podsetsInfo: []podset.PodSetInfo{
				{Name: "non-existent-job"},
			},
			wantTrainJob:    testTrainJob.DeepCopy(),
			wantErrIs:       podset.ErrInvalidPodsetInfo,
			wantErrContains: "does not match a replicated job",
			wantErr:         true,
		},
		"should return an error if the trainjob references an unknown training runtime": {
			trainJob: testTrainJob.DeepCopy(),
			wantErr:  true,
		},
		"should replace existing Kueue overrides (idempotency)": {
			trainJob: testTrainJob.Clone().
				RuntimePatches([]kftrainerapi.RuntimePatch{
					testingtrainjob.MakeRuntimePatch("example.com/user-manager").
						ReplicatedJobs(
							testingtrainjob.MakeReplicatedJobPatch("user-provided").
								NodeSelector("disktype", "sdd").
								Obj(),
						).
						Obj(),
					testingtrainjob.MakeRuntimePatch(runtimePatchManagerName).
						EmptyMetadata().
						ReplicatedJobs(
							testingtrainjob.MakeReplicatedJobPatch("node").
								PodAnnotation("test-annotation", "old-value").
								PodLabel(constants.PodSetLabel, "node").
								NodeSelector("old-selector", "value").
								Obj(),
						).
						Obj(),
				}).
				Obj(),
			podsetsInfo: []podset.PodSetInfo{
				{
					Name: "user-provided",
				},
				{
					Name: "node",
					Annotations: map[string]string{
						"test-annotation": "new-value",
					},
					Labels: map[string]string{
						constants.PodSetLabel: "node",
					},
					NodeSelector: map[string]string{"new-selector": "value"},
				},
			},
			wantTrainJob: testTrainJob.Clone().
				RuntimePatches([]kftrainerapi.RuntimePatch{
					testingtrainjob.MakeRuntimePatch("example.com/user-manager").
						ReplicatedJobs(
							testingtrainjob.MakeReplicatedJobPatch("user-provided").
								NodeSelector("disktype", "sdd").
								Obj(),
						).
						Obj(),
					testingtrainjob.MakeRuntimePatch(runtimePatchManagerName).
						EmptyMetadata().
						ReplicatedJobs(
							kftrainerapi.ReplicatedJobPatch{
								Name: "user-provided",
								Template: &kftrainerapi.JobTemplatePatch{
									Spec: &kftrainerapi.JobSpecPatch{
										Template: &kftrainerapi.PodTemplatePatch{
											Metadata: &metav1.ObjectMeta{},
											Spec:     &kftrainerapi.PodSpecPatch{},
										},
									},
								},
							},
							testingtrainjob.MakeReplicatedJobPatch("node").
								PodAnnotation("test-annotation", "new-value").
								PodLabel(constants.PodSetLabel, "node").
								NodeSelector("new-selector", "value").
								Obj(),
						).
						Obj(),
				}).
				Suspend(false).
				Obj(),
			wantErr: false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			clientBuilder := utiltesting.NewClientBuilder(kftrainerapi.AddToScheme, jobsetapi.AddToScheme).WithObjects()
			indexer := utiltesting.AsIndexer(clientBuilder)
			runtime := tc.runtime
			if runtime == nil {
				runtime = testCtr
			}
			kClient := clientBuilder.WithObjects(tc.trainJob, runtime).Build()
			recorder := &utiltesting.EventRecorder{}
			_, err := NewReconciler(ctx, kClient, indexer, recorder, jobframework.WithManageJobsWithoutQueueName(true))
			if err != nil {
				t.Errorf("Error creating the reconciler: %v", err)
			}

			kTrainJob := (*TrainJob)(tc.trainJob)
			originalTrainJob := tc.trainJob.DeepCopy()
			err = kTrainJob.RunWithPodSetsInfo(ctx, kClient, tc.podsetsInfo)
			if err != nil {
				if !tc.wantErr {
					t.Errorf("unexpected RunWithPodSetsInfo() error: %v", err)
				}
				if tc.wantErrIs != nil && !errors.Is(err, tc.wantErrIs) {
					t.Errorf("RunWithPodSetsInfo() error = %v, want error wrapping %v", err, tc.wantErrIs)
				}
				if tc.wantErrContains != "" && !strings.Contains(err.Error(), tc.wantErrContains) {
					t.Errorf("RunWithPodSetsInfo() error = %v, want it to contain %q", err, tc.wantErrContains)
				}
				// Ensure that neither the podSpecOverrides nor the suspend fields were modified
				if diff := cmp.Diff(tc.trainJob, originalTrainJob, tjobCmpOpts); diff != "" {
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
		"should clear replicated job patches from the kueue RuntimePatch": {
			trainJob: testTrainJob.Clone().
				RuntimePatches([]kftrainerapi.RuntimePatch{
					testingtrainjob.MakeRuntimePatch("example.com/user-manager").
						ReplicatedJobs(
							testingtrainjob.MakeReplicatedJobPatch("user-provided-1").Obj(),
							testingtrainjob.MakeReplicatedJobPatch("user-provided-2").Obj(),
						).
						Obj(),
					testingtrainjob.MakeRuntimePatch(runtimePatchManagerName).
						EmptyMetadata().
						ReplicatedJobs(
							testingtrainjob.MakeReplicatedJobPatch("kueue-provided-1").Obj(),
							testingtrainjob.MakeReplicatedJobPatch("kueue-provided-2").Obj(),
						).
						Obj(),
				}).
				Obj(),
			wantTrainJob: testTrainJob.Clone().
				RuntimePatches([]kftrainerapi.RuntimePatch{
					testingtrainjob.MakeRuntimePatch("example.com/user-manager").
						ReplicatedJobs(
							testingtrainjob.MakeReplicatedJobPatch("user-provided-1").Obj(),
							testingtrainjob.MakeReplicatedJobPatch("user-provided-2").Obj(),
						).
						Obj(),
					testingtrainjob.MakeRuntimePatch(runtimePatchManagerName).
						EmptyMetadata().
						Obj(),
				}).
				Obj(),
			wantReturn: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			kTrainJob := (*TrainJob)(tc.trainJob)
			ret := kTrainJob.RestorePodSetsInfo(t.Context(), []podset.PodSetInfo{})
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
	// Create and refererence a fake ClusterTrainingRuntime
	testTrainJob := testingtrainjob.MakeTrainJob("trainjob", "ns").RuntimeRef(kftrainerapi.RuntimeRef{
		APIGroup: new(kftrainerapi.GroupVersion.Group),
		Name:     "test",
		Kind:     new(kftrainerapi.ClusterTrainingRuntimeKind),
	})
	testJobset := testingjobset.MakeJobSet("", "").ReplicatedJobs(
		testingjobset.ReplicatedJobRequirements{
			Name:        "node",
			Replicas:    1,
			Parallelism: 1,
			Completions: 1,
			Labels: map[string]string{
				"trainer.kubeflow.org/trainjob-ancestor-step": "trainer",
			},
		},
		testingjobset.ReplicatedJobRequirements{
			Name:        "foo",
			Replicas:    1,
			Parallelism: 1,
			Completions: 1,
		}).Obj()
	testCtr := testingtrainjob.MakeClusterTrainingRuntime("test", testJobset.Spec)

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
			trainJob:     testTrainJob.DeepCopy(),
			wantTrainJob: testTrainJob.DeepCopy(),
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(testTrainJob.Name, testTrainJob.Namespace).
					PodSets(
						*utiltestingapi.MakePodSet("node", 1).
							PodIndexLabel(new("batch.kubernetes.io/job-completion-index")).
							SubGroupIndexLabel(new(jobsetapi.JobIndexKey)).
							SubGroupCount(new(int32(1))).
							Obj(),
						*utiltestingapi.MakePodSet("foo", 1).
							PodIndexLabel(new("batch.kubernetes.io/job-completion-index")).
							SubGroupIndexLabel(new(jobsetapi.JobIndexKey)).
							SubGroupCount(new(int32(1))).
							Obj(),
					).
					Obj(),
			},
		},
		"podset count for the trainer job is set to .Spec.Trainer.NumNodes": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(true),
				jobframework.WithManagedJobsNamespaceSelector(labels.Everything()),
			},
			trainJob:     testTrainJob.Clone().TrainerNumNodes(2).Obj(),
			wantTrainJob: testTrainJob.Clone().TrainerNumNodes(2).Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(testTrainJob.Name, testTrainJob.Namespace).
					PodSets(
						*utiltestingapi.MakePodSet("node", 2).
							PodIndexLabel(new("batch.kubernetes.io/job-completion-index")).
							SubGroupIndexLabel(new(jobsetapi.JobIndexKey)).
							SubGroupCount(new(int32(1))).
							Obj(),
						*utiltestingapi.MakePodSet("foo", 1).
							PodIndexLabel(new("batch.kubernetes.io/job-completion-index")).
							SubGroupIndexLabel(new(jobsetapi.JobIndexKey)).
							SubGroupCount(new(int32(1))).
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
			kClient := clientBuilder.WithObjects(tc.trainJob, testCtr, testNamespace).Build()
			indexer := utiltesting.AsIndexer(clientBuilder)
			if err := SetupIndexes(ctx, indexer); err != nil {
				t.Fatalf("Could not setup indexes: %v", err)
			}
			recorder := &utiltesting.EventRecorder{}
			reconciler, err := NewReconciler(ctx, kClient, indexer, recorder, tc.reconcilerOptions...)
			if err != nil {
				t.Errorf("Error creating the reconciler: %v", err)
			}

			tJobKey := client.ObjectKeyFromObject(tc.trainJob)
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
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
