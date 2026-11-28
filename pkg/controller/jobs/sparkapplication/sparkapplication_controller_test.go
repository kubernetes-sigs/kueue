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
	"fmt"
	"maps"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	sparkappv1beta2 "github.com/kubeflow/spark-operator/v2/api/v1beta2"
	sparkcommon "github.com/kubeflow/spark-operator/v2/pkg/common"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/component-base/featuregate"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/podset"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	sparkapplicationtesting "sigs.k8s.io/kueue/pkg/util/testingjobs/sparkapplication"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

var (
	sparkAppCmpOpts = cmp.Options{
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion"),
	}
	workloadCmpOpts = cmp.Options{
		cmpopts.EquateEmpty(),
		cmpopts.SortSlices(func(a, b metav1.Condition) bool { return a.Type < b.Type }),
		cmpopts.IgnoreFields(kueue.Workload{}, "TypeMeta"),
		cmpopts.IgnoreFields(metav1.ObjectMeta{}, "Name", "Labels", "ResourceVersion", "OwnerReferences", "Finalizers"),
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
		sparkApp     *sparkappv1beta2.SparkApplication
		featureGates map[featuregate.Feature]bool
		want         []kueue.PodSet
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
									corev1.ResourceMemory: resource.MustParse("896Mi"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceMemory: resource.MustParse("896Mi"),
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
									corev1.ResourceMemory: resource.MustParse("896Mi"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceMemory: resource.MustParse("896Mi"),
								},
							},
						},
					},
				}).Obj(),
			},
		},
		"with SparkApplication-level node selector": {
			sparkApp: testSparkApp.Clone().
				NodeSelector(maps.Clone(nodeSelector)).
				ExecutorInstances(3).Obj(),
			want: []kueue.PodSet{
				*utiltestingapi.MakePodSet("driver", 1).PodSpec(corev1.PodSpec{
					NodeSelector:   maps.Clone(nodeSelector),
					Tolerations:    []corev1.Toleration{},
					InitContainers: []corev1.Container{},
					Containers: []corev1.Container{
						{
							Name: sparkcommon.SparkDriverContainerName,
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("100m"),
									corev1.ResourceMemory: resource.MustParse("896Mi"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceMemory: resource.MustParse("896Mi"),
								},
							},
						},
					},
				}).Obj(),
				*utiltestingapi.MakePodSet("executor", 3).PodSpec(corev1.PodSpec{
					NodeSelector:   maps.Clone(nodeSelector),
					Tolerations:    []corev1.Toleration{},
					InitContainers: []corev1.Container{},
					Containers: []corev1.Container{
						{
							Name: sparkcommon.Spark3DefaultExecutorContainerName,
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("100m"),
									corev1.ResourceMemory: resource.MustParse("896Mi"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceMemory: resource.MustParse("896Mi"),
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
										corev1.ResourceMemory: resource.MustParse("896Mi"),
									},
									Limits: corev1.ResourceList{
										corev1.ResourceMemory: resource.MustParse("896Mi"),
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
										corev1.ResourceMemory: resource.MustParse("896Mi"),
									},
									Limits: corev1.ResourceList{
										corev1.ResourceMemory: resource.MustParse("896Mi"),
									},
								},
							},
						},
					}).Obj(),
			},
		},
		"with TopologyAwareScheduling annotation but TopologyAwareScheduling feature gate disabled": {
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
			sparkApp: testSparkApp.Clone().Queue("local-queue").
				DriverAnnotation(
					kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block",
				).
				ExecutorAnnotation(
					kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block",
				).Obj(),
			want: []kueue.PodSet{
				*utiltestingapi.MakePodSet("driver", 1).
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
										corev1.ResourceMemory: resource.MustParse("896Mi"),
									},
									Limits: corev1.ResourceList{
										corev1.ResourceMemory: resource.MustParse("896Mi"),
									},
								},
							},
						},
					}).
					Obj(),
				*utiltestingapi.MakePodSet("executor", 1).
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
										corev1.ResourceMemory: resource.MustParse("896Mi"),
									},
									Limits: corev1.ResourceList{
										corev1.ResourceMemory: resource.MustParse("896Mi"),
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
			features.SetFeatureGatesDuringTest(t, tc.featureGates)

			ctx, _ := utiltesting.ContextWithLog(t)

			kSparkApp := (*SparkApplication)(tc.sparkApp)
			got, err := kSparkApp.PodSets(ctx, nil)

			if err != nil {
				t.Fatalf("PodSets() returned error: %v", err)
			}

			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("PodSets() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestPodSetsExecutorCount(t *testing.T) {
	elasticSparkApp := sparkapplicationtesting.MakeSparkApplication("sparkapp", "ns").
		Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
		ExecutorInstances(2).
		SubmissionID("sub-1")
	dynamicSparkApp := elasticSparkApp.Clone().
		DynamicAllocation(&sparkappv1beta2.DynamicAllocation{Enabled: true, MaxExecutors: new(int32(4))})
	executorPod := testingpod.MakePod("", "ns").
		Label(sparkcommon.LabelSparkAppName, "sparkapp").
		Label(sparkcommon.LabelSparkRole, sparkcommon.SparkRoleExecutor).
		Label(sparkcommon.LabelSubmissionID, "sub-1")
	// rampingUpSparkApp is running and starts with 4 executors, which the driver
	// creates in batches.
	rampingUpSparkApp := elasticSparkApp.Clone().
		DynamicAllocation(&sparkappv1beta2.DynamicAllocation{Enabled: true, InitialExecutors: new(int32(4))}).
		AppState(sparkappv1beta2.ApplicationStateRunning)
	// executorPods returns executor pods of the current submission with the
	// given IDs.
	executorPods := func(phase corev1.PodPhase, ids ...int) []corev1.Pod {
		pods := make([]corev1.Pod, 0, len(ids))
		for _, id := range ids {
			pods = append(pods, *executorPod.Clone().
				Name(fmt.Sprintf("exec-%d", id)).
				Label(sparkcommon.LabelSparkExecutorID, strconv.Itoa(id)).
				StatusPhase(phase).
				Obj())
		}
		return pods
	}
	// withoutInstances unsets spec.executor.instances.
	withoutInstances := func(app *sparkappv1beta2.SparkApplication) *sparkappv1beta2.SparkApplication {
		app.Spec.Executor.Instances = nil
		return app
	}
	// executorIDs returns the IDs from first to last.
	executorIDs := func(first, last int) []int {
		ids := make([]int, 0, last-first+1)
		for id := first; id <= last; id++ {
			ids = append(ids, id)
		}
		return ids
	}
	// partialSparkApp is running with the partial scale-up strategy, and
	// partialExecutorPods are its 3 created executors.
	partialSparkApp := elasticSparkApp.Clone().
		Annotation(constants.ElasticJobScaleUpStrategyAnnotationKey, constants.ElasticJobScaleUpStrategyPartial).
		ExecutorInstances(1).
		DynamicAllocation(&sparkappv1beta2.DynamicAllocation{Enabled: true, MinExecutors: new(int32(1)), MaxExecutors: new(int32(4))}).
		AppState(sparkappv1beta2.ApplicationStateRunning)
	partialExecutorPods := append(executorPods(corev1.PodRunning, 1, 2), executorPods(corev1.PodPending, 3)...)

	testCases := map[string]struct {
		sparkApp     *sparkappv1beta2.SparkApplication
		pods         []corev1.Pod
		featureGates map[featuregate.Feature]bool
		wantCount    int32
		wantMinCount *int32
	}{
		"not elastic: initial executors": {
			sparkApp: sparkapplicationtesting.MakeSparkApplication("sparkapp", "ns").
				ExecutorInstances(2).
				AppState(sparkappv1beta2.ApplicationStateRunning).
				Obj(),
			pods:      executorPods(corev1.PodRunning, 1),
			wantCount: 2,
		},
		"not elastic: spark.executor.instances in sparkConf": {
			sparkApp: withoutInstances(sparkapplicationtesting.MakeSparkApplication("sparkapp", "ns").
				SparkConf("spark.executor.instances", "5").
				Obj()),
			wantCount: 5,
		},
		"not elastic: spec.executor.instances wins over sparkConf": {
			sparkApp: sparkapplicationtesting.MakeSparkApplication("sparkapp", "ns").
				ExecutorInstances(2).
				SparkConf("spark.executor.instances", "5").
				Obj(),
			wantCount: 2,
		},
		"elastic, dynamic allocation in sparkConf: initialExecutors": {
			sparkApp: elasticSparkApp.Clone().
				SparkConf("spark.dynamicAllocation.enabled", "true").
				SparkConf("spark.dynamicAllocation.initialExecutors", "4").
				Obj(),
			wantCount: 4,
		},
		"elastic, dynamic allocation in sparkConf, running above its maxExecutors: capped": {
			sparkApp: elasticSparkApp.Clone().
				SparkConf("spark.dynamicAllocation.enabled", "true").
				SparkConf("spark.dynamicAllocation.maxExecutors", "3").
				AppState(sparkappv1beta2.ApplicationStateRunning).
				Obj(),
			pods:      executorPods(corev1.PodRunning, 1, 2, 3, 4, 5),
			wantCount: 3,
		},
		"elastic, spec.dynamicAllocation enabled with maxExecutors in sparkConf: capped": {
			sparkApp: elasticSparkApp.Clone().
				DynamicAllocation(&sparkappv1beta2.DynamicAllocation{Enabled: true}).
				SparkConf("spark.dynamicAllocation.maxExecutors", "3").
				AppState(sparkappv1beta2.ApplicationStateRunning).
				Obj(),
			pods:      executorPods(corev1.PodRunning, 1, 2, 3, 4, 5),
			wantCount: 3,
		},
		"elastic, dynamic allocation in sparkConf: the largest of instances, minExecutors and initialExecutors": {
			sparkApp: withoutInstances(elasticSparkApp.Clone().
				SparkConf("spark.executor.instances", "1").
				SparkConf("spark.dynamicAllocation.enabled", "true").
				SparkConf("spark.dynamicAllocation.minExecutors", "2").
				SparkConf("spark.dynamicAllocation.initialExecutors", "3").
				Obj()),
			wantCount: 3,
		},
		"elastic, spec.dynamicAllocation maxExecutors wins over sparkConf": {
			sparkApp: elasticSparkApp.Clone().
				DynamicAllocation(&sparkappv1beta2.DynamicAllocation{Enabled: true, MaxExecutors: new(int32(8))}).
				SparkConf("spark.dynamicAllocation.maxExecutors", "6").
				AppState(sparkappv1beta2.ApplicationStateRunning).
				Obj(),
			pods:      executorPods(corev1.PodRunning, executorIDs(1, 9)...),
			wantCount: 8,
		},
		"elastic, spec.dynamicAllocation without minExecutors falls back to sparkConf": {
			sparkApp: elasticSparkApp.Clone().
				DynamicAllocation(&sparkappv1beta2.DynamicAllocation{Enabled: true}).
				SparkConf("spark.dynamicAllocation.minExecutors", "2").
				AppState(sparkappv1beta2.ApplicationStateRunning).
				Obj(),
			pods:      append(executorPods(corev1.PodSucceeded, 1, 2), executorPods(corev1.PodRunning, 3)...),
			wantCount: 2,
		},
		// spark-operator passes no spec.dynamicAllocation field when it is disabled, so
		// Spark runs with what spec.sparkConf says.
		"elastic, spec.dynamicAllocation disabled does not override sparkConf": {
			sparkApp: elasticSparkApp.Clone().
				DynamicAllocation(&sparkappv1beta2.DynamicAllocation{MaxExecutors: new(int32(8))}).
				SparkConf("spark.dynamicAllocation.enabled", "true").
				SparkConf("spark.dynamicAllocation.maxExecutors", "6").
				AppState(sparkappv1beta2.ApplicationStateRunning).
				Obj(),
			pods:      executorPods(corev1.PodRunning, executorIDs(1, 9)...),
			wantCount: 6,
		},
		"elastic, spark.dynamicAllocation.enabled is case-insensitive in its value, as in Spark": {
			sparkApp: elasticSparkApp.Clone().
				SparkConf("spark.dynamicAllocation.enabled", "TRUE").
				SparkConf("spark.dynamicAllocation.initialExecutors", "4").
				Obj(),
			wantCount: 4,
		},
		// Spark property names are case-sensitive: a lowercase key has no effect in Spark.
		"elastic, a sparkConf key in the wrong case is ignored": {
			sparkApp: elasticSparkApp.Clone().
				SparkConf("spark.dynamicallocation.enabled", "true").
				SparkConf("spark.dynamicAllocation.initialExecutors", "4").
				Obj(),
			wantCount: 2,
		},
		"elastic, a malformed or negative count in sparkConf is unset": {
			sparkApp: elasticSparkApp.Clone().
				SparkConf("spark.dynamicAllocation.enabled", "true").
				SparkConf("spark.dynamicAllocation.initialExecutors", "two").
				SparkConf("spark.dynamicAllocation.maxExecutors", "-1").
				AppState(sparkappv1beta2.ApplicationStateRunning).
				Obj(),
			pods:      executorPods(corev1.PodRunning, 1, 2, 3, 4, 5),
			wantCount: 5,
		},
		"elastic without dynamic allocation: initial executors": {
			sparkApp:  elasticSparkApp.Clone().AppState(sparkappv1beta2.ApplicationStateRunning).Obj(),
			pods:      executorPods(corev1.PodRunning, 1),
			wantCount: 2,
		},
		"elastic, driver not started: initial executors": {
			sparkApp:  dynamicSparkApp.Clone().AppState(sparkappv1beta2.ApplicationStateSubmitted).Obj(),
			pods:      executorPods(corev1.PodRunning, 1),
			wantCount: 2,
		},
		"elastic, suspended with stale executors: initial executors": {
			sparkApp:  dynamicSparkApp.Clone().AppState(sparkappv1beta2.ApplicationStateSuspended).Obj(),
			pods:      executorPods(corev1.PodRunning, 1),
			wantCount: 2,
		},
		"elastic, running without executors: initial executors": {
			sparkApp:  dynamicSparkApp.Clone().AppState(sparkappv1beta2.ApplicationStateRunning).Obj(),
			wantCount: 2,
		},
		"elastic, running without the SparkApplication elastic feature: counts pods": {
			sparkApp:     dynamicSparkApp.Clone().AppState(sparkappv1beta2.ApplicationStateRunning).Obj(),
			pods:         executorPods(corev1.PodRunning, 1, 2, 3),
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlicesForSparkApplication: false},
			wantCount:    3,
		},
		"elastic, running: pending and running executors": {
			sparkApp: dynamicSparkApp.Clone().AppState(sparkappv1beta2.ApplicationStateRunning).Obj(),
			pods: slices.Concat(
				executorPods(corev1.PodRunning, 1, 2),
				executorPods(corev1.PodPending, 3),
				executorPods(corev1.PodSucceeded, 4),
				executorPods(corev1.PodFailed, 5),
			),
			wantCount: 3,
		},
		"elastic, running before initial executors created with a failed executor: initial executors": {
			sparkApp: dynamicSparkApp.Clone().
				DynamicAllocation(&sparkappv1beta2.DynamicAllocation{Enabled: true, InitialExecutors: new(int32(4)), MaxExecutors: new(int32(10))}).
				AppState(sparkappv1beta2.ApplicationStateRunning).
				Obj(),
			pods: slices.Concat(
				executorPods(corev1.PodRunning, 1, 2),
				executorPods(corev1.PodFailed, 3),
			),
			wantCount: 4,
		},
		"elastic, running after a failed executor is deleted: initial executors": {
			sparkApp: dynamicSparkApp.Clone().
				DynamicAllocation(&sparkappv1beta2.DynamicAllocation{Enabled: true, InitialExecutors: new(int32(4)), MaxExecutors: new(int32(10))}).
				AppState(sparkappv1beta2.ApplicationStateRunning).
				Annotation(maxExecutorIDAnnotation, "3").
				Annotation(maxExecutorIDSubmissionAnnotation, "sub-1").
				Annotation(failedExecutorIDsAnnotation, "3").
				Obj(),
			pods:      executorPods(corev1.PodRunning, 1, 2),
			wantCount: 4,
		},
		"elastic, running with replacement executor after failure: initial executors": {
			sparkApp: dynamicSparkApp.Clone().
				DynamicAllocation(&sparkappv1beta2.DynamicAllocation{Enabled: true, InitialExecutors: new(int32(4)), MaxExecutors: new(int32(10))}).
				AppState(sparkappv1beta2.ApplicationStateRunning).
				Annotation(maxExecutorIDAnnotation, "3").
				Annotation(maxExecutorIDSubmissionAnnotation, "sub-1").
				Annotation(failedExecutorIDsAnnotation, "3").
				Obj(),
			pods:      executorPods(corev1.PodRunning, 1, 2, 4),
			wantCount: 4,
		},
		"elastic, failed executor IDs recorded for a previous submission are ignored": {
			sparkApp: dynamicSparkApp.Clone().
				DynamicAllocation(&sparkappv1beta2.DynamicAllocation{Enabled: true, InitialExecutors: new(int32(4)), MaxExecutors: new(int32(10))}).
				AppState(sparkappv1beta2.ApplicationStateRunning).
				Annotation(maxExecutorIDAnnotation, "3").
				Annotation(maxExecutorIDSubmissionAnnotation, "sub-0").
				Annotation(failedExecutorIDsAnnotation, "3").
				Obj(),
			pods:      executorPods(corev1.PodRunning, 1, 2, 4),
			wantCount: 3,
		},
		"elastic, executors started, then an executor fails and is not replaced: counts pods": {
			sparkApp: dynamicSparkApp.Clone().
				DynamicAllocation(&sparkappv1beta2.DynamicAllocation{Enabled: true, InitialExecutors: new(int32(4)), MaxExecutors: new(int32(10))}).
				AppState(sparkappv1beta2.ApplicationStateRunning).
				Annotation(executorsStartedSubmissionAnnotation, "sub-1").
				Obj(),
			pods: slices.Concat(
				executorPods(corev1.PodRunning, 1, 2, 3),
				executorPods(corev1.PodFailed, 4),
			),
			wantCount: 3,
		},
		"elastic, unknown: counts pods": {
			sparkApp:  dynamicSparkApp.Clone().AppState(sparkappv1beta2.ApplicationStateUnknown).Obj(),
			pods:      executorPods(corev1.PodRunning, 1, 2, 3),
			wantCount: 3,
		},
		"elastic, unknown without executors: initial executors": {
			sparkApp:  dynamicSparkApp.Clone().AppState(sparkappv1beta2.ApplicationStateUnknown).Obj(),
			wantCount: 2,
		},
		"elastic, running before the initial executors are created: initial executors": {
			sparkApp:  dynamicSparkApp.Clone().AppState(sparkappv1beta2.ApplicationStateRunning).Obj(),
			pods:      executorPods(corev1.PodRunning, 1),
			wantCount: 2,
		},
		"elastic, running with maxExecutors below the initial executors: counts pods": {
			sparkApp: elasticSparkApp.Clone().
				DynamicAllocation(&sparkappv1beta2.DynamicAllocation{Enabled: true, MaxExecutors: new(int32(1))}).
				AppState(sparkappv1beta2.ApplicationStateRunning).
				Obj(),
			pods:      executorPods(corev1.PodRunning, 1),
			wantCount: 1,
		},
		"elastic, running with executors untracked by the operator: counts pods": {
			sparkApp: dynamicSparkApp.Clone().AppState(sparkappv1beta2.ApplicationStateRunning).Obj(),
			pods: slices.Concat(
				executorPods(corev1.PodSucceeded, 1),
				executorPods(corev1.PodRunning, 1001, 1002),
				executorPods(corev1.PodPending, 1003),
			),
			wantCount: 3,
		},
		"elastic, more initial executors than the operator tracks: scale-up counts pods": {
			sparkApp: elasticSparkApp.Clone().
				DynamicAllocation(&sparkappv1beta2.DynamicAllocation{Enabled: true, InitialExecutors: new(int32(1002)), MaxExecutors: new(int32(2000))}).
				AppState(sparkappv1beta2.ApplicationStateRunning).
				Obj(),
			pods:      executorPods(corev1.PodRunning, executorIDs(1, 1003)...),
			wantCount: 1003,
		},
		"elastic, running: executors of a previous submission are not counted": {
			sparkApp: dynamicSparkApp.Clone().AppState(sparkappv1beta2.ApplicationStateRunning).Obj(),
			pods: slices.Concat(
				[]corev1.Pod{
					*executorPod.Clone().Name("old-exec-1").Label(sparkcommon.LabelSubmissionID, "sub-0").
						Label(sparkcommon.LabelSparkExecutorID, "1").StatusPhase(corev1.PodRunning).Obj(),
					*executorPod.Clone().Name("old-exec-2").Label(sparkcommon.LabelSubmissionID, "sub-0").
						Label(sparkcommon.LabelSparkExecutorID, "2").StatusPhase(corev1.PodRunning).Obj(),
				},
				executorPods(corev1.PodRunning, 1, 2, 3),
			),
			wantCount: 3,
		},
		"elastic, running above maxExecutors: capped": {
			sparkApp: dynamicSparkApp.Clone().AppState(sparkappv1beta2.ApplicationStateRunning).Obj(),
			pods: slices.Concat(
				executorPods(corev1.PodRunning, 1, 2, 3, 4),
				executorPods(corev1.PodPending, 5),
			),
			wantCount: 4,
		},
		"elastic, running below minExecutors: count stays at minExecutors": {
			sparkApp: elasticSparkApp.Clone().
				DynamicAllocation(&sparkappv1beta2.DynamicAllocation{Enabled: true, MinExecutors: new(int32(1))}).
				AppState(sparkappv1beta2.ApplicationStateRunning).
				Obj(),
			pods:      executorPods(corev1.PodSucceeded, 1, 2),
			wantCount: 1,
		},
		"elastic, running below minExecutors with failed executors during ramp-up: holds the initial executors, the driver is replacing them": {
			sparkApp: elasticSparkApp.Clone().
				DynamicAllocation(&sparkappv1beta2.DynamicAllocation{Enabled: true, MinExecutors: new(int32(1))}).
				AppState(sparkappv1beta2.ApplicationStateRunning).
				Obj(),
			pods:      executorPods(corev1.PodFailed, 1, 2),
			wantCount: 2,
		},
		"elastic, running below minExecutors with failed executors after the executors started: count stays at minExecutors": {
			sparkApp: elasticSparkApp.Clone().
				DynamicAllocation(&sparkappv1beta2.DynamicAllocation{Enabled: true, MinExecutors: new(int32(1))}).
				AppState(sparkappv1beta2.ApplicationStateRunning).
				Annotation(executorsStartedSubmissionAnnotation, "sub-1").
				Obj(),
			pods:      executorPods(corev1.PodFailed, 1, 2),
			wantCount: 1,
		},
		"elastic, completed: does not fall back to initial executors": {
			sparkApp: dynamicSparkApp.Clone().
				AppState(sparkappv1beta2.ApplicationStateCompleted).
				Annotation(maxExecutorIDAnnotation, "2").
				Annotation(maxExecutorIDSubmissionAnnotation, "sub-1").
				Obj(),
			wantCount: 0,
		},
		"elastic, succeeding: does not fall back to initial executors": {
			sparkApp: dynamicSparkApp.Clone().
				AppState(sparkappv1beta2.ApplicationStateSucceeding).
				Annotation(maxExecutorIDAnnotation, "2").
				Annotation(maxExecutorIDSubmissionAnnotation, "sub-1").
				Obj(),
			wantCount: 0,
		},
		"elastic, failing: does not fall back to initial executors": {
			sparkApp: dynamicSparkApp.Clone().
				AppState(sparkappv1beta2.ApplicationStateFailing).
				Annotation(maxExecutorIDAnnotation, "2").
				Annotation(maxExecutorIDSubmissionAnnotation, "sub-1").
				Obj(),
			wantCount: 0,
		},
		"elastic, failed: does not fall back to initial executors": {
			sparkApp: dynamicSparkApp.Clone().
				AppState(sparkappv1beta2.ApplicationStateFailed).
				Annotation(maxExecutorIDAnnotation, "2").
				Annotation(maxExecutorIDSubmissionAnnotation, "sub-1").
				Obj(),
			wantCount: 0,
		},
		"elastic, minExecutors above instances: count raised to minExecutors": {
			sparkApp: elasticSparkApp.Clone().
				DynamicAllocation(&sparkappv1beta2.DynamicAllocation{Enabled: true, MinExecutors: new(int32(3))}).
				Obj(),
			wantCount: 3,
		},
		"elastic, initialExecutors above instances and minExecutors: count raised to initialExecutors": {
			sparkApp: elasticSparkApp.Clone().
				DynamicAllocation(&sparkappv1beta2.DynamicAllocation{Enabled: true, MinExecutors: new(int32(1)), InitialExecutors: new(int32(3))}).
				Obj(),
			wantCount: 3,
		},
		"elastic, running before the initialExecutors are created: initial executors": {
			sparkApp: elasticSparkApp.Clone().
				DynamicAllocation(&sparkappv1beta2.DynamicAllocation{Enabled: true, InitialExecutors: new(int32(3))}).
				AppState(sparkappv1beta2.ApplicationStateRunning).
				Obj(),
			pods:      executorPods(corev1.PodRunning, 1, 2),
			wantCount: 3,
		},
		"elastic, executor stopped before the initial executors are created: counts pods": {
			sparkApp: rampingUpSparkApp.Clone().Obj(),
			pods: slices.Concat(
				executorPods(corev1.PodRunning, 1, 2),
				executorPods(corev1.PodSucceeded, 3),
			),
			wantCount: 2,
		},
		"elastic, executor pod deleted before the initial executors are created: counts pods": {
			sparkApp: rampingUpSparkApp.Clone().
				Annotation(maxExecutorIDAnnotation, "3").
				Annotation(maxExecutorIDSubmissionAnnotation, "sub-1").
				Obj(),
			pods:      executorPods(corev1.PodRunning, 1, 2),
			wantCount: 2,
		},
		"elastic, executor ID recorded for a previous submission: initial executors": {
			sparkApp: rampingUpSparkApp.Clone().
				Annotation(maxExecutorIDAnnotation, "10").
				Annotation(maxExecutorIDSubmissionAnnotation, "sub-0").
				Obj(),
			pods:      executorPods(corev1.PodRunning, 1, 2),
			wantCount: 4,
		},
		"elastic, completed: count stays at minExecutors": {
			sparkApp: elasticSparkApp.Clone().
				DynamicAllocation(&sparkappv1beta2.DynamicAllocation{Enabled: true, MinExecutors: new(int32(1))}).
				AppState(sparkappv1beta2.ApplicationStateCompleted).
				Obj(),
			pods:      executorPods(corev1.PodSucceeded, 1, 2),
			wantCount: 1,
		},
		"elastic, partial scale-up: minCount is the executor count": {
			sparkApp:     partialSparkApp.Clone().Obj(),
			pods:         partialExecutorPods,
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlicesWithPartialReplicaScaleUp: true},
			wantCount:    3,
			wantMinCount: new(int32(3)),
		},
		"elastic, partial scale-up, dynamic allocation in sparkConf: minCount is the executor count": {
			sparkApp: partialSparkApp.Clone().
				DynamicAllocation(nil).
				SparkConf("spark.dynamicAllocation.enabled", "true").
				SparkConf("spark.dynamicAllocation.minExecutors", "2").
				SparkConf("spark.dynamicAllocation.maxExecutors", "4").
				Obj(),
			pods:         partialExecutorPods,
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlicesWithPartialReplicaScaleUp: true},
			wantCount:    3,
			wantMinCount: new(int32(3)),
		},
		"elastic, partial scale-up without minExecutors: minCount is the executor count": {
			sparkApp: partialSparkApp.Clone().
				DynamicAllocation(&sparkappv1beta2.DynamicAllocation{Enabled: true, MaxExecutors: new(int32(4))}).
				Obj(),
			pods:         partialExecutorPods,
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlicesWithPartialReplicaScaleUp: true},
			wantCount:    3,
			wantMinCount: new(int32(3)),
		},
		"elastic, partial scale-up, minExecutors above maxExecutors: minCount is the executor count": {
			sparkApp: partialSparkApp.Clone().
				DynamicAllocation(&sparkappv1beta2.DynamicAllocation{Enabled: true, MinExecutors: new(int32(3)), MaxExecutors: new(int32(2))}).
				Obj(),
			pods:         partialExecutorPods,
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlicesWithPartialReplicaScaleUp: true},
			wantCount:    3,
			wantMinCount: new(int32(3)),
		},
		"elastic, partial scale-up, completed: minCount follows the count": {
			sparkApp: partialSparkApp.Clone().
				AppState(sparkappv1beta2.ApplicationStateCompleted).
				Obj(),
			pods:         executorPods(corev1.PodSucceeded, 1, 2, 3),
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlicesWithPartialReplicaScaleUp: true},
			wantCount:    1,
			wantMinCount: new(int32(1)),
		},
		"elastic, partial scale-up gate disabled: atomic": {
			sparkApp:  partialSparkApp.Clone().Obj(),
			pods:      partialExecutorPods,
			wantCount: 3,
		},
		"elastic, default scale-up strategy: atomic": {
			sparkApp: partialSparkApp.Clone().
				Annotation(constants.ElasticJobScaleUpStrategyAnnotationKey, "").
				Obj(),
			pods:         partialExecutorPods,
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlicesWithPartialReplicaScaleUp: true},
			wantCount:    3,
		},
		"elastic, partial scale-up without dynamic allocation: atomic": {
			sparkApp: partialSparkApp.Clone().
				DynamicAllocation(nil).
				Obj(),
			pods:         partialExecutorPods,
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlicesWithPartialReplicaScaleUp: true},
			wantCount:    1,
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlicesForSparkApplication, true)
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			ctx, _ := utiltesting.ContextWithLog(t)
			cl := utiltesting.NewClientBuilder().WithLists(&corev1.PodList{Items: tc.pods}).Build()

			got, err := (*SparkApplication)(tc.sparkApp).PodSets(ctx, cl)
			if err != nil {
				t.Fatalf("PodSets() returned error: %v", err)
			}
			if got[1].Count != tc.wantCount {
				t.Errorf("executor PodSet Count = %d, want %d", got[1].Count, tc.wantCount)
			}
			if diff := cmp.Diff(tc.wantMinCount, got[1].MinCount); diff != "" {
				t.Errorf("executor PodSet MinCount (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestGetWorkloadNameExtraPart(t *testing.T) {
	testCases := map[string]struct {
		generation         int64
		submissionAttempts int32
		maxExecutorID      string
		want               string
	}{
		"not submitted: generation only": {
			generation: 1,
			want:       "1",
		},
		"submitted, no executors yet": {
			generation:         1,
			submissionAttempts: 1,
			want:               "1_1",
		},
		"submitted with executors": {
			generation:         1,
			submissionAttempts: 1,
			maxExecutorID:      "1003",
			want:               "1_1_1003",
		},
		"rerun starts a new attempt": {
			generation:         1,
			submissionAttempts: 2,
			maxExecutorID:      "2",
			want:               "1_2_2",
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			sparkApp := sparkapplicationtesting.MakeSparkApplication("sparkapp", "ns").Obj()
			sparkApp.Generation = tc.generation
			sparkApp.Status.SubmissionAttempts = tc.submissionAttempts
			if tc.maxExecutorID != "" {
				sparkApp.Annotations = map[string]string{maxExecutorIDAnnotation: tc.maxExecutorID}
			}
			if got := (*SparkApplication)(sparkApp).GetWorkloadNameExtraPart(); got != tc.want {
				t.Errorf("GetWorkloadNameExtraPart() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGetCustomAnnotations(t *testing.T) {
	executorPod := testingpod.MakePod("", "ns").
		Label(sparkcommon.LabelSparkAppName, "sparkapp").
		Label(sparkcommon.LabelSparkRole, sparkcommon.SparkRoleExecutor).
		Label(sparkcommon.LabelSubmissionID, "sub-1")

	elasticSparkApp := sparkapplicationtesting.MakeSparkApplication("sparkapp", "ns").
		Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue)
	// dynamicSparkApp starts with 2 executors.
	dynamicSparkApp := elasticSparkApp.Clone().
		SubmissionID("sub-1").
		DynamicAllocation(&sparkappv1beta2.DynamicAllocation{Enabled: true, InitialExecutors: new(int32(2))})
	readyExecutorPod := func(id int) corev1.Pod {
		return *executorPod.Clone().
			Name(fmt.Sprintf("exec-%d", id)).
			Label(sparkcommon.LabelSparkExecutorID, strconv.Itoa(id)).
			StatusPhase(corev1.PodRunning).
			StatusConditions(corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionTrue}).
			Obj()
	}

	testCases := map[string]struct {
		sparkApp *sparkappv1beta2.SparkApplication
		pods     []corev1.Pod
		want     map[string]string
	}{
		"not elastic": {
			sparkApp: sparkapplicationtesting.MakeSparkApplication("sparkapp", "ns").SubmissionID("sub-1").Obj(),
			pods: []corev1.Pod{
				*executorPod.Clone().Name("exec-1").Label(sparkcommon.LabelSparkExecutorID, "1").Obj(),
			},
			want: nil,
		},
		"not submitted": {
			sparkApp: elasticSparkApp.Clone().Obj(),
			want:     nil,
		},
		"submitted, no executors yet": {
			sparkApp: elasticSparkApp.Clone().SubmissionID("sub-1").Obj(),
			want:     map[string]string{maxExecutorIDAnnotation: "0", maxExecutorIDSubmissionAnnotation: "sub-1"},
		},
		"executors untracked by the operator": {
			sparkApp: elasticSparkApp.Clone().SubmissionID("sub-1").Obj(),
			pods: []corev1.Pod{
				*executorPod.Clone().Name("exec-1").Label(sparkcommon.LabelSparkExecutorID, "1").Obj(),
				*executorPod.Clone().Name("exec-1003").Label(sparkcommon.LabelSparkExecutorID, "1003").Obj(),
				*executorPod.Clone().Name("exec-1002").Label(sparkcommon.LabelSparkExecutorID, "1002").Obj(),
			},
			want: map[string]string{maxExecutorIDAnnotation: "1003", maxExecutorIDSubmissionAnnotation: "sub-1"},
		},
		"failed executor is recorded in annotations": {
			sparkApp: elasticSparkApp.Clone().SubmissionID("sub-1").Obj(),
			pods: []corev1.Pod{
				*executorPod.Clone().Name("exec-1").Label(sparkcommon.LabelSparkExecutorID, "1").StatusPhase(corev1.PodRunning).Obj(),
				*executorPod.Clone().Name("exec-2").Label(sparkcommon.LabelSparkExecutorID, "2").StatusPhase(corev1.PodFailed).Obj(),
			},
			want: map[string]string{
				maxExecutorIDAnnotation:           "2",
				maxExecutorIDSubmissionAnnotation: "sub-1",
				failedExecutorIDsAnnotation:       "2",
			},
		},
		"executors of a previous submission are ignored": {
			sparkApp: elasticSparkApp.Clone().SubmissionID("sub-1").Obj(),
			pods: []corev1.Pod{
				*executorPod.Clone().Name("old-exec-5").Label(sparkcommon.LabelSubmissionID, "sub-0").Label(sparkcommon.LabelSparkExecutorID, "5").Obj(),
				*executorPod.Clone().Name("exec-1").Label(sparkcommon.LabelSparkExecutorID, "1").Obj(),
			},
			want: map[string]string{maxExecutorIDAnnotation: "1", maxExecutorIDSubmissionAnnotation: "sub-1"},
		},
		"executor pods deleted: keeps the recorded ID": {
			sparkApp: elasticSparkApp.Clone().SubmissionID("sub-1").
				Annotation(maxExecutorIDAnnotation, "5").
				Annotation(maxExecutorIDSubmissionAnnotation, "sub-1").
				Obj(),
			pods: []corev1.Pod{
				*executorPod.Clone().Name("exec-1").Label(sparkcommon.LabelSparkExecutorID, "1").Obj(),
			},
			want: map[string]string{maxExecutorIDAnnotation: "5", maxExecutorIDSubmissionAnnotation: "sub-1"},
		},
		"new submission: resets the recorded ID": {
			sparkApp: elasticSparkApp.Clone().SubmissionID("sub-1").
				Annotation(maxExecutorIDAnnotation, "5").
				Annotation(maxExecutorIDSubmissionAnnotation, "sub-0").
				Obj(),
			pods: []corev1.Pod{
				*executorPod.Clone().Name("exec-1").Label(sparkcommon.LabelSparkExecutorID, "1").Obj(),
			},
			want: map[string]string{maxExecutorIDAnnotation: "1", maxExecutorIDSubmissionAnnotation: "sub-1"},
		},
		"dynamic allocation, the starting executors ready: records that the executors started": {
			sparkApp: dynamicSparkApp.Clone().Obj(),
			pods:     []corev1.Pod{readyExecutorPod(1), readyExecutorPod(2)},
			want: map[string]string{
				maxExecutorIDAnnotation:              "2",
				maxExecutorIDSubmissionAnnotation:    "sub-1",
				executorsStartedSubmissionAnnotation: "sub-1",
			},
		},
		"dynamic allocation, fewer than the starting executors ready": {
			sparkApp: dynamicSparkApp.Clone().Obj(),
			pods: []corev1.Pod{
				readyExecutorPod(1),
				*executorPod.Clone().Name("exec-2").Label(sparkcommon.LabelSparkExecutorID, "2").StatusPhase(corev1.PodPending).Obj(),
			},
			want: map[string]string{maxExecutorIDAnnotation: "2", maxExecutorIDSubmissionAnnotation: "sub-1"},
		},
		"dynamic allocation, started in this submission: keeps the record": {
			sparkApp: dynamicSparkApp.Clone().Annotation(executorsStartedSubmissionAnnotation, "sub-1").Obj(),
			pods:     []corev1.Pod{readyExecutorPod(1)},
			want:     map[string]string{maxExecutorIDAnnotation: "1", maxExecutorIDSubmissionAnnotation: "sub-1"},
		},
		"dynamic allocation, started in this submission: failed executors are no longer recorded": {
			sparkApp: dynamicSparkApp.Clone().Annotation(executorsStartedSubmissionAnnotation, "sub-1").Obj(),
			pods: []corev1.Pod{
				readyExecutorPod(1),
				*executorPod.Clone().Name("exec-2").Label(sparkcommon.LabelSparkExecutorID, "2").StatusPhase(corev1.PodFailed).Obj(),
			},
			want: map[string]string{maxExecutorIDAnnotation: "2", maxExecutorIDSubmissionAnnotation: "sub-1"},
		},
		"dynamic allocation, target lowered before the starting executors were all created: records once the target is ready": {
			sparkApp: elasticSparkApp.Clone().SubmissionID("sub-1").
				AppState(sparkappv1beta2.ApplicationStateRunning).
				DynamicAllocation(&sparkappv1beta2.DynamicAllocation{Enabled: true, InitialExecutors: new(int32(4)), MinExecutors: new(int32(1))}).
				Annotation(maxExecutorIDAnnotation, "2").
				Annotation(maxExecutorIDSubmissionAnnotation, "sub-1").
				Obj(),
			pods: []corev1.Pod{readyExecutorPod(1)},
			want: map[string]string{
				maxExecutorIDAnnotation:              "2",
				maxExecutorIDSubmissionAnnotation:    "sub-1",
				executorsStartedSubmissionAnnotation: "sub-1",
			},
		},
		"dynamic allocation, ramping up: waits for the starting executors": {
			sparkApp: elasticSparkApp.Clone().SubmissionID("sub-1").
				AppState(sparkappv1beta2.ApplicationStateRunning).
				DynamicAllocation(&sparkappv1beta2.DynamicAllocation{Enabled: true, InitialExecutors: new(int32(4)), MinExecutors: new(int32(1))}).
				Obj(),
			pods: []corev1.Pod{
				readyExecutorPod(1),
				*executorPod.Clone().Name("exec-2").Label(sparkcommon.LabelSparkExecutorID, "2").StatusPhase(corev1.PodPending).Obj(),
			},
			want: map[string]string{maxExecutorIDAnnotation: "2", maxExecutorIDSubmissionAnnotation: "sub-1"},
		},
		"dynamic allocation, started in a previous submission: records the new one once its executors started": {
			sparkApp: dynamicSparkApp.Clone().Annotation(executorsStartedSubmissionAnnotation, "sub-0").Obj(),
			pods:     []corev1.Pod{readyExecutorPod(1), readyExecutorPod(2)},
			want: map[string]string{
				maxExecutorIDAnnotation:              "2",
				maxExecutorIDSubmissionAnnotation:    "sub-1",
				executorsStartedSubmissionAnnotation: "sub-1",
			},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlicesForSparkApplication, true)
			ctx, _ := utiltesting.ContextWithLog(t)
			cl := utiltesting.NewClientBuilder().WithLists(&corev1.PodList{Items: tc.pods}).Build()

			got, err := (*SparkApplication)(tc.sparkApp).GetCustomAnnotations(ctx, cl)
			if err != nil {
				t.Fatalf("GetCustomAnnotations() returned error: %v", err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("GetCustomAnnotations() mismatch (-want,+got):\n%s", diff)
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

	elasticPodSetsInfo := func(workloadName, sliceName string) []podset.PodSetInfo {
		annotations := func() map[string]string {
			annotations := map[string]string{kueue.WorkloadAnnotation: workloadName}
			if sliceName != "" {
				annotations[kueue.WorkloadSliceNameAnnotation] = sliceName
			}
			return annotations
		}
		return []podset.PodSetInfo{
			{Name: driverPodSetName, Annotations: annotations()},
			{Name: executorPodSetName, Annotations: annotations()},
		}
	}
	// previouslyAdmitted is the spec after the restore on preemption: it still carries the
	// annotations of the Workload that admitted it before.
	previouslyAdmitted := testSparkApp.Clone().
		DriverAnnotation(kueue.WorkloadAnnotation, "wl-1").
		DriverAnnotation(kueue.WorkloadSliceNameAnnotation, "wl-1").
		DriverAnnotation("user-annotation", "kept").
		ExecutorAnnotation(kueue.WorkloadAnnotation, "wl-1").
		ExecutorAnnotation(kueue.WorkloadSliceNameAnnotation, "wl-1")
	admittedAgain := func(workloadName, sliceName string) *sparkappv1beta2.SparkApplication {
		return testSparkApp.Clone().
			Suspend(false).
			DriverAnnotation(kueue.WorkloadAnnotation, workloadName).
			DriverAnnotation(kueue.WorkloadSliceNameAnnotation, sliceName).
			DriverAnnotation("user-annotation", "kept").
			DriverTemplate(emptyDriverPodTemplateSpec.DeepCopy()).
			ExecutorAnnotation(kueue.WorkloadAnnotation, workloadName).
			ExecutorAnnotation(kueue.WorkloadSliceNameAnnotation, sliceName).
			ExecutorTemplate(emptyExecutorPodTemplateSpec.DeepCopy()).
			Obj()
	}

	cases := map[string]struct {
		featureGates map[featuregate.Feature]bool
		sparkApp     *sparkappv1beta2.SparkApplication
		podsetsInfo  []podset.PodSetInfo
		wantSparkApp *sparkappv1beta2.SparkApplication
		wantErr      bool
	}{
		"should add to the SparkApplication specified in the PodSet info": {
			sparkApp: testSparkApp.DeepCopy(),
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
		"should apply different node selectors per role when global nodeSelector is set": {
			sparkApp: testSparkApp.Clone().
				NodeSelector(map[string]string{"zone": "us-east"}).
				Obj(),
			podsetsInfo: []podset.PodSetInfo{
				{
					Name:         "driver",
					NodeSelector: map[string]string{"zone": "us-east", "node-type": "cpu"},
				},
				{
					Name:         "executor",
					NodeSelector: map[string]string{"zone": "us-east", "node-type": "gpu"},
				},
			},
			wantSparkApp: testSparkApp.Clone().
				Suspend(false).
				DriverNodeSelector(map[string]string{"zone": "us-east", "node-type": "cpu"}).
				DriverTemplate(&corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: sparkcommon.SparkDriverContainerName},
						},
					},
				}).
				ExecutorNodeSelector(map[string]string{"zone": "us-east", "node-type": "gpu"}).
				ExecutorTemplate(&corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: sparkcommon.Spark3DefaultExecutorContainerName},
						},
					},
				}).
				Obj(),
			wantErr: false,
		},
		"should replace stale workload annotations of an elastic job admitted again under a replacement slice": {
			sparkApp:     previouslyAdmitted.Clone().Obj(),
			podsetsInfo:  elasticPodSetsInfo("wl-3", "wl-1"),
			wantSparkApp: admittedAgain("wl-3", "wl-1"),
		},
		"should replace stale workload annotations of an elastic job admitted again under a new slice chain": {
			sparkApp:     previouslyAdmitted.Clone().Obj(),
			podsetsInfo:  elasticPodSetsInfo("wl-4", "wl-4"),
			wantSparkApp: admittedAgain("wl-4", "wl-4"),
		},
		"should raise error on a stale workload annotation for a non-elastic admission": {
			sparkApp: testSparkApp.Clone().
				DriverAnnotation(kueue.WorkloadAnnotation, "wl-1").
				ExecutorAnnotation(kueue.WorkloadAnnotation, "wl-1").
				Obj(),
			podsetsInfo: elasticPodSetsInfo("wl-2", ""),
			wantErr:     true,
		},
		"should raise error if the wrong number of PodSet infos is provided": {
			sparkApp: testSparkApp.DeepCopy(),
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
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			ctx, _ := utiltesting.ContextWithLog(t)

			kSparkApp := (*SparkApplication)(tc.sparkApp)
			err := kSparkApp.RunWithPodSetsInfo(ctx, nil, tc.podsetsInfo)
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
	sparkAppWithoutExecutorInstances := testSparkApp.Clone().
		DriverTemplate(emptyDriverPodTemplateSpec.DeepCopy()).
		ExecutorTemplate(emptyExecutorPodTemplateSpec.DeepCopy()).
		Obj()
	sparkAppWithoutExecutorInstances.Spec.Executor.Instances = nil
	sparkAppWithExecutorCountInSparkConf := sparkAppWithoutExecutorInstances.DeepCopy()
	sparkAppWithExecutorCountInSparkConf.Spec.SparkConf = map[string]string{"spark.executor.instances": "5"}
	elasticSparkApp := testSparkApp.Clone().
		Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
		DriverTemplate(emptyDriverPodTemplateSpec.DeepCopy()).
		ExecutorTemplate(emptyExecutorPodTemplateSpec.DeepCopy()).
		ExecutorInstances(2).
		Obj()

	cases := map[string]struct {
		sparkApp     *sparkappv1beta2.SparkApplication
		podsetsInfo  []podset.PodSetInfo
		wantSparkApp *sparkappv1beta2.SparkApplication
		wantChanged  bool
	}{
		"should restore PodSet info to the SparkApplication": {
			sparkApp: testSparkApp.DeepCopy(),
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
		"should restore per-role node selectors when global nodeSelector is set": {
			sparkApp: testSparkApp.Clone().
				NodeSelector(map[string]string{"zone": "us-east"}).
				Obj(),
			podsetsInfo: []podset.PodSetInfo{
				{
					Name:         "driver",
					NodeSelector: map[string]string{"zone": "us-east", "node-type": "cpu"},
				},
				{
					Name:         "executor",
					NodeSelector: map[string]string{"zone": "us-east", "node-type": "gpu"},
					Count:        3,
				},
			},
			wantSparkApp: testSparkApp.Clone().
				DriverNodeSelector(map[string]string{"zone": "us-east", "node-type": "cpu"}).
				DriverTemplate(&corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: sparkcommon.SparkDriverContainerName},
						},
					},
				}).
				ExecutorNodeSelector(map[string]string{"zone": "us-east", "node-type": "gpu"}).
				ExecutorTemplate(&corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: sparkcommon.Spark3DefaultExecutorContainerName},
						},
					},
				}).
				ExecutorInstances(3).
				Obj(),
			wantChanged: true,
		},
		"should keep executor instances unset when the executor PodSet count is 0": {
			sparkApp: sparkAppWithoutExecutorInstances.DeepCopy(),
			podsetsInfo: []podset.PodSetInfo{
				{Name: "driver"},
				{Name: "executor", Count: 0},
			},
			wantSparkApp: sparkAppWithoutExecutorInstances.DeepCopy(),
			wantChanged:  false,
		},
		"should restore spec.executor.instances as unset when the executor PodSet count is 0": {
			sparkApp: func() *sparkappv1beta2.SparkApplication {
				app := sparkAppWithoutExecutorInstances.DeepCopy()
				app.Spec.Executor.Instances = new(int32(2))
				return app
			}(),
			podsetsInfo: []podset.PodSetInfo{
				{Name: driverPodSetName},
				{Name: executorPodSetName, Count: 0},
			},
			wantSparkApp: sparkAppWithoutExecutorInstances.DeepCopy(),
			wantChanged:  true,
		},
		"should keep executor instances of an elastic SparkApplication": {
			sparkApp: elasticSparkApp.DeepCopy(),
			podsetsInfo: []podset.PodSetInfo{
				{Name: "driver"},
				{Name: "executor", Count: 4},
			},
			wantSparkApp: elasticSparkApp.DeepCopy(),
			wantChanged:  false,
		},
		"should keep executor instances of an elastic SparkApplication with no executors": {
			sparkApp: elasticSparkApp.DeepCopy(),
			podsetsInfo: []podset.PodSetInfo{
				{Name: "driver"},
				{Name: "executor", Count: 0},
			},
			wantSparkApp: elasticSparkApp.DeepCopy(),
			wantChanged:  false,
		},
		"should keep the executor count in sparkConf when it is unchanged": {
			sparkApp: sparkAppWithExecutorCountInSparkConf.DeepCopy(),
			podsetsInfo: []podset.PodSetInfo{
				{Name: driverPodSetName},
				{Name: executorPodSetName, Count: 5},
			},
			wantSparkApp: sparkAppWithExecutorCountInSparkConf.DeepCopy(),
			wantChanged:  false,
		},
		// Admitted with 3 executors, then spark.executor.instances was edited to 5.
		"should keep an edited executor count in sparkConf": {
			sparkApp: sparkAppWithExecutorCountInSparkConf.DeepCopy(),
			podsetsInfo: []podset.PodSetInfo{
				{Name: driverPodSetName},
				{Name: executorPodSetName, Count: 3},
			},
			wantSparkApp: sparkAppWithExecutorCountInSparkConf.DeepCopy(),
			wantChanged:  false,
		},
		"should not modify the SparkApplication  if the wrong number of PodSet infos is provided": {
			sparkApp: testSparkApp.DeepCopy(),
			podsetsInfo: []podset.PodSetInfo{
				{
					Name:            "driver",
					NodeSelector:    maps.Clone(nodeSelector),
					Tolerations:     []corev1.Toleration{*toleration.DeepCopy()},
					SchedulingGates: []corev1.PodSchedulingGate{*schedulingGate.DeepCopy()},
				},
			},
			wantSparkApp: testSparkApp.DeepCopy(),
			wantChanged:  false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlicesForSparkApplication, true)
			kSparkApp := (*SparkApplication)(tc.sparkApp)
			changed := kSparkApp.RestorePodSetsInfo(t.Context(), tc.podsetsInfo)
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

