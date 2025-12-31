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

package sparkapplication

import (
	"maps"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	sparkappv1beta2 "github.com/kubeflow/spark-operator/v2/api/v1beta2"
	sparkcommon "github.com/kubeflow/spark-operator/v2/pkg/common"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/podset"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	sparkapplicationtesting "sigs.k8s.io/kueue/pkg/util/testingjobs/sparkapplication"
)

var (
	sparkAppCmpOpts = cmp.Options{
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

func TestPodSets(t *testing.T) {
	toleration := corev1.Toleration{
		Key:      "t1k",
		Operator: corev1.TolerationOpEqual,
		Value:    "t1v",
		Effect:   corev1.TaintEffectNoExecute,
	}
	nodeSelector := map[string]string{"test": "test"}
	testSparkApp := sparkapplicationtesting.MakeSparkApplication("sparkapp", "ns")

	testCases := map[string]struct {
		sparkApp *sparkappv1beta2.SparkApplication
		want     []kueue.PodSet
	}{
		"base": {
			sparkApp: testSparkApp.Clone().
				DriverNodeSelector(maps.Clone(nodeSelector)).
				DriverTolerations([]corev1.Toleration{*toleration.DeepCopy()}).
				ExecutorNodeSelector(maps.Clone(nodeSelector)).
				ExecutorTolerations([]corev1.Toleration{*toleration.DeepCopy()}).
				ExecutorInstances(3).Obj(),
			want: []kueue.PodSet{
				*utiltestingapi.MakePodSet("driver", 1).PodSpec(corev1.PodSpec{
					NodeSelector:   maps.Clone(nodeSelector),
					Tolerations:    []corev1.Toleration{*toleration.DeepCopy()},
					InitContainers: []corev1.Container{},
					Containers: []corev1.Container{
						{
							Name: sparkcommon.SparkDriverContainerName,
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("100m"),
									corev1.ResourceMemory: resource.MustParse("512Mi"),
								},
							},
						},
					},
				}).Obj(),
				*utiltestingapi.MakePodSet("executor", 3).PodSpec(corev1.PodSpec{
					NodeSelector:   maps.Clone(nodeSelector),
					Tolerations:    []corev1.Toleration{*toleration.DeepCopy()},
					InitContainers: []corev1.Container{},
					Containers: []corev1.Container{
						{
							Name: sparkcommon.Spark3DefaultExecutorContainerName,
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("100m"),
									corev1.ResourceMemory: resource.MustParse("512Mi"),
								},
							},
						},
					},
				}).Obj(),
			},
		},
		"with TopologyAwareScheduling": {
			sparkApp: testSparkApp.Clone().Queue("local-queue").
				DriverAnnotation(
					kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block",
				).
				ExecutorAnnotation(
					kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block",
				).Obj(),
			want: []kueue.PodSet{
				*utiltestingapi.MakePodSet("driver", 1).
					RequiredTopologyRequest("cloud.com/block").
					Annotations(map[string]string{
						kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
					}).
					PodSpec(corev1.PodSpec{
						NodeSelector:   map[string]string{},
						Tolerations:    []corev1.Toleration{},
						InitContainers: []corev1.Container{},
						Containers: []corev1.Container{
							{
								Name: sparkcommon.SparkDriverContainerName,
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceCPU:    resource.MustParse("100m"),
										corev1.ResourceMemory: resource.MustParse("512Mi"),
									},
								},
							},
						},
					}).
					Obj(),
				*utiltestingapi.MakePodSet("executor", 1).
					RequiredTopologyRequest("cloud.com/block").
					Annotations(map[string]string{
						kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
					}).
					PodSpec(corev1.PodSpec{
						NodeSelector:   map[string]string{},
						Tolerations:    []corev1.Toleration{},
						InitContainers: []corev1.Container{},
						Containers: []corev1.Container{
							{
								Name: sparkcommon.Spark3DefaultExecutorContainerName,
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceCPU:    resource.MustParse("100m"),
										corev1.ResourceMemory: resource.MustParse("512Mi"),
									},
								},
							},
						},
					}).Obj(),
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, true)

			ctx, _ := utiltesting.ContextWithLog(t)

			kSparkApp := (*SparkApplication)(tc.sparkApp)
			got, err := kSparkApp.PodSets(ctx)

			if err != nil {
				t.Fatalf("PodSets() returned error: %v", err)
			}

			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("PodSets() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestRunWithPodsetsInfo(t *testing.T) {
	toleration := corev1.Toleration{
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
	nodeSelector := map[string]string{"disktype": "ssd"}
	nodeSelector2 := map[string]string{"disktype": "hdd"}
	schedulingGate := corev1.PodSchedulingGate{Name: "test-scheduling-gate-1"}
	schedulingGate2 := corev1.PodSchedulingGate{Name: "test-scheduling-gate-2"}

	testSparkApp := sparkapplicationtesting.MakeSparkApplication("test-sparkapp", "ns")

	cases := map[string]struct {
		sparkApp     *sparkappv1beta2.SparkApplication
		podsetsInfo  []podset.PodSetInfo
		wantSparkApp *sparkappv1beta2.SparkApplication
		wantErr      bool
	}{
		"should add to the SparkApplication specified in the PodSet info": {
			sparkApp: testSparkApp.Clone().Obj(),
			podsetsInfo: []podset.PodSetInfo{
				{
					Name:            "driver",
					NodeSelector:    maps.Clone(nodeSelector),
					Tolerations:     []corev1.Toleration{*toleration.DeepCopy()},
					SchedulingGates: []corev1.PodSchedulingGate{*schedulingGate.DeepCopy()},
				},
				{
					Name:            "executor",
					NodeSelector:    maps.Clone(nodeSelector),
					Tolerations:     []corev1.Toleration{*toleration.DeepCopy()},
					SchedulingGates: []corev1.PodSchedulingGate{*schedulingGate.DeepCopy()},
				},
			},
			wantSparkApp: testSparkApp.Clone().
				Suspend(false).
				DriverNodeSelector(maps.Clone(nodeSelector)).
				DriverTolerations([]corev1.Toleration{*toleration.DeepCopy()}).
				DriverTemplate(&corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						SchedulingGates: []corev1.PodSchedulingGate{*schedulingGate.DeepCopy()},
						Containers: []corev1.Container{
							{Name: sparkcommon.SparkDriverContainerName},
						},
					},
				}).
				ExecutorNodeSelector(maps.Clone(nodeSelector)).
				ExecutorTolerations([]corev1.Toleration{*toleration.DeepCopy()}).
				ExecutorTemplate(&corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						SchedulingGates: []corev1.PodSchedulingGate{*schedulingGate.DeepCopy()},
						Containers: []corev1.Container{
							{Name: sparkcommon.Spark3DefaultExecutorContainerName},
						},
					},
				}).
				Obj(),
			wantErr: false,
		},
		"should raise error when PodSet info config conflicts to the SparkApplication": {
			sparkApp: testSparkApp.Clone().
				DriverNodeSelector(maps.Clone(nodeSelector2)).
				DriverTolerations([]corev1.Toleration{*toleration2.DeepCopy()}).
				DriverTemplate(&corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						SchedulingGates: []corev1.PodSchedulingGate{*schedulingGate2.DeepCopy()},
						Containers: []corev1.Container{
							{Name: sparkcommon.SparkDriverContainerName},
						},
					},
				}).
				ExecutorTolerations([]corev1.Toleration{*toleration2.DeepCopy()}).
				ExecutorNodeSelector(maps.Clone(nodeSelector2)).
				ExecutorTemplate(&corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						SchedulingGates: []corev1.PodSchedulingGate{*schedulingGate2.DeepCopy()},
						Containers: []corev1.Container{
							{Name: sparkcommon.Spark3DefaultExecutorContainerName},
						},
					},
				}).
				Obj(),
			podsetsInfo: []podset.PodSetInfo{
				{
					Name:            "driver",
					NodeSelector:    maps.Clone(nodeSelector),
					Tolerations:     []corev1.Toleration{*toleration.DeepCopy()},
					SchedulingGates: []corev1.PodSchedulingGate{*schedulingGate.DeepCopy()},
				},
				{
					Name:            "executor",
					NodeSelector:    maps.Clone(nodeSelector),
					Tolerations:     []corev1.Toleration{*toleration.DeepCopy()},
					SchedulingGates: []corev1.PodSchedulingGate{*schedulingGate.DeepCopy()},
				},
			},
			wantErr: true,
		},
		"should raise error if the wrong number of PodSet infos is provided": {
			sparkApp: testSparkApp.Clone().Obj(),
			podsetsInfo: []podset.PodSetInfo{
				{
					Name:            "driver",
					NodeSelector:    maps.Clone(nodeSelector),
					Tolerations:     []corev1.Toleration{*toleration.DeepCopy()},
					SchedulingGates: []corev1.PodSchedulingGate{*schedulingGate.DeepCopy()},
				},
			},
			wantErr: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)

			kSparkApp := (*SparkApplication)(tc.sparkApp)
			err := kSparkApp.RunWithPodSetsInfo(ctx, tc.podsetsInfo)
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected RunWithPodSetsInfo() to fail")
					return
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected RunWithPodSetsInfo() error: %v", err)
				return
			}
			if diff := cmp.Diff(tc.wantSparkApp, tc.sparkApp, sparkAppCmpOpts); diff != "" {
				t.Errorf("RunWithPodSetsInfo() mismatch (-want,+got):\n%s", diff)
				return
			}
		})
	}
}

