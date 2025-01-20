/*
Copyright 2025 The Kubernetes Authors.

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
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	podtesting "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	sparkapptesting "sigs.k8s.io/kueue/pkg/util/testingjobs/sparkapplication"

	kfsparkapi "github.com/kubeflow/spark-operator/api/v1beta2"
	kfsparkcommon "github.com/kubeflow/spark-operator/pkg/common"
)

var (
	baseAppWrapper = sparkapptesting.
			MakeSparkApplication("job", "default").
			Queue("queue")

	expectedDriverPodWrapper = &podtesting.PodWrapper{Pod: corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				"spec-driver-labels": "spec-driver-labels",
			},
			Annotations: map[string]string{
				"spec-driver-annotations": "spec-driver-annotations",
			},
		},
		Spec: corev1.PodSpec{
			PriorityClassName: *baseAppWrapper.Spec.Driver.PriorityClassName,
			Containers: []corev1.Container{{
				Name:  kfsparkcommon.SparkDriverContainerName,
				Image: *baseAppWrapper.Spec.Image,
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse(*baseAppWrapper.Spec.Driver.CoreRequest),
						corev1.ResourceMemory: resource.MustParse(*baseAppWrapper.Spec.Driver.Memory),
					},
					Limits: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse(*baseAppWrapper.Spec.Driver.CoreLimit),
					},
				},
			}},
			Volumes: baseAppWrapper.Spec.Volumes,
		},
	}}
	expectedExecutorPodWrapper = &podtesting.PodWrapper{Pod: corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				"spec-executor-template-labels": "spec-executor-template-labels",
				"spec-executor-labels":          "spec-executor-labels",
			},
			Annotations: map[string]string{
				"spec-executor-annotations": "spec-executor-annotations",
			},
		},
		Spec: corev1.PodSpec{
			Affinity:          baseAppWrapper.Spec.Executor.Affinity,
			NodeSelector:      baseAppWrapper.Spec.Executor.NodeSelector,
			Tolerations:       baseAppWrapper.Spec.Executor.Tolerations,
			InitContainers:    baseAppWrapper.Spec.Executor.InitContainers,
			PriorityClassName: *baseAppWrapper.Spec.Executor.PriorityClassName,
			Containers: append([]corev1.Container{{
				Name:  kfsparkcommon.SparkExecutorContainerName,
				Image: *baseAppWrapper.Spec.Executor.Image,
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse(*baseAppWrapper.Spec.Executor.CoreRequest),
						corev1.ResourceMemory: resource.MustParse(*baseAppWrapper.Spec.Executor.Memory),
					},
					Limits: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse(*baseAppWrapper.Spec.Executor.CoreLimit),
						corev1.ResourceName(baseAppWrapper.Spec.Executor.GPU.Name): resource.MustParse(fmt.Sprintf("%d", baseAppWrapper.Spec.Executor.GPU.Quantity)),
					},
				},
			}}, baseAppWrapper.Spec.Executor.Sidecars...),
		},
	}}
)

func TestPodSets(t *testing.T) {
	testCases := map[string]struct {
		sparkApp    *kfsparkapi.SparkApplication
		wantPodSets []kueue.PodSet
		wantErr     error
	}{
		"normal case, dynamicAllocation=false": {
			sparkApp: baseAppWrapper.Clone().
				DynamicAllocation(false).
				ExecutorInstances(1).
				Obj(),
			wantPodSets: []kueue.PodSet{{
				Name:     kfsparkcommon.SparkRoleDriver,
				Count:    1,
				Template: toPodTemplateSpec(expectedDriverPodWrapper),
			}, {
				Name:     kfsparkcommon.SparkRoleExecutor,
				Count:    1,
				Template: toPodTemplateSpec(expectedExecutorPodWrapper.Clone()),
			}},
		},
		"normal case, dynamicAllocation=true": {
			sparkApp: baseAppWrapper.Clone().
				DynamicAllocation(true).
				DynamicAllocationMaxExecutor(1).
				Obj(),
			wantPodSets: []kueue.PodSet{{
				Name:     kfsparkcommon.SparkRoleDriver,
				Count:    1,
				Template: toPodTemplateSpec(expectedDriverPodWrapper),
			}},
		},

		"with required topology annotation, dynamicAllocation=false": {
			sparkApp: baseAppWrapper.Clone().
				DynamicAllocation(false).
				ExecutorInstances(1).
				DriverAnnotation(kueuealpha.PodSetRequiredTopologyAnnotation, "cloud.com/block").
				ExecutorAnnotation(kueuealpha.PodSetRequiredTopologyAnnotation, "cloud.com/block").
				Obj(),
			wantPodSets: []kueue.PodSet{{
				Name:  kfsparkcommon.SparkRoleDriver,
				Count: 1,
				Template: toPodTemplateSpec(
					expectedDriverPodWrapper.Clone().Annotation(kueuealpha.PodSetRequiredTopologyAnnotation, "cloud.com/block"),
				),
				TopologyRequest: &kueue.PodSetTopologyRequest{
					Required: ptr.To("cloud.com/block"),
				},
			}, {
				Name:  kfsparkcommon.SparkRoleExecutor,
				Count: 1,
				Template: toPodTemplateSpec(
					expectedExecutorPodWrapper.Clone().Annotation(kueuealpha.PodSetRequiredTopologyAnnotation, "cloud.com/block"),
				),
				TopologyRequest: &kueue.PodSetTopologyRequest{
					Required:      ptr.To("cloud.com/block"),
					PodIndexLabel: ptr.To(kfsparkcommon.LabelSparkExecutorID),
				},
			}},
		},
		"with required topology annotation, dynamicAllocation=true": {
			sparkApp: baseAppWrapper.Clone().
				DynamicAllocation(true).
				DynamicAllocationMaxExecutor(1).
				DriverAnnotation(kueuealpha.PodSetRequiredTopologyAnnotation, "cloud.com/block").
				ExecutorAnnotation(kueuealpha.PodSetRequiredTopologyAnnotation, "cloud.com/block").
				Obj(),
			wantPodSets: []kueue.PodSet{{
				Name:  kfsparkcommon.SparkRoleDriver,
				Count: 1,
				Template: toPodTemplateSpec(
					expectedDriverPodWrapper.Clone().Annotation(kueuealpha.PodSetRequiredTopologyAnnotation, "cloud.com/block"),
				),
				TopologyRequest: &kueue.PodSetTopologyRequest{
					Required: ptr.To("cloud.com/block"),
				},
			}},
		},

		"with preferred topology annotation, dynamicAllocation=false": {
			sparkApp: baseAppWrapper.Clone().
				DynamicAllocation(false).
				ExecutorInstances(1).
				DriverAnnotation(kueuealpha.PodSetPreferredTopologyAnnotation, "cloud.com/block").
				ExecutorAnnotation(kueuealpha.PodSetPreferredTopologyAnnotation, "cloud.com/block").
				Obj(),
			wantPodSets: []kueue.PodSet{{
				Name:  kfsparkcommon.SparkRoleDriver,
				Count: 1,
				Template: toPodTemplateSpec(
					expectedDriverPodWrapper.Clone().Annotation(kueuealpha.PodSetPreferredTopologyAnnotation, "cloud.com/block"),
				),
				TopologyRequest: &kueue.PodSetTopologyRequest{
					Preferred: ptr.To("cloud.com/block"),
				},
			}, {
				Name:  kfsparkcommon.SparkRoleExecutor,
				Count: 1,
				Template: toPodTemplateSpec(
					expectedExecutorPodWrapper.Clone().Annotation(kueuealpha.PodSetPreferredTopologyAnnotation, "cloud.com/block"),
				),
				TopologyRequest: &kueue.PodSetTopologyRequest{
					Preferred:     ptr.To("cloud.com/block"),
					PodIndexLabel: ptr.To(kfsparkcommon.LabelSparkExecutorID),
				},
			}},
		},
		"with preferred topology annotation, dynamicAllocation=true": {
			sparkApp: baseAppWrapper.Clone().
				DynamicAllocation(true).
				DynamicAllocationMaxExecutor(1).
				DriverAnnotation(kueuealpha.PodSetPreferredTopologyAnnotation, "cloud.com/block").
				ExecutorAnnotation(kueuealpha.PodSetPreferredTopologyAnnotation, "cloud.com/block").
				Obj(),
			wantPodSets: []kueue.PodSet{{
				Name:  kfsparkcommon.SparkRoleDriver,
				Count: 1,
				Template: toPodTemplateSpec(
					expectedDriverPodWrapper.Clone().Annotation(kueuealpha.PodSetPreferredTopologyAnnotation, "cloud.com/block"),
				),
				TopologyRequest: &kueue.PodSetTopologyRequest{
					Preferred: ptr.To("cloud.com/block"),
				},
			}},
		},

		"invalid - can't parse resource": {
			sparkApp: baseAppWrapper.Clone().
				DynamicAllocation(false).
				ExecutorInstances(1).
				CoreLimit(kfsparkcommon.SparkRoleDriver, ptr.To("malformat")).
				Obj(),
			wantErr: fmt.Errorf(`spec.driver.coreLimit=malformat can't parse: %w`, resource.ErrFormatWrong),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			gotPodSets, gotErr := fromObject(tc.sparkApp).PodSets()
			if gotErr != nil {
				if tc.wantErr == nil {
					t.Errorf("PodSets expected error (-want,+got):\n-nil\n+%s", gotErr.Error())
				}
				if diff := cmp.Diff(tc.wantErr.Error(), gotErr.Error()); diff != "" {
					t.Errorf("PodSets returned error (-want,+got):\n%s", diff)
				}
				return
			}
			if diff := cmp.Diff(tc.wantPodSets, gotPodSets); diff != "" {
				t.Errorf("pod sets mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

var (
	jobCmpOpts = []cmp.Option{
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreFields(kfsparkapi.SparkApplication{}, "TypeMeta", "ObjectMeta"),
	}
	workloadCmpOpts = []cmp.Option{
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreFields(kueue.Workload{}, "TypeMeta"),
		cmpopts.IgnoreFields(metav1.ObjectMeta{}, "Name", "Labels", "ResourceVersion", "OwnerReferences", "Finalizers"),
		cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
		cmpopts.IgnoreFields(kueue.PodSet{}, "Template"),
	}
)

func TestReconciler(t *testing.T) {
	baseWPCWrapper := utiltesting.MakeWorkloadPriorityClass("test-wpc").
		PriorityValue(100)
	driverPCWrapper := utiltesting.MakePriorityClass("driver-priority-class").
		PriorityValue(200)
	executorPCWrapper := utiltesting.MakePriorityClass("executor-priority-class").PriorityValue(100)

	testNamespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "ns",
			Labels: map[string]string{
				"kubernetes.io/metadata.name": "ns",
			},
		},
	}

	testCases := map[string]struct {
		reconcilerOptions []jobframework.Option
		sparkApp          *kfsparkapi.SparkApplication
		priorityClasses   []client.Object
		wantSparkApp      *kfsparkapi.SparkApplication
		wantWorkloads     []kueue.Workload
		wantErr           error
	}{
		// dynamicAllocation=false => integrationModeExecutorAggregated
		// - SparkApplication Workload managed both driver and executor pods
		"dynamicAllocation=false, workload is created with queue and priorityClass": {
			sparkApp: baseAppWrapper.Clone().
				DynamicAllocation(false).
				ExecutorInstances(1).
				Obj(),
			wantSparkApp: baseAppWrapper.Clone().
				DynamicAllocation(false).
				ExecutorInstances(1).
				Suspend(true).
				Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("job", "default").
					Queue("queue").
					PriorityClass(driverPCWrapper.Name).
					Priority(driverPCWrapper.Value).
					PriorityClassSource(constants.PodPriorityClassSource).
					PodSets(
						*utiltesting.MakePodSet(kfsparkcommon.SparkRoleDriver, 1).Obj(),
						*utiltesting.MakePodSet(kfsparkcommon.SparkRoleExecutor, int(1)).Obj(),
					).Obj(),
			},
			priorityClasses: []client.Object{
				driverPCWrapper.Obj(), executorPCWrapper.Obj(),
			},
		},
		"dynamicAllocation=false, workload is created with queue, priorityClass and workloadPriorityClass": {
			sparkApp: baseAppWrapper.Clone().
				DynamicAllocation(false).
				ExecutorInstances(1).
				WorkloadPriorityClass(baseWPCWrapper.Name).
				Obj(),
			wantSparkApp: baseAppWrapper.Clone().
				DynamicAllocation(false).
				ExecutorInstances(1).
				WorkloadPriorityClass(baseWPCWrapper.Name).
				Suspend(true).
				Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("job", "default").
					Queue("queue").
					PriorityClass(baseWPCWrapper.Name).
					Priority(baseWPCWrapper.Value).
					PriorityClassSource(constants.WorkloadPriorityClassSource).
					PodSets(
						*utiltesting.MakePodSet(kfsparkcommon.SparkRoleDriver, 1).Obj(),
						*utiltesting.MakePodSet(kfsparkcommon.SparkRoleExecutor, 1).Obj(),
					).Obj(),
			},
			priorityClasses: []client.Object{
				baseWPCWrapper.Obj(), driverPCWrapper.Obj(), executorPCWrapper.Obj(),
			},
		},
		"dynamicAllocation=false, workload is created with queue, priorityClass and required topology request": {
			sparkApp: baseAppWrapper.Clone().
				DynamicAllocation(false).
				ExecutorInstances(1).
				DriverAnnotation(kueuealpha.PodSetRequiredTopologyAnnotation, "cloud.com/block").
				ExecutorAnnotation(kueuealpha.PodSetRequiredTopologyAnnotation, "cloud.com/block").
				Obj(),
			wantSparkApp: baseAppWrapper.Clone().
				DynamicAllocation(false).
				ExecutorInstances(1).
				DriverAnnotation(
					kueuealpha.PodSetRequiredTopologyAnnotation, "cloud.com/block",
				).
				ExecutorAnnotation(
					kueuealpha.PodSetRequiredTopologyAnnotation, "cloud.com/block",
				).
				Suspend(true).
				Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("job", "default").
					Queue("queue").
					PriorityClass(driverPCWrapper.Name).
					Priority(driverPCWrapper.Value).
					PriorityClassSource(constants.PodPriorityClassSource).
					PodSets(
						*utiltesting.MakePodSet(kfsparkcommon.SparkRoleDriver, 1).
							RequiredTopologyRequest("cloud.com/block").
							Obj(),
						*utiltesting.MakePodSet(kfsparkcommon.SparkRoleExecutor, 1).
							RequiredTopologyRequest("cloud.com/block").
							PodIndexLabel(ptr.To(kfsparkcommon.LabelSparkExecutorID)).
							Obj(),
					).Obj(),
			},
			priorityClasses: []client.Object{
				driverPCWrapper.Obj(), executorPCWrapper.Obj(),
			},
		},
		"dynamicAllocation=false, workload is created with queue, priorityClass and preferred topology request": {
			sparkApp: baseAppWrapper.Clone().
				DynamicAllocation(false).
				ExecutorInstances(1).
				DriverAnnotation(kueuealpha.PodSetPreferredTopologyAnnotation, "cloud.com/block").
				ExecutorAnnotation(kueuealpha.PodSetPreferredTopologyAnnotation, "cloud.com/block").
				Obj(),
			wantSparkApp: baseAppWrapper.Clone().
				DynamicAllocation(false).
				ExecutorInstances(1).
				DriverAnnotation(
					kueuealpha.PodSetPreferredTopologyAnnotation, "cloud.com/block",
				).
				ExecutorAnnotation(
					kueuealpha.PodSetPreferredTopologyAnnotation, "cloud.com/block",
				).
				Suspend(true).
				Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("job", "default").
					Queue("queue").
					PriorityClass(driverPCWrapper.Name).
					Priority(driverPCWrapper.Value).
					PriorityClassSource(constants.PodPriorityClassSource).
					PodSets(
						*utiltesting.MakePodSet(kfsparkcommon.SparkRoleDriver, 1).
							PreferredTopologyRequest("cloud.com/block").
							Obj(),
						*utiltesting.MakePodSet(kfsparkcommon.SparkRoleExecutor, 1).
							PreferredTopologyRequest("cloud.com/block").
							PodIndexLabel(ptr.To(kfsparkcommon.LabelSparkExecutorID)).
							Obj(),
					).Obj(),
			},
			priorityClasses: []client.Object{
				driverPCWrapper.Obj(), executorPCWrapper.Obj(),
			},
		},

		// dynamicAllocation=true => integrationModeExecutorDetached
		// - only driver will be managed in SparkApplication Workloads
		// - executor pods are managed by pod-integration (Workloads per executor pods)
		"dynamicAllocation=true, workload is created with queue and priorityClass": {
			sparkApp: baseAppWrapper.Clone().
				DynamicAllocation(true).
				DynamicAllocationMaxExecutor(1).
				Obj(),
			wantSparkApp: baseAppWrapper.Clone().
				DynamicAllocation(true).
				DynamicAllocationMaxExecutor(1).
				Suspend(true).
				Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("job", "default").
					Queue("queue").
					PriorityClass(driverPCWrapper.Name).
					Priority(driverPCWrapper.Value).
					PriorityClassSource(constants.PodPriorityClassSource).
					PodSets(
						*utiltesting.MakePodSet(kfsparkcommon.SparkRoleDriver, 1).Obj(),
					).Obj(),
			},
			priorityClasses: []client.Object{
				driverPCWrapper.Obj(), executorPCWrapper.Obj(),
			},
		},
		"dynamicAllocation=true, workload is created with queue, priorityClass and workloadPriorityClass": {
			sparkApp: baseAppWrapper.Clone().
				DynamicAllocation(true).
				DynamicAllocationMaxExecutor(1).
				WorkloadPriorityClass(baseWPCWrapper.Name).
				Obj(),
			wantSparkApp: baseAppWrapper.Clone().
				DynamicAllocation(true).
				DynamicAllocationMaxExecutor(1).
				WorkloadPriorityClass(baseWPCWrapper.Name).
				Suspend(true).
				Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("job", "default").
					Queue("queue").
					PriorityClass(baseWPCWrapper.Name).
					Priority(baseWPCWrapper.Value).
					PriorityClassSource(constants.WorkloadPriorityClassSource).
					PodSets(
						*utiltesting.MakePodSet(kfsparkcommon.SparkRoleDriver, 1).Obj(),
					).Obj(),
			},
			priorityClasses: []client.Object{
				baseWPCWrapper.Obj(), driverPCWrapper.Obj(), executorPCWrapper.Obj(),
			},
		},
		"dynamicAllocation=true, workload is created with queue, priorityClass and required topology request": {
			sparkApp: baseAppWrapper.Clone().
				DynamicAllocation(true).
				DynamicAllocationMaxExecutor(1).
				DriverAnnotation(kueuealpha.PodSetRequiredTopologyAnnotation, "cloud.com/block").
				ExecutorAnnotation(kueuealpha.PodSetRequiredTopologyAnnotation, "cloud.com/block").
				Obj(),
			wantSparkApp: baseAppWrapper.Clone().
				DynamicAllocation(true).
				DynamicAllocationMaxExecutor(1).
				DriverAnnotation(
					kueuealpha.PodSetRequiredTopologyAnnotation, "cloud.com/block",
				).
				ExecutorAnnotation(
					kueuealpha.PodSetRequiredTopologyAnnotation, "cloud.com/block",
				).
				Suspend(true).
				Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("job", "default").
					Queue("queue").
					PriorityClass(driverPCWrapper.Name).
					Priority(driverPCWrapper.Value).
					PriorityClassSource(constants.PodPriorityClassSource).
					PodSets(
						*utiltesting.MakePodSet(kfsparkcommon.SparkRoleDriver, 1).
							RequiredTopologyRequest("cloud.com/block").
							Obj(),
					).Obj(),
			},
			priorityClasses: []client.Object{
				driverPCWrapper.Obj(), executorPCWrapper.Obj(),
			},
		},
		"dynamicAllocation=true, workload is created with queue, priorityClass and preferred topology request": {
			sparkApp: baseAppWrapper.Clone().
				DynamicAllocation(true).
				DynamicAllocationMaxExecutor(1).
				DriverAnnotation(
					kueuealpha.PodSetPreferredTopologyAnnotation, "cloud.com/block",
				).
				ExecutorAnnotation(
					kueuealpha.PodSetPreferredTopologyAnnotation, "cloud.com/block",
				).
				Obj(),
			wantSparkApp: baseAppWrapper.Clone().
				DynamicAllocation(true).
				DynamicAllocationMaxExecutor(1).
				DriverAnnotation(
					kueuealpha.PodSetPreferredTopologyAnnotation, "cloud.com/block",
				).
				ExecutorAnnotation(
					kueuealpha.PodSetPreferredTopologyAnnotation, "cloud.com/block",
				).
				Suspend(true).
				Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("job", "default").
					Queue("queue").
					PriorityClass(driverPCWrapper.Name).
					Priority(driverPCWrapper.Value).
					PriorityClassSource(constants.PodPriorityClassSource).
					PodSets(
						*utiltesting.MakePodSet(kfsparkcommon.SparkRoleDriver, 1).
							PreferredTopologyRequest("cloud.com/block").
							Obj(),
					).Obj(),
			},
			priorityClasses: []client.Object{
				driverPCWrapper.Obj(), executorPCWrapper.Obj(),
			},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			clientBuilder := utiltesting.NewClientBuilder(kfsparkapi.AddToScheme)
			if err := SetupIndexes(ctx, utiltesting.AsIndexer(clientBuilder)); err != nil {
				t.Fatalf("Could not setup indexes: %v", err)
			}

			objs := append(tc.priorityClasses, tc.sparkApp, testNamespace)
			kClient := clientBuilder.WithObjects(objs...).Build()
			recorder := record.NewBroadcaster().NewRecorder(kClient.Scheme(), corev1.EventSource{Component: "test"})

			reconciler := NewReconciler(kClient, recorder, tc.reconcilerOptions...)

			jobKey := client.ObjectKeyFromObject(tc.sparkApp)
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: jobKey,
			})
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Reconcile returned error (-want,+got):\n%s", diff)
			}

			var gotSparkApp kfsparkapi.SparkApplication
			if err := kClient.Get(ctx, jobKey, &gotSparkApp); err != nil {
				t.Fatalf("Could not get Job after reconcile: %v", err)
			}
			if diff := cmp.Diff(tc.wantSparkApp, &gotSparkApp, jobCmpOpts...); diff != "" {
				t.Errorf("SparkApplication after reconcile (-want,+got):\n%s", diff)
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

func toPodTemplateSpec(pw *podtesting.PodWrapper) corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{
		ObjectMeta: pw.ObjectMeta,
		Spec:       pw.Spec,
	}
}