func TestFinished(t *testing.T) {
	cases := map[string]struct {
		state        sparkappv1beta2.ApplicationStateType
		wantSuccess  bool
		wantFinished bool
	}{
		"running": {
			state: sparkappv1beta2.ApplicationStateRunning,
		},
		"submission failed, the operator may still resubmit": {
			state: sparkappv1beta2.ApplicationStateFailedSubmission,
		},
		"completed": {
			state:        sparkappv1beta2.ApplicationStateCompleted,
			wantSuccess:  true,
			wantFinished: true,
		},
		"failed": {
			state:        sparkappv1beta2.ApplicationStateFailed,
			wantFinished: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			sparkApp := sparkapplicationtesting.MakeSparkApplication("test-sparkapp", "ns").Obj()
			sparkApp.Status.AppState.State = tc.state

			_, gotSuccess, gotFinished := (*SparkApplication)(sparkApp).Finished(t.Context())
			if gotSuccess != tc.wantSuccess {
				t.Errorf("unexpected success: want %v, got %v", tc.wantSuccess, gotSuccess)
			}
			if gotFinished != tc.wantFinished {
				t.Errorf("unexpected finished: want %v, got %v", tc.wantFinished, gotFinished)
			}
		})
	}
}

func TestReconciler(t *testing.T) {
	now := time.Now().Truncate(time.Second)

	testNamespace := utiltesting.MakeNamespaceWrapper("ns").Label(corev1.LabelMetadataName, "ns").Obj()
	const testSparkAppUID types.UID = "test-sparkapp-uid"
	testSparkApp := sparkapplicationtesting.MakeSparkApplication("test-sparkapp", testNamespace.Name)

	// Helpers for the PodsReady cases: build a SparkApplication whose status
	// simulates "driver Running and N executors in the given state". The
	// framework calls PodsReady() during Reconcile and we assert the resulting
	// Workload condition.
	withUID := func(s *sparkappv1beta2.SparkApplication) *sparkappv1beta2.SparkApplication {
		s.UID = testSparkAppUID
		return s
	}
	withExecutorsRunning := func(s *sparkappv1beta2.SparkApplication, n int32) *sparkappv1beta2.SparkApplication {
		s.Spec.Executor.Instances = new(n)
		s.Status.AppState.State = sparkappv1beta2.ApplicationStateRunning
		states := make(map[string]sparkappv1beta2.ExecutorState, n)
		for i := int32(1); i <= n; i++ {
			states[fmt.Sprintf("%s-exec-%d", s.Name, i)] = sparkappv1beta2.ExecutorStateRunning
		}
		s.Status.ExecutorState = states
		s.Status.SubmissionID = "sub-1"
		return s
	}
	// readyExecutorPods are the n executor pods of s, running and ready, from which
	// PodsReady counts the ready executors.
	readyExecutorPods := func(s *sparkappv1beta2.SparkApplication, n int32) []corev1.Pod {
		pods := make([]corev1.Pod, 0, n)
		for i := int32(1); i <= n; i++ {
			pods = append(pods, corev1.Pod{
				Name:      fmt.Sprintf("%s-exec-%d", s.Name, i),
				Namespace: s.Namespace,
				Labels: map[string]string{
					sparkcommon.LabelSparkAppName: s.Name,
					sparkcommon.LabelSparkRole:    sparkcommon.SparkRoleExecutor,
					sparkcommon.LabelSubmissionID: s.Status.SubmissionID,
				},
				Status: corev1.PodStatus{
					Phase:      corev1.PodRunning,
					Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
				},
			})
		}
		return pods
	}
	withDriverRunningOnly := func(s *sparkappv1beta2.SparkApplication, expected int32) *sparkappv1beta2.SparkApplication {
		s.Spec.Executor.Instances = new(expected)
		s.Status.AppState.State = sparkappv1beta2.ApplicationStateRunning
		// No ExecutorState entries — simulates the Spark Operator state where
		// AppState flips to Running as soon as the driver starts even though
		// executors haven't reported ready yet (the bug this fix addresses).
		return s
	}

	// Build a workload whose PodSets are derived from the same SparkApplication
	// the framework will Reconcile, so EquivalentToWorkload returns true and
	// the workload is treated as "matching" rather than recreated.
	makeAdmittedWorkload := func(s *sparkappv1beta2.SparkApplication) *utiltestingapi.WorkloadWrapper {
		t.Helper()
		podSets, err := (*SparkApplication)(s).PodSets(t.Context(), nil)
		if err != nil {
			t.Fatalf("PodSets returned error during test setup: %v", err)
		}
		psas := make([]kueue.PodSetAssignment, 0, len(podSets))
		for i := range podSets {
			psas = append(psas, utiltestingapi.MakePodSetAssignment(podSets[i].Name).Count(podSets[i].Count).Obj())
		}
		return utiltestingapi.MakeWorkload("wl", testNamespace.Name).
			Finalizers(kueue.ResourceInUseFinalizerName).
			ControllerReference(gvk, s.Name, string(s.UID)).
			PodSets(podSets...).
			ReserveQuotaAt(
				utiltestingapi.MakeAdmission("cq").PodSets(psas...).Obj(),
				now,
			).
			AdmittedAt(true, now)
	}

	baseWaitForPodsReadyConf := &configapi.WaitForPodsReady{}

	// SparkApp variants used by the PodsReady cases.
	sparkAppDriverRunningOnly := withDriverRunningOnly(withUID(testSparkApp.DeepCopy()), 2)
	sparkAppAllExecutorsReady := withExecutorsRunning(withUID(testSparkApp.DeepCopy()), 2)
	// Dynamic allocation variant: Instances=10, MinExecutors=5, 5 executors Running.
	// PodsReady should use MinExecutors (5) as the expected count, not Instances (10).
	sparkAppDynamicAllocation := withExecutorsRunning(withUID(testSparkApp.DeepCopy()), 5)
	sparkAppDynamicAllocation.Spec.Executor.Instances = new(int32(10))
	sparkAppDynamicAllocation.Spec.DynamicAllocation = &sparkappv1beta2.DynamicAllocation{
		Enabled:      true,
		MinExecutors: new(int32(5)),
	}

	cases := map[string]struct {
		reconcilerOptions []jobframework.Option
		sparkApp          *sparkappv1beta2.SparkApplication
		pods              []corev1.Pod
		workloads         []kueue.Workload
		wantWorkloads     []kueue.Workload
	}{
		"workload is created with the corresponding podsets": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(true),
				jobframework.WithManagedJobsNamespaceSelector(labels.Everything()),
			},
			sparkApp: testSparkApp.DeepCopy(),
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(testSparkApp.Name, testSparkApp.Namespace).
					PodSets(
						*utiltestingapi.MakePodSet("driver", 1).Obj(),
						*utiltestingapi.MakePodSet("executor", 1).Obj(),
					).
					Obj(),
			},
		},
		"PodsReady stays False/WaitForStart while AppState=Running but no executors reported ready": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(true),
				jobframework.WithManagedJobsNamespaceSelector(labels.Everything()),
				jobframework.WithWaitForPodsReady(baseWaitForPodsReadyConf),
			},
			sparkApp: sparkAppDriverRunningOnly,
			workloads: []kueue.Workload{
				*makeAdmittedWorkload(sparkAppDriverRunningOnly).Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*makeAdmittedWorkload(sparkAppDriverRunningOnly).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadPodsReady,
						Status:  metav1.ConditionFalse,
						Reason:  kueue.WorkloadWaitForStart,
						Message: "Not all pods are ready or succeeded",
					}).
					Obj(),
			},
		},
		"PodsReady becomes True/Started after AppState=Running and all executors reach Running": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(true),
				jobframework.WithManagedJobsNamespaceSelector(labels.Everything()),
				jobframework.WithWaitForPodsReady(baseWaitForPodsReadyConf),
			},
			sparkApp: sparkAppAllExecutorsReady,
			pods:     readyExecutorPods(sparkAppAllExecutorsReady, 2),
			workloads: []kueue.Workload{
				*makeAdmittedWorkload(sparkAppAllExecutorsReady).Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*makeAdmittedWorkload(sparkAppAllExecutorsReady).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadPodsReady,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadStarted,
						Message: "All pods reached readiness and the workload is running",
					}).
					Obj(),
			},
		},
		"PodsReady becomes True/Started with dynamic allocation when MinExecutors executors are ready": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(true),
				jobframework.WithManagedJobsNamespaceSelector(labels.Everything()),
				jobframework.WithWaitForPodsReady(baseWaitForPodsReadyConf),
			},
			sparkApp: sparkAppDynamicAllocation,
			pods:     readyExecutorPods(sparkAppDynamicAllocation, 5),
			workloads: []kueue.Workload{
				*makeAdmittedWorkload(sparkAppDynamicAllocation).Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*makeAdmittedWorkload(sparkAppDynamicAllocation).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadPodsReady,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadStarted,
						Message: "All pods reached readiness and the workload is running",
					}).
					Obj(),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)

			clientBuilder := utiltesting.NewClientBuilder(sparkappv1beta2.AddToScheme).
				WithInterceptorFuncs(interceptor.Funcs{
					SubResourceApply: utiltesting.TreatSSAAsStrategicMergeForApplyConfiguration,
				})
			objs := []client.Object{tc.sparkApp, testNamespace}
			for i := range tc.pods {
				objs = append(objs, &tc.pods[i])
			}
			kClient := clientBuilder.
				WithObjects(objs...).
				WithStatusSubresource(&kueue.Workload{}).
				Build()
			// Pre-existing workloads must be created via the client (not WithObjects)
			// so that the status subresource is initialized correctly.
			for i := range tc.workloads {
				if err := kClient.Create(ctx, &tc.workloads[i]); err != nil {
					t.Fatalf("Could not create pre-existing workload: %v", err)
				}
			}
			indexer := utiltesting.AsIndexer(clientBuilder)
			if err := SetupIndexes(ctx, indexer); err != nil {
				t.Fatalf("Could not setup indexes: %v", err)
			}
			recorder := &utiltesting.EventRecorder{}
			reconciler, err := NewReconciler(ctx, kClient, indexer, recorder,
				append(tc.reconcilerOptions,
					jobframework.WithCache(schdcache.New(kClient)),
					jobframework.WithClock(testingclock.NewFakeClock(now)),
				)...)
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