func TestRestorePodSetsInfo(t *testing.T) {
	toleration := corev1.Toleration{
		Key:      "t1k",
		Operator: corev1.TolerationOpEqual,
		Value:    "t1v",
		Effect:   corev1.TaintEffectNoExecute,
	}
	nodeSelector := map[string]string{"disktype": "ssd"}
	schedulingGate := corev1.PodSchedulingGate{Name: "test-scheduling-gate-1"}
	testSparkApp := sparkapplicationtesting.MakeSparkApplication("test-sparkapp", "ns")

	cases := map[string]struct {
		sparkApp     *sparkappv1beta2.SparkApplication
		podsetsInfo  []podset.PodSetInfo
		wantSparkApp *sparkappv1beta2.SparkApplication
		wantChanged  bool
	}{
		"should restore PodSet info to the SparkApplication": {
			sparkApp: testSparkApp.Clone().Obj(),
			podsetsInfo: []podset.PodSetInfo{
				{
					Name:            "driver",
					NodeSelector:    maps.Clone(nodeSelector),
					Tolerations:     []corev1.Toleration{*toleration.DeepCopy()},
					SchedulingGates: []corev1.PodSchedulingGate{*schedulingGate.DeepCopy()},
				},
				{
					Name:            "executor",
					NodeSelector:    maps.Clone(nodeSelector),
					Tolerations:     []corev1.Toleration{*toleration.DeepCopy()},
					SchedulingGates: []corev1.PodSchedulingGate{*schedulingGate.DeepCopy()},
					Count:           3,
				},
			},
			wantSparkApp: testSparkApp.Clone().
				DriverNodeSelector(maps.Clone(nodeSelector)).
				DriverTolerations([]corev1.Toleration{*toleration.DeepCopy()}).
				DriverTemplate(&corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						SchedulingGates: []corev1.PodSchedulingGate{*schedulingGate.DeepCopy()},
						Containers: []corev1.Container{
							{Name: sparkcommon.SparkDriverContainerName},
						},
					},
				}).
				ExecutorNodeSelector(maps.Clone(nodeSelector)).
				ExecutorTolerations([]corev1.Toleration{*toleration.DeepCopy()}).
				ExecutorTemplate(&corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						SchedulingGates: []corev1.PodSchedulingGate{*schedulingGate.DeepCopy()},
						Containers: []corev1.Container{
							{Name: sparkcommon.Spark3DefaultExecutorContainerName},
						},
					},
				}).
				ExecutorInstances(3).
				Obj(),
			wantChanged: true,
		},
		"should not modify the SparkApplication when already restored": {
			sparkApp: testSparkApp.Clone().
				DriverNodeSelector(maps.Clone(nodeSelector)).
				DriverTolerations([]corev1.Toleration{*toleration.DeepCopy()}).
				DriverTemplate(&corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						SchedulingGates: []corev1.PodSchedulingGate{*schedulingGate.DeepCopy()},
						Containers: []corev1.Container{
							{Name: sparkcommon.SparkDriverContainerName},
						},
					},
				}).
				ExecutorTolerations([]corev1.Toleration{*toleration.DeepCopy()}).
				ExecutorNodeSelector(maps.Clone(nodeSelector)).
				ExecutorTemplate(&corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						SchedulingGates: []corev1.PodSchedulingGate{*schedulingGate.DeepCopy()},
						Containers: []corev1.Container{
							{Name: sparkcommon.Spark3DefaultExecutorContainerName},
						},
					},
				}).
				ExecutorInstances(3).
				Obj(),
			podsetsInfo: []podset.PodSetInfo{
				{
					Name:            "driver",
					NodeSelector:    maps.Clone(nodeSelector),
					Tolerations:     []corev1.Toleration{*toleration.DeepCopy()},
					SchedulingGates: []corev1.PodSchedulingGate{*schedulingGate.DeepCopy()},
				},
				{
					Name:            "executor",
					NodeSelector:    maps.Clone(nodeSelector),
					Tolerations:     []corev1.Toleration{*toleration.DeepCopy()},
					SchedulingGates: []corev1.PodSchedulingGate{*schedulingGate.DeepCopy()},
					Count:           3,
				},
			},
			wantSparkApp: testSparkApp.Clone().
				DriverNodeSelector(maps.Clone(nodeSelector)).
				DriverTolerations([]corev1.Toleration{*toleration.DeepCopy()}).
				DriverTemplate(&corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						SchedulingGates: []corev1.PodSchedulingGate{*schedulingGate.DeepCopy()},
						Containers: []corev1.Container{
							{Name: sparkcommon.SparkDriverContainerName},
						},
					},
				}).
				ExecutorNodeSelector(maps.Clone(nodeSelector)).
				ExecutorTolerations([]corev1.Toleration{*toleration.DeepCopy()}).
				ExecutorTemplate(&corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						SchedulingGates: []corev1.PodSchedulingGate{*schedulingGate.DeepCopy()},
						Containers: []corev1.Container{
							{Name: sparkcommon.Spark3DefaultExecutorContainerName},
						},
					},
				}).
				ExecutorInstances(3).
				Obj(),
			wantChanged: false,
		},
		"should not modify the SparkApplication  if the wrong number of PodSet infos is provided": {
			sparkApp: testSparkApp.Clone().Obj(),
			podsetsInfo: []podset.PodSetInfo{
				{
					Name:            "driver",
					NodeSelector:    maps.Clone(nodeSelector),
					Tolerations:     []corev1.Toleration{*toleration.DeepCopy()},
					SchedulingGates: []corev1.PodSchedulingGate{*schedulingGate.DeepCopy()},
				},
			},
			wantSparkApp: testSparkApp.Clone().Obj(),
			wantChanged:  false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			kSparkApp := (*SparkApplication)(tc.sparkApp)
			changed := kSparkApp.RestorePodSetsInfo(tc.podsetsInfo)
			if diff := cmp.Diff(tc.wantChanged, changed); diff != "" {
				t.Errorf("changed mismatch (-want,+got):\n%s", diff)
				return
			}
			if diff := cmp.Diff(tc.wantSparkApp, tc.sparkApp, sparkAppCmpOpts); diff != "" {
				t.Errorf("RunWithPodSetsInfo() mismatch (-want,+got):\n%s", diff)
				return
			}
		})
	}
}