// TestGlobalNodeSelectorSurvivesRunRestoreRoundTrip guards the full admit/evict cycle:
// RunWithPodSetsInfo flattens spec.nodeSelector into the per-role selectors and clears
// it, so the PodSet templates recorded in the Workload are the only place
// RestorePodSetsInfo can read the original selector back from.
func TestGlobalNodeSelectorSurvivesRunRestoreRoundTrip(t *testing.T) {
	globalNodeSelector := map[string]string{"zone": "us-east"}
	sparkApp := sparkapplicationtesting.MakeSparkApplication("test-sparkapp", "ns").
		NodeSelector(maps.Clone(globalNodeSelector)).
		ExecutorInstances(3).
		Obj()
	kSparkApp := (*SparkApplication)(sparkApp)

	runRestoreRoundTrip(t, kSparkApp)

	if diff := cmp.Diff(globalNodeSelector, sparkApp.Spec.Driver.NodeSelector); diff != "" {
		t.Errorf("driver node selector mismatch (-want,+got):\n%s", diff)
	}
	if diff := cmp.Diff(globalNodeSelector, sparkApp.Spec.Executor.NodeSelector); diff != "" {
		t.Errorf("executor node selector mismatch (-want,+got):\n%s", diff)
	}
	if sparkApp.Spec.NodeSelector != nil {
		t.Errorf("spec.nodeSelector should stay cleared, got %v", sparkApp.Spec.NodeSelector)
	}
}

func TestSchedulingGatesSurviveRunRestoreRoundTrip(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlicesForSparkApplication, true)
	driverGate := corev1.PodSchedulingGate{Name: "example.com/driver-gate"}
	elasticGate := corev1.PodSchedulingGate{Name: kueue.ElasticJobSchedulingGate}
	sparkApp := sparkapplicationtesting.MakeSparkApplication("test-sparkapp", "ns").
		Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
		DriverTemplate(&corev1.PodTemplateSpec{Spec: corev1.PodSpec{SchedulingGates: []corev1.PodSchedulingGate{driverGate}}}).
		ExecutorInstances(1).
		ExecutorSchedulingGates(elasticGate).
		Obj()
	kSparkApp := (*SparkApplication)(sparkApp)

	runRestoreRoundTrip(t, kSparkApp)

	if diff := cmp.Diff([]corev1.PodSchedulingGate{driverGate}, sparkApp.Spec.Driver.Template.Spec.SchedulingGates); diff != "" {
		t.Errorf("driver scheduling gates mismatch (-want,+got):\n%s", diff)
	}
	if diff := cmp.Diff([]corev1.PodSchedulingGate{elasticGate}, sparkApp.Spec.Executor.Template.Spec.SchedulingGates); diff != "" {
		t.Errorf("executor scheduling gates mismatch (-want,+got):\n%s", diff)
	}
}