func TestReconciler(t *testing.T) {
	testNamespace := utiltesting.MakeNamespaceWrapper("ns").Label(corev1.LabelMetadataName, "ns").Obj()
	testSparkApp := sparkapplicationtesting.MakeSparkApplication("test-sparkapp", testNamespace.Name)

	cases := map[string]struct {
		reconcilerOptions []jobframework.Option
		sparkApp          *sparkappv1beta2.SparkApplication
		wantWorkloads     []kueue.Workload
	}{
		"workload is created with the corresponding podsets": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(true),
				jobframework.WithManagedJobsNamespaceSelector(labels.Everything()),
			},
			sparkApp: testSparkApp.Clone().Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(testSparkApp.Name, testSparkApp.Namespace).
					PodSets(
						*utiltestingapi.MakePodSet("driver", 1).Obj(),
						*utiltestingapi.MakePodSet("executor", 1).Obj(),
					).
					Obj(),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)

			clientBuilder := utiltesting.NewClientBuilder(sparkappv1beta2.AddToScheme)
			kClient := clientBuilder.WithObjects(tc.sparkApp, testNamespace).Build()
			indexer := utiltesting.AsIndexer(clientBuilder)
			if err := SetupIndexes(ctx, indexer); err != nil {
				t.Fatalf("Could not setup indexes: %v", err)
			}
			recorder := record.NewBroadcaster().NewRecorder(kClient.Scheme(), corev1.EventSource{Component: "test"})
			reconciler, err := NewReconciler(ctx, kClient, indexer, recorder, tc.reconcilerOptions...)
			if err != nil {
				t.Errorf("Error creating the reconciler: %v", err)
			}

			sparkAppKey := client.ObjectKeyFromObject(tc.sparkApp)
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: sparkAppKey,
			})
			if err != nil {
				t.Errorf("Reconcile returned error: %v", err)
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