// runRestoreRoundTrip starts and stops the SparkApplication with the PodSetInfos the
// reconciler derives from the Workload's PodSets.
func runRestoreRoundTrip(t *testing.T, kSparkApp *SparkApplication) {
	t.Helper()
	podSets, err := kSparkApp.PodSets(t.Context(), nil)
	if err != nil {
		t.Fatalf("PodSets() returned error: %v", err)
	}

	// Mirrors how the reconciler derives the PodSetInfos it feeds back on eviction.
	podSetsInfo := make([]podset.PodSetInfo, 0, len(podSets))
	for i := range podSets {
		podSetsInfo = append(podSetsInfo, podset.FromPodSet(&podSets[i]))
	}

	if err := kSparkApp.RunWithPodSetsInfo(t.Context(), nil, podSetsInfo); err != nil {
		t.Fatalf("RunWithPodSetsInfo() returned error: %v", err)
	}
	kSparkApp.RestorePodSetsInfo(t.Context(), podSetsInfo)
}

func TestPodsReadyCountsReadyExecutorPods(t *testing.T) {
	executorPod := func(name string, phase corev1.PodPhase, ready bool, submissionID string) *corev1.Pod {
		pod := testingpod.MakePod(name, "ns").
			Label(sparkcommon.LabelSparkAppName, "sparkapp").
			Label(sparkcommon.LabelSparkRole, sparkcommon.SparkRoleExecutor).
			Label(sparkcommon.LabelSubmissionID, submissionID).
			StatusPhase(phase).
			Obj()
		if ready {
			pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
		}
		return pod
	}
	ready := func(n int) []*corev1.Pod {
		pods := make([]*corev1.Pod, 0, n)
		for i := range n {
			pods = append(pods, executorPod(fmt.Sprintf("e%d", i+1), corev1.PodRunning, true, "sub-1"))
		}
		return pods
	}
	testCases := map[string]struct {
		// sparkConf replaces spec.executor.instances when set, unless instances is.
		sparkConf map[string]string
		instances *int32
		elastic   bool
		// started is the submission recorded in executorsStartedSubmissionAnnotation.
		started       string
		pods          []*corev1.Pod
		deleting      []*corev1.Pod
		executorState map[string]sparkappv1beta2.ExecutorState
		want          bool
	}{
		"spark.executor.instances in sparkConf: fewer ready": {
			sparkConf: map[string]string{"spark.executor.instances": "3"},
			pods:      ready(2),
			want:      false,
		},
		"spark.executor.instances in sparkConf: all ready": {
			sparkConf: map[string]string{"spark.executor.instances": "3"},
			pods:      ready(3),
			want:      true,
		},
		"elastic, dynamic allocation in sparkConf, starting: fewer than its initialExecutors ready": {
			sparkConf: map[string]string{
				"spark.executor.instances":                 "3",
				"spark.dynamicAllocation.enabled":          "true",
				"spark.dynamicAllocation.minExecutors":     "1",
				"spark.dynamicAllocation.initialExecutors": "4",
			},
			elastic: true,
			pods:    ready(3),
			want:    false,
		},
		"elastic, dynamic allocation in sparkConf, starting: its initialExecutors ready": {
			sparkConf: map[string]string{
				"spark.executor.instances":                 "3",
				"spark.dynamicAllocation.enabled":          "true",
				"spark.dynamicAllocation.minExecutors":     "1",
				"spark.dynamicAllocation.initialExecutors": "4",
			},
			elastic: true,
			pods:    ready(4),
			want:    true,
		},
		"elastic, dynamic allocation in sparkConf, started: minExecutors ready": {
			sparkConf: map[string]string{
				"spark.executor.instances":             "3",
				"spark.dynamicAllocation.enabled":      "true",
				"spark.dynamicAllocation.minExecutors": "1",
			},
			elastic: true,
			started: "sub-1",
			pods:    ready(1),
			want:    true,
		},
		// As with spec.dynamicAllocation, a job that is not elastic expects
		// minExecutors, which it reads with the operator's precedence.
		"not elastic, dynamic allocation in sparkConf: fewer than minExecutors ready": {
			sparkConf: map[string]string{
				"spark.dynamicAllocation.enabled":      "true",
				"spark.dynamicAllocation.minExecutors": "2",
			},
			instances: new(int32(3)),
			pods:      ready(1),
			want:      false,
		},
		"not elastic, dynamic allocation in sparkConf: minExecutors ready": {
			sparkConf: map[string]string{
				"spark.dynamicAllocation.enabled":      "true",
				"spark.dynamicAllocation.minExecutors": "2",
			},
			instances: new(int32(3)),
			pods:      ready(2),
			want:      true,
		},
		"ready executor pods reach the instances, though status.executorState lists none": {
			pods: []*corev1.Pod{executorPod("e6", corev1.PodRunning, true, "sub-1"), executorPod("e7", corev1.PodRunning, true, "sub-1")},
			want: true,
		},
		"completed entries of status.executorState don't count": {
			executorState: map[string]sparkappv1beta2.ExecutorState{
				"e1": sparkappv1beta2.ExecutorStateCompleted,
				"e2": sparkappv1beta2.ExecutorStateCompleted,
			},
			want: false,
		},
		"unready executors don't count": {
			pods: []*corev1.Pod{executorPod("e1", corev1.PodRunning, true, "sub-1"), executorPod("e2", corev1.PodRunning, false, "sub-1")},
			want: false,
		},
		"succeeded executors don't count": {
			pods: []*corev1.Pod{executorPod("e1", corev1.PodRunning, true, "sub-1"), executorPod("e2", corev1.PodSucceeded, true, "sub-1")},
			want: false,
		},
		"terminating executors don't count": {
			pods:     []*corev1.Pod{executorPod("e1", corev1.PodRunning, true, "sub-1")},
			deleting: []*corev1.Pod{executorPod("e2", corev1.PodRunning, true, "sub-1")},
			want:     false,
		},
		"other submissions' executors don't count": {
			pods: []*corev1.Pod{executorPod("e1", corev1.PodRunning, true, "sub-1"), executorPod("e2", corev1.PodRunning, true, "sub-0")},
			want: false,
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			app := sparkapplicationtesting.MakeSparkApplication("sparkapp", "ns").
				ExecutorInstances(2).
				AppState(sparkappv1beta2.ApplicationStateRunning).
				Obj()
			if tc.sparkConf != nil {
				app.Spec.Executor.Instances = tc.instances
				app.Spec.SparkConf = tc.sparkConf
			}
			if tc.elastic {
				features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlices, true)
				app.Annotations = map[string]string{workloadslicing.EnabledAnnotationKey: workloadslicing.EnabledAnnotationValue}
			}
			if tc.started != "" {
				metav1.SetMetaDataAnnotation(&app.ObjectMeta, executorsStartedSubmissionAnnotation, tc.started)
			}
			app.Status.SubmissionID = "sub-1"
			app.Status.ExecutorState = tc.executorState
			builder := utiltesting.NewClientBuilder()
			for _, pod := range tc.pods {
				builder = builder.WithObjects(pod)
			}
			for _, pod := range tc.deleting {
				pod.Finalizers = []string{"test"} // the fake client keeps an object being deleted only with a finalizer
				builder = builder.WithObjects(pod)
			}
			c := builder.Build()
			for _, pod := range tc.deleting {
				if err := c.Delete(ctx, pod); err != nil {
					t.Fatal(err)
				}
			}
			if got := (*SparkApplication)(app).PodsReady(ctx, c); got != tc.want {
				t.Errorf("PodsReady() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestExecutorPodChanged(t *testing.T) {
	base := testingpod.MakePod("e1", "ns").StatusPhase(corev1.PodRunning).Obj()
	ready := base.DeepCopy()
	ready.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	deleting := base.DeepCopy()
	deleting.DeletionTimestamp = &metav1.Time{}
	succeeded := testingpod.MakePod("e1", "ns").StatusPhase(corev1.PodSucceeded).Obj()
	relabeled := base.DeepCopy()
	relabeled.Labels = map[string]string{"other": "x"}
	testCases := map[string]struct {
		old, updated *corev1.Pod
		want         bool
	}{
		"phase change":            {old: base, updated: succeeded, want: true},
		"becomes ready":           {old: base, updated: ready, want: true},
		"becomes unready":         {old: ready, updated: base, want: true},
		"starts being deleted":    {old: base, updated: deleting, want: true},
		"unrelated metadata only": {old: base, updated: relabeled, want: false},
		"no change":               {old: base, updated: base.DeepCopy(), want: false},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			if got := executorPodChanged(event.UpdateEvent{ObjectOld: tc.old, ObjectNew: tc.updated}); got != tc.want {
				t.Errorf("executorPodChanged() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestPodsReadyResubmissionKeepingTheSlice reconciles an elastic SparkApplication
// twice after its driver was resubmitted while Kueue kept the workload slice,
// which is still PodsReady from the previous submission. The first reconcile
// turns it to WaitForRecovery, and the second must not take that as the new
// submission having been ready.
func TestPodsReadyResubmissionKeepingTheSlice(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	testCases := map[string]struct {
		minExecutors *int32
		ready        int
		wantStatus   metav1.ConditionStatus
		wantReason   string
		wantRecorded bool
	}{
		"fewer than the initial executors ready": {
			minExecutors: new(int32(1)),
			ready:        1,
			wantStatus:   metav1.ConditionFalse,
			wantReason:   kueue.WorkloadWaitForRecovery,
		},
		"no executor ready, minExecutors unset": {
			wantStatus: metav1.ConditionFalse,
			wantReason: kueue.WorkloadWaitForRecovery,
		},
		"the initial executors ready": {
			minExecutors: new(int32(1)),
			ready:        3,
			wantStatus:   metav1.ConditionTrue,
			wantReason:   kueue.WorkloadStarted,
			wantRecorded: true,
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			app := sparkapplicationtesting.MakeSparkApplication("sparkapp", "ns").
				Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				ExecutorInstances(3).
				DynamicAllocation(&sparkappv1beta2.DynamicAllocation{Enabled: true, MinExecutors: tc.minExecutors}).
				AppState(sparkappv1beta2.ApplicationStateRunning).
				SubmissionID("sub-2").
				Obj()
			app.Status.LastSubmissionAttemptTime = metav1.NewTime(now.Add(-time.Minute))
			pods := make([]*corev1.Pod, 0, tc.ready)
			for i := range tc.ready {
				pods = append(pods, readyExecutorPodOf("sub-2", i+1))
			}
			// The previous submission made the slice ready an hour ago, and Kueue
			// did not see its driver stop.
			got, gotApp := reconcileReadySlice(t, now, app, 3, pods)
			if got == nil || got.Status != tc.wantStatus || got.Reason != tc.wantReason {
				t.Errorf("PodsReady condition = %v, want %s/%s", got, tc.wantStatus, tc.wantReason)
			}
			if recorded := gotApp.Annotations[executorsStartedSubmissionAnnotation] == "sub-2"; recorded != tc.wantRecorded {
				t.Errorf("executors of sub-2 recorded as started = %v, want %v", recorded, tc.wantRecorded)
			}
		})
	}
}

// TestPodsReadyRunningWithoutTheRecord covers an application that was running
// before Kueue recorded executorsStartedSubmissionAnnotation, as at an upgrade,
// and has scaled below its starting executors.
func TestPodsReadyRunningWithoutTheRecord(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	testCases := map[string]struct {
		pods         []*corev1.Pod
		wantStatus   metav1.ConditionStatus
		wantReason   string
		wantRecorded bool
	}{
		"the executors ready": {
			pods:         []*corev1.Pod{readyExecutorPodOf("sub-1", 4), readyExecutorPodOf("sub-1", 5)},
			wantStatus:   metav1.ConditionTrue,
			wantReason:   kueue.WorkloadStarted,
			wantRecorded: true,
		},
		"an executor terminating": {
			pods: []*corev1.Pod{
				readyExecutorPodOf("sub-1", 4),
				func() *corev1.Pod {
					pod := readyExecutorPodOf("sub-1", 5)
					pod.Finalizers = []string{"test-finalizer"}
					pod.DeletionTimestamp = &metav1.Time{Time: now}
					return pod
				}(),
			},
			wantStatus: metav1.ConditionFalse,
			wantReason: kueue.WorkloadWaitForRecovery,
		},
		"the terminating executor gone": {
			pods:         []*corev1.Pod{readyExecutorPodOf("sub-1", 4)},
			wantStatus:   metav1.ConditionTrue,
			wantReason:   kueue.WorkloadStarted,
			wantRecorded: true,
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			app := sparkapplicationtesting.MakeSparkApplication("sparkapp", "ns").
				Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				Annotation(maxExecutorIDAnnotation, "5").
				Annotation(maxExecutorIDSubmissionAnnotation, "sub-1").
				ExecutorInstances(3).
				DynamicAllocation(&sparkappv1beta2.DynamicAllocation{Enabled: true, MinExecutors: new(int32(1)), MaxExecutors: new(int32(5))}).
				AppState(sparkappv1beta2.ApplicationStateRunning).
				SubmissionID("sub-1").
				Obj()
			app.Status.LastSubmissionAttemptTime = metav1.NewTime(now.Add(-2 * time.Hour))
			// The slice was ready an hour ago, and has shrunk to 2 executors.
			got, gotApp := reconcileReadySlice(t, now, app, 2, tc.pods)
			if got == nil || got.Status != tc.wantStatus || got.Reason != tc.wantReason {
				t.Errorf("PodsReady condition = %v, want %s/%s", got, tc.wantStatus, tc.wantReason)
			}
			if recorded := gotApp.Annotations[executorsStartedSubmissionAnnotation] == "sub-1"; recorded != tc.wantRecorded {
				t.Errorf("executors of sub-1 recorded as started = %v, want %v", recorded, tc.wantRecorded)
			}
		})
	}
}

func readyExecutorPodOf(submissionID string, id int) *corev1.Pod {
	return testingpod.MakePod(fmt.Sprintf("sparkapp-exec-%d", id), "ns").
		Label(sparkcommon.LabelSparkAppName, "sparkapp").
		Label(sparkcommon.LabelSparkRole, sparkcommon.SparkRoleExecutor).
		Label(sparkcommon.LabelSubmissionID, submissionID).
		Label(sparkcommon.LabelSparkExecutorID, strconv.Itoa(id)).
		StatusPhase(corev1.PodRunning).
		StatusConditions(corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionTrue}).
		Obj()
}

// reconcileReadySlice reconciles app twice with a workload slice that admitted the
// given executors two hours ago and became ready an hour ago. It returns
// the PodsReady condition of the only workload, and the SparkApplication.
func reconcileReadySlice(t *testing.T, now time.Time, app *sparkappv1beta2.SparkApplication, executors int32, pods []*corev1.Pod) (*metav1.Condition, *sparkappv1beta2.SparkApplication) {
	t.Helper()
	features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlicesForSparkApplication, true)
	ctx, _ := utiltesting.ContextWithLog(t)
	ns := utiltesting.MakeNamespaceWrapper("ns").Label(corev1.LabelMetadataName, "ns").Obj()
	app.UID = "sparkapp-uid"
	objs := []client.Object{app, ns}
	for _, pod := range pods {
		objs = append(objs, pod)
	}
	podSets, err := (*SparkApplication)(app).PodSets(ctx, nil)
	if err != nil {
		t.Fatalf("PodSets() returned error: %v", err)
	}
	podSets[executorPodSetIndex].Count = executors
	psas := make([]kueue.PodSetAssignment, 0, len(podSets))
	for i := range podSets {
		psas = append(psas, utiltestingapi.MakePodSetAssignment(podSets[i].Name).Count(podSets[i].Count).Obj())
	}
	wl := utiltestingapi.MakeWorkload("wl", "ns").
		Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
		Finalizers(kueue.ResourceInUseFinalizerName).
		ControllerReference(gvk, app.Name, string(app.UID)).
		PodSets(podSets...).
		ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").PodSets(psas...).Obj(), now.Add(-2*time.Hour)).
		AdmittedAt(true, now.Add(-2*time.Hour)).
		Condition(metav1.Condition{
			Type: kueue.WorkloadPodsReady, Status: metav1.ConditionTrue, Reason: kueue.WorkloadStarted,
			LastTransitionTime: metav1.NewTime(now.Add(-time.Hour)),
		}).
		Obj()
	clientBuilder := utiltesting.NewClientBuilder(sparkappv1beta2.AddToScheme).
		WithInterceptorFuncs(interceptor.Funcs{SubResourceApply: utiltesting.TreatSSAAsStrategicMergeForApplyConfiguration})
	kClient := clientBuilder.WithObjects(objs...).WithStatusSubresource(&kueue.Workload{}).Build()
	if err := kClient.Create(ctx, wl); err != nil {
		t.Fatalf("Could not create the workload: %v", err)
	}
	indexer := utiltesting.AsIndexer(clientBuilder)
	if err := SetupIndexes(ctx, indexer); err != nil {
		t.Fatalf("Could not setup indexes: %v", err)
	}
	reconciler, err := NewReconciler(ctx, kClient, indexer, &utiltesting.EventRecorder{},
		jobframework.WithManageJobsWithoutQueueName(true),
		jobframework.WithManagedJobsNamespaceSelector(labels.Everything()),
		jobframework.WithWaitForPodsReady(&configapi.WaitForPodsReady{}),
		jobframework.WithCache(schdcache.New(kClient)),
		jobframework.WithClock(testingclock.NewFakeClock(now)),
	)
	if err != nil {
		t.Fatalf("Error creating the reconciler: %v", err)
	}
	for range 2 {
		if _, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(app)}); err != nil {
			t.Fatalf("Reconcile returned error: %v", err)
		}
	}
	var gotWorkloads kueue.WorkloadList
	if err := kClient.List(ctx, &gotWorkloads); err != nil {
		t.Fatalf("Could not list workloads: %v", err)
	}
	if len(gotWorkloads.Items) != 1 {
		t.Fatalf("Got %d workloads, want the kept slice only", len(gotWorkloads.Items))
	}
	var gotApp sparkappv1beta2.SparkApplication
	if err := kClient.Get(ctx, client.ObjectKeyFromObject(app), &gotApp); err != nil {
		t.Fatalf("Could not get the SparkApplication: %v", err)
	}
	return apimeta.FindStatusCondition(gotWorkloads.Items[0].Status.Conditions, kueue.WorkloadPodsReady), &gotApp
}

func TestPodsReadyDynamicAllocationFloor(t *testing.T) {
	grantedExecutors := func(n int32) *kueue.Workload {
		now := time.Now()
		return utiltestingapi.MakeWorkload("wl", "ns").
			PodSets(*utiltestingapi.MakePodSet(driverPodSetName, 1).Obj(), *utiltestingapi.MakePodSet(executorPodSetName, int(n)).Obj()).
			ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").PodSets(
				utiltestingapi.MakePodSetAssignment(driverPodSetName).Count(1).Obj(),
				utiltestingapi.MakePodSetAssignment(executorPodSetName).Count(n).Obj(),
			).Obj(), now).
			AdmittedAt(true, now).
			Obj()
	}
	initialThree := &sparkappv1beta2.DynamicAllocation{Enabled: true, MinExecutors: new(int32(1)), InitialExecutors: new(int32(3))}
	testCases := map[string]struct {
		dynamicAllocation *sparkappv1beta2.DynamicAllocation
		notElastic        bool
		// started is the submission recorded in executorsStartedSubmissionAnnotation.
		started  string
		workload *kueue.Workload
		running  int
		want     bool
	}{
		"without dynamic allocation: the executor instances": {
			running: 2,
			want:    true,
		},
		"not ready yet: fewer than the initial executors": {
			dynamicAllocation: initialThree,
			running:           2,
			want:              false,
		},
		"not ready yet: the initial executors": {
			dynamicAllocation: initialThree,
			running:           3,
			want:              true,
		},
		"not ready yet: the instances are the initial executors": {
			dynamicAllocation: &sparkappv1beta2.DynamicAllocation{Enabled: true, MinExecutors: new(int32(1))},
			running:           1,
			want:              false,
		},
		"not ready yet: the initial executors are capped at maxExecutors": {
			dynamicAllocation: &sparkappv1beta2.DynamicAllocation{Enabled: true, InitialExecutors: new(int32(3)), MaxExecutors: new(int32(2))},
			running:           2,
			want:              true,
		},
		"not ready yet: granted fewer than the initial executors": {
			dynamicAllocation: initialThree,
			workload:          grantedExecutors(2),
			running:           2,
			want:              true,
		},
		"not ready yet: fewer than the granted executors": {
			dynamicAllocation: initialThree,
			workload:          grantedExecutors(3),
			running:           2,
			want:              false,
		},
		"not ready yet: granted more than the initial executors": {
			dynamicAllocation: initialThree,
			workload:          grantedExecutors(4),
			running:           3,
			want:              true,
		},
		"started: minExecutors is the floor": {
			dynamicAllocation: initialThree,
			started:           "sub-1",
			running:           1,
			want:              true,
		},
		"started: below minExecutors": {
			dynamicAllocation: initialThree,
			started:           "sub-1",
			want:              false,
		},
		"started in a previous submission: the initial executors": {
			dynamicAllocation: initialThree,
			started:           "sub-0",
			running:           1,
			want:              false,
		},
		"without minExecutors, started: the driver running is enough": {
			dynamicAllocation: &sparkappv1beta2.DynamicAllocation{Enabled: true},
			started:           "sub-1",
			want:              true,
		},
		"not elastic: minExecutors": {
			dynamicAllocation: initialThree,
			notElastic:        true,
			running:           1,
			want:              true,
		},
		"not elastic, without minExecutors: the executor instances": {
			dynamicAllocation: &sparkappv1beta2.DynamicAllocation{Enabled: true},
			notElastic:        true,
			running:           1,
			want:              false,
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			app := sparkapplicationtesting.MakeSparkApplication("sparkapp", "ns").
				ExecutorInstances(2).
				AppState(sparkappv1beta2.ApplicationStateRunning).
				Obj()
			if !tc.notElastic {
				app.Annotations = map[string]string{workloadslicing.EnabledAnnotationKey: workloadslicing.EnabledAnnotationValue}
			}
			if tc.started != "" {
				metav1.SetMetaDataAnnotation(&app.ObjectMeta, executorsStartedSubmissionAnnotation, tc.started)
			}
			app.Spec.DynamicAllocation = tc.dynamicAllocation
			app.Status.SubmissionID = "sub-1"
			builder := utiltesting.NewClientBuilder()
			for i := range tc.running {
				builder = builder.WithObjects(testingpod.MakePod(fmt.Sprintf("sparkapp-exec-%d", i+1), "ns").
					Label(sparkcommon.LabelSparkAppName, "sparkapp").
					Label(sparkcommon.LabelSparkRole, sparkcommon.SparkRoleExecutor).
					Label(sparkcommon.LabelSubmissionID, "sub-1").
					StatusPhase(corev1.PodRunning).
					StatusConditions(corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionTrue}).
					Obj())
			}
			if got := (*SparkApplication)(app).PodsReady(ctx, builder.Build(), jobframework.WithWorkload(tc.workload)); got != tc.want {
				t.Errorf("PodsReady() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPodSetsPartialPreemption(t *testing.T) {
	sparkApp := sparkapplicationtesting.MakeSparkApplication("sparkapp", "ns").ExecutorInstances(4)

	testCases := map[string]struct {
		sparkApp     *sparkappv1beta2.SparkApplication
		featureGates map[featuregate.Feature]bool
		want         *kueue.PodSetPartialPreemption
	}{
		"floor from the executor annotation": {
			sparkApp:     sparkApp.Clone().ExecutorAnnotation(constants.PodSetPartialPreemptionMinRetainedCountAnnotation, "2").Obj(),
			featureGates: map[featuregate.Feature]bool{features.PartialPreemption: true},
			want:         &kueue.PodSetPartialPreemption{MinRetainedCount: new(int32(2))},
		},
		"feature gate disabled": {
			sparkApp:     sparkApp.Clone().ExecutorAnnotation(constants.PodSetPartialPreemptionMinRetainedCountAnnotation, "2").Obj(),
			featureGates: map[featuregate.Feature]bool{features.PartialPreemption: false},
		},
		"no annotation": {
			sparkApp:     sparkApp.Clone().Obj(),
			featureGates: map[featuregate.Feature]bool{features.PartialPreemption: true},
		},
		"zero floor": {
			sparkApp:     sparkApp.Clone().ExecutorAnnotation(constants.PodSetPartialPreemptionMinRetainedCountAnnotation, "0").Obj(),
			featureGates: map[featuregate.Feature]bool{features.PartialPreemption: true},
			want:         &kueue.PodSetPartialPreemption{MinRetainedCount: new(int32(0))},
		},
		"malformed annotation": {
			sparkApp:     sparkApp.Clone().ExecutorAnnotation(constants.PodSetPartialPreemptionMinRetainedCountAnnotation, "x").Obj(),
			featureGates: map[featuregate.Feature]bool{features.PartialPreemption: true},
		},
		"negative annotation": {
			sparkApp:     sparkApp.Clone().ExecutorAnnotation(constants.PodSetPartialPreemptionMinRetainedCountAnnotation, "-1").Obj(),
			featureGates: map[featuregate.Feature]bool{features.PartialPreemption: true},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			ctx, _ := utiltesting.ContextWithLog(t)

			got, err := (*SparkApplication)(tc.sparkApp).PodSets(ctx, nil)
			if err != nil {
				t.Fatalf("PodSets() returned error: %v", err)
			}
			if got[driverPodSetIndex].PartialPreemption != nil {
				t.Errorf("driver PodSet PartialPreemption = %v, want nil", got[driverPodSetIndex].PartialPreemption)
			}
			if diff := cmp.Diff(tc.want, got[executorPodSetIndex].PartialPreemption); diff != "" {
				t.Errorf("executor PodSet PartialPreemption (-want +got):\n%s", diff)
			}
		})
	}
}

func TestPodsToReclaim(t *testing.T) {
	sparkApp := sparkapplicationtesting.MakeSparkApplication("sparkapp", "ns").SubmissionID("sub-1").Obj()
	created := time.Now().Truncate(time.Second)
	// executorPod(n) is created n seconds after the first executor.
	executorPod := func(n int) *testingpod.PodWrapper {
		return testingpod.MakePod(fmt.Sprintf("exec-%d", n), "ns").
			Label(sparkcommon.LabelSparkAppName, "sparkapp").
			Label(sparkcommon.LabelSparkRole, sparkcommon.SparkRoleExecutor).
			Label(sparkcommon.LabelSubmissionID, "sub-1").
			CreationTimestamp(created.Add(time.Duration(n) * time.Second)).
			StatusPhase(corev1.PodRunning)
	}
	driverPod := testingpod.MakePod("driver", "ns").
		Label(sparkcommon.LabelSparkAppName, "sparkapp").
		Label(sparkcommon.LabelSparkRole, sparkcommon.SparkRoleDriver).
		Label(sparkcommon.LabelSubmissionID, "sub-1").
		StatusPhase(corev1.PodRunning)

	testCases := map[string]struct {
		pods      []*corev1.Pod
		targets   map[kueue.PodSetReference]int32
		wantNames []string
	}{
		"evicts the youngest executors above the target, never the driver": {
			pods: []*corev1.Pod{
				driverPod.Obj(),
				executorPod(3).Obj(),
				executorPod(1).Obj(),
				executorPod(10).Obj(),
				executorPod(2).Obj(),
			},
			targets:   map[kueue.PodSetReference]int32{executorPodSetName: 2},
			wantNames: []string{"exec-3", "exec-10"},
		},
		"skips gated, terminating and terminated executors": {
			pods: []*corev1.Pod{
				executorPod(1).Obj(),
				executorPod(2).Obj(),
				executorPod(3).Gate(kueue.ElasticJobSchedulingGate).Obj(),
				executorPod(4).Finalizer("test").DeletionTimestamp(time.Now()).Obj(),
				executorPod(5).StatusPhase(corev1.PodFailed).Obj(),
			},
			targets:   map[kueue.PodSetReference]int32{executorPodSetName: 1},
			wantNames: []string{"exec-2"},
		},
		"skips executors of a previous submission": {
			pods: []*corev1.Pod{
				executorPod(1).Obj(),
				executorPod(2).Label(sparkcommon.LabelSubmissionID, "sub-0").Obj(),
			},
			targets: map[kueue.PodSetReference]int32{executorPodSetName: 1},
		},
		"nothing to reclaim at the target": {
			pods:    []*corev1.Pod{executorPod(1).Obj(), executorPod(2).Obj()},
			targets: map[kueue.PodSetReference]int32{executorPodSetName: 2},
		},
		"no executor target": {
			pods:    []*corev1.Pod{driverPod.Obj(), executorPod(1).Obj()},
			targets: map[kueue.PodSetReference]int32{driverPodSetName: 0},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			builder := utiltesting.NewClientBuilder()
			for _, pod := range tc.pods {
				builder = builder.WithObjects(pod)
			}

			got, err := (*SparkApplication)(sparkApp).PodsToReclaim(ctx, builder.Build(), nil, tc.targets)
			if err != nil {
				t.Fatalf("PodsToReclaim() returned error: %v", err)
			}
			var gotNames []string
			for _, pod := range got {
				gotNames = append(gotNames, pod.Name)
			}
			if diff := cmp.Diff(tc.wantNames, gotNames); diff != "" {
				t.Errorf("reclaimed pods (-want +got):\n%s", diff)
			}
		})
	}
}

func TestPodsReadyPartialPreemption(t *testing.T) {
	// 4 executors are expected (instances, or minExecutors with dynamic allocation),
	// 2 run after a shrink.
	makeSparkApp := func(dynamicAllocation bool) *sparkappv1beta2.SparkApplication {
		app := sparkapplicationtesting.MakeSparkApplication("sparkapp", "ns").
			Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			ExecutorInstances(4).
			AppState(sparkappv1beta2.ApplicationStateRunning).
			SubmissionID("sub-1")
		if dynamicAllocation {
			app.DynamicAllocation(&sparkappv1beta2.DynamicAllocation{Enabled: true, MinExecutors: new(int32(4))})
		}
		return app.Obj()
	}
	readyExecutor := func(name string) *corev1.Pod {
		return testingpod.MakePod(name, "ns").
			Label(sparkcommon.LabelSparkAppName, "sparkapp").
			Label(sparkcommon.LabelSparkRole, sparkcommon.SparkRoleExecutor).
			Label(sparkcommon.LabelSubmissionID, "sub-1").
			StatusPhase(corev1.PodRunning).
			StatusConditions(corev1.PodCondition{Type: corev1.PodReady, Status: corev1.ConditionTrue}).
			Obj()
	}
	admittedWorkload := func(granted int32, target *int32) *kueue.Workload {
		psa := utiltestingapi.MakePodSetAssignment(executorPodSetName).Count(granted).Obj()
		if target != nil {
			psa.PartialPreemption = &kueue.PodSetPartialPreemptionStatus{RetainedCount: target}
		}
		return utiltestingapi.MakeWorkload("wl", "ns").
			PodSets(
				*utiltestingapi.MakePodSet(driverPodSetName, 1).Obj(),
				*utiltestingapi.MakePodSet(executorPodSetName, 4).Obj(),
			).
			ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").PodSets(
				utiltestingapi.MakePodSetAssignment(driverPodSetName).Count(1).Obj(), psa,
			).Obj(), time.Now()).
			AdmittedAt(true, time.Now()).
			Obj()
	}

	testCases := map[string]struct {
		withoutDynamicAllocation bool
		workload                 *kueue.Workload
		featureGates             map[featuregate.Feature]bool
		want                     bool
	}{
		"shrinking to the target below minExecutors": {
			workload:     admittedWorkload(4, new(int32(2))),
			featureGates: map[featuregate.Feature]bool{features.PartialPreemption: true},
			want:         true,
		},
		"shrinking to the target below minExecutors, feature gate disabled": {
			workload:     admittedWorkload(4, new(int32(2))),
			featureGates: map[featuregate.Feature]bool{features.PartialPreemption: false},
			want:         false,
		},
		"shrinking to the target below minExecutors, not elastic": {
			workload: admittedWorkload(4, new(int32(2))),
			featureGates: map[featuregate.Feature]bool{
				features.PartialPreemption:            true,
				features.ElasticJobsViaWorkloadSlices: false,
			},
			want: false,
		},
		"fewer ready executors than the target": {
			workload:     admittedWorkload(4, new(int32(3))),
			featureGates: map[featuregate.Feature]bool{features.PartialPreemption: true},
			want:         false,
		},
		"quota released down to the target below minExecutors": {
			workload:     admittedWorkload(2, new(int32(2))),
			featureGates: map[featuregate.Feature]bool{features.PartialPreemption: true},
			want:         true,
		},
		"shrinking to the target without dynamic allocation": {
			withoutDynamicAllocation: true,
			workload:                 admittedWorkload(4, new(int32(2))),
			featureGates:             map[featuregate.Feature]bool{features.PartialPreemption: true},
			want:                     true,
		},
		"admitted at minExecutors": {
			workload: admittedWorkload(4, nil),
			want:     false,
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlicesForSparkApplication, true)
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			ctx, _ := utiltesting.ContextWithLog(t)

			c := utiltesting.NewClientBuilder().WithObjects(readyExecutor("exec-1"), readyExecutor("exec-2")).Build()

			got := (*SparkApplication)(makeSparkApp(!tc.withoutDynamicAllocation)).PodsReady(ctx, c, jobframework.WithWorkload(tc.workload))
			if got != tc.want {
				t.Errorf("PodsReady() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestFailedExecutorIDsAcrossResubmission applies GetCustomAnnotations the way
// the job reconciler does (it only adds or updates annotations) over two
// reconciles, to check that failed executor IDs recorded for a previous
// submission do not count as failures of the new one.
func TestFailedExecutorIDsAcrossResubmission(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlicesForSparkApplication, true)
	ctx, _ := utiltesting.ContextWithLog(t)
	app := sparkapplicationtesting.MakeSparkApplication("sparkapp", "ns").
		Queue("local-queue").
		Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
		SubmissionID("sub-2").
		DynamicAllocation(&sparkappv1beta2.DynamicAllocation{Enabled: true, InitialExecutors: new(int32(4)), MaxExecutors: new(int32(10))}).
		AppState(sparkappv1beta2.ApplicationStateRunning).
		Annotation(maxExecutorIDAnnotation, "3").
		Annotation(maxExecutorIDSubmissionAnnotation, "sub-1").
		Annotation(failedExecutorIDsAnnotation, "3").
		Obj()
	executorPod := testingpod.MakePod("", "ns").
		Label(sparkcommon.LabelSparkAppName, "sparkapp").
		Label(sparkcommon.LabelSparkRole, sparkcommon.SparkRoleExecutor).
		Label(sparkcommon.LabelSubmissionID, "sub-2")
	var pods []corev1.Pod
	for _, id := range []string{"1", "2", "4"} {
		pods = append(pods, *executorPod.Clone().Name("exec-"+id).Label(sparkcommon.LabelSparkExecutorID, id).StatusPhase(corev1.PodRunning).Obj())
	}
	cl := utiltesting.NewClientBuilder().WithLists(&corev1.PodList{Items: pods}).Build()

	for reconcile := 1; reconcile <= 2; reconcile++ {
		got, err := (*SparkApplication)(app).GetCustomAnnotations(ctx, cl)
		if err != nil {
			t.Fatalf("reconcile %d: GetCustomAnnotations() returned error: %v", reconcile, err)
		}
		merged := maps.Clone(app.Annotations)
		maps.Copy(merged, got)
		app.Annotations = merged
	}
	if got := app.Annotations[failedExecutorIDsAnnotation]; got != "" {
		t.Errorf("failed executor IDs of the previous submission were kept: %q", got)
	}
	// The driver created executors 1, 2 and 4 of 4 starting executors, so
	// the count follows the 3 running pods.
	count, err := (*SparkApplication)(app).numExecutors(ctx, cl)
	if err != nil {
		t.Fatalf("numExecutors() returned error: %v", err)
	}
	if count != 3 {
		t.Errorf("numExecutors() = %d, want 3", count)
	}
}
