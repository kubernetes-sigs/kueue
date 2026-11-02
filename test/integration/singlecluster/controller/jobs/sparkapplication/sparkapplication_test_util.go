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
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	sparkv1beta2 "github.com/kubeflow/spark-operator/v2/api/v1beta2"
	sparkcommon "github.com/kubeflow/spark-operator/v2/pkg/common"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/component-base/featuregate"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	workloadsparkapplication "sigs.k8s.io/kueue/pkg/controller/jobs/sparkapplication"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/integration"
)

const (
	instanceKey                       = "cloud.provider.com/instance"
	jobQueueName kueue.LocalQueueName = "test-queue"
	submissionID                      = "submission-1"
)

// executorPodPhases maps the executor states the Spark Operator reports to the phases of
// their pods.
var executorPodPhases = map[sparkv1beta2.ExecutorState]corev1.PodPhase{
	sparkv1beta2.ExecutorStatePending:   corev1.PodPending,
	sparkv1beta2.ExecutorStateRunning:   corev1.PodRunning,
	sparkv1beta2.ExecutorStateCompleted: corev1.PodSucceeded,
	sparkv1beta2.ExecutorStateFailed:    corev1.PodFailed,
}

type PodsReadyTestSpec struct {
	FeatureGates    map[featuregate.Feature]bool
	PodsScheduled   *metav1.Condition
	BeforeAppState  *sparkv1beta2.ApplicationStateType
	BeforeCondition *metav1.Condition
	AppState        sparkv1beta2.ApplicationStateType
	Suspended       bool
	WantCondition   *metav1.Condition
}

func shouldReconcileSparkApplication(ctx context.Context, k8sClient client.Client, sparkApp *sparkv1beta2.SparkApplication) {
	ginkgo.By("checking the job gets suspended when created unsuspended")
	behavioral.MustCreate(ctx, k8sClient, sparkApp)
	lookupKey := client.ObjectKeyFromObject(sparkApp)
	createdSparkApplication := &sparkv1beta2.SparkApplication{}

	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, lookupKey, createdSparkApplication)).Should(gomega.Succeed())
		g.Expect(ptr.Deref(createdSparkApplication.Spec.Suspend, false)).Should(gomega.BeTrue())
	}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

	wlLookupKey := types.NamespacedName{
		Name:      workloadsparkapplication.GetWorkloadNameForSparkApplication(sparkApp.Name, sparkApp.UID),
		Namespace: sparkApp.Namespace,
	}

	ginkgo.By("checking the workload is created without queue assigned")
	createdWorkload := behavioral.AwaitAndVerifyCreatedWorkload(ctx, k8sClient, wlLookupKey, createdSparkApplication)
	gomega.Expect(createdWorkload.Spec.QueueName).Should(gomega.Equal(kueue.LocalQueueName("")), "The Workload shouldn't have .spec.queueName set")

	ginkgo.By("checking the workload is updated with queue name when the job does")
	createdSparkApplication.Labels = map[string]string{constants.QueueLabel: string(jobQueueName)}
	gomega.Expect(k8sClient.Update(ctx, createdSparkApplication)).Should(gomega.Succeed())
	behavioral.AwaitAndVerifyWorkloadQueueName(ctx, k8sClient, createdWorkload, wlLookupKey, jobQueueName)

	ginkgo.By("checking the job is unsuspended when workload is assigned")
	onDemandFlavor := utiltestingapi.MakeResourceFlavor("on-demand").NodeLabel(instanceKey, "on-demand").Obj()
	behavioral.MustCreate(ctx, k8sClient, onDemandFlavor)
	spotFlavor := utiltestingapi.MakeResourceFlavor("spot").NodeLabel(instanceKey, "spot").Obj()
	behavioral.MustCreate(ctx, k8sClient, spotFlavor)
	defer func() {
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, spotFlavor, true)
	}()

	clusterQueue := utiltestingapi.MakeClusterQueue("cluster-queue").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "5").Obj(),
			*utiltestingapi.MakeFlavorQuotas("spot").Resource(corev1.ResourceCPU, "5").Obj(),
		).Obj()
	admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(clusterQueue.Name)).
		PodSets(
			kueue.PodSetAssignment{
				Name: kueue.NewPodSetReference("driver"),
				Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
					corev1.ResourceCPU: "on-demand",
				},
			},
			kueue.PodSetAssignment{
				Name: kueue.NewPodSetReference("executor"),
				Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
					corev1.ResourceCPU: "spot",
				},
			},
		).Obj()

	integration.SetQuotaReservation(ctx, k8sClient, wlLookupKey, admission)
	integration.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, lookupKey, createdSparkApplication)).Should(gomega.Succeed())
		g.Expect(ptr.Deref(createdSparkApplication.Spec.Suspend, true)).Should(gomega.BeFalse())
	}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

	gomega.Eventually(func(g gomega.Gomega) {
		ok, _ := utiltesting.CheckEventRecordedFor(ctx, k8sClient, "Started", corev1.EventTypeNormal, fmt.Sprintf("Admitted by clusterQueue %v", clusterQueue.Name), lookupKey)
		g.Expect(ok).Should(gomega.BeTrue())
	}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

	gomega.Expect(createdSparkApplication.Spec.Driver.NodeSelector).To(gomega.BeComparableTo(map[string]string{instanceKey: "on-demand"}))
	gomega.Expect(createdSparkApplication.Spec.Executor.NodeSelector).To(gomega.BeComparableTo(map[string]string{instanceKey: "spot"}))
}

func shouldNotReconcileUnmanagedSparkApplication(ctx context.Context, k8sClient client.Client, sparkApp *sparkv1beta2.SparkApplication) {
	ginkgo.By("checking the job remains unsuspended and no workload is created in unmanaged namespace")
	behavioral.MustCreate(ctx, k8sClient, sparkApp)

	lookupKey := client.ObjectKeyFromObject(sparkApp)
	wlLookupKey := types.NamespacedName{
		Name:      workloadsparkapplication.GetWorkloadNameForSparkApplication(sparkApp.Name, sparkApp.UID),
		Namespace: sparkApp.Namespace,
	}
	createdSparkApplication := &sparkv1beta2.SparkApplication{}
	workload := &kueue.Workload{}
	gomega.Consistently(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, lookupKey, createdSparkApplication)).Should(gomega.Succeed())
		g.Expect(ptr.Deref(createdSparkApplication.Spec.Suspend, false)).Should(gomega.BeFalse())
		g.Expect(k8sClient.Get(ctx, wlLookupKey, workload)).Should(utiltesting.BeNotFoundError())
	}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
}

func waitForPodsReadyEnabledForSparkApplication(ctx context.Context, k8sClient client.Client, sparkApp, createdSparkApplication *sparkv1beta2.SparkApplication, podsReadyTestSpec PodsReadyTestSpec) {
	features.SetFeatureGatesDuringTest(ginkgo.GinkgoTB(), podsReadyTestSpec.FeatureGates)
	ginkgo.By("Create a SparkApplication")
	sparkApp.Labels = map[string]string{constants.QueueLabel: string(jobQueueName)}
	behavioral.MustCreate(ctx, k8sClient, sparkApp)
	lookupKey := client.ObjectKeyFromObject(sparkApp)
	gomega.ExpectWithOffset(1, k8sClient.Get(ctx, lookupKey, createdSparkApplication)).Should(gomega.Succeed())

	wlLookupKey := types.NamespacedName{
		Name:      workloadsparkapplication.GetWorkloadNameForSparkApplication(sparkApp.Name, sparkApp.UID),
		Namespace: sparkApp.Namespace,
	}

	ginkgo.By("Fetch the workload created for the SparkApplication")
	createdWorkload := &kueue.Workload{}
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
	}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

	ginkgo.By("Admit the workload created for the SparkApplication")
	admission := utiltestingapi.MakeAdmission("foo").PodSets(
		kueue.PodSetAssignment{
			Name: kueue.NewPodSetReference("driver"),
			Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
				corev1.ResourceCPU: "default",
			},
			Count: new(createdWorkload.Spec.PodSets[0].Count),
		},
		kueue.PodSetAssignment{
			Name: kueue.NewPodSetReference("executor"),
			Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
				corev1.ResourceCPU: "default",
			},
			Count: new(createdWorkload.Spec.PodSets[1].Count),
		},
	).Obj()
	integration.SetQuotaReservation(ctx, k8sClient, wlLookupKey, admission)
	integration.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)

	ginkgo.By("Await for the SparkApplication to be unsuspended")
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, lookupKey, createdSparkApplication)).Should(gomega.Succeed())
		g.Expect(ptr.Deref(createdSparkApplication.Spec.Suspend, true)).Should(gomega.BeFalse())
	}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

	if podsReadyTestSpec.PodsScheduled != nil {
		integration.SetPodsScheduledCondition(ctx, k8sClient, wlLookupKey, *podsReadyTestSpec.PodsScheduled)
	}

	if podsReadyTestSpec.BeforeAppState != nil {
		ginkgo.By("Update the SparkApplication status to simulate initial progress")
		createdSparkApplication.Status.AppState.State = *podsReadyTestSpec.BeforeAppState
		createdSparkApplication.Status.ExecutorState = executorStatesForAppState(createdSparkApplication, *podsReadyTestSpec.BeforeAppState)
		gomega.ExpectWithOffset(1, k8sClient.Status().Update(ctx, createdSparkApplication)).Should(gomega.Succeed())
		gomega.ExpectWithOffset(1, k8sClient.Get(ctx, lookupKey, createdSparkApplication)).Should(gomega.Succeed())
	}

	if podsReadyTestSpec.BeforeCondition != nil {
		ginkgo.By("Verify pre-condition on Workload")
		gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
			g.Expect(apimeta.FindStatusCondition(createdWorkload.Status.Conditions, kueue.WorkloadPodsReady)).Should(
				gomega.BeComparableTo(
					podsReadyTestSpec.BeforeCondition,
					behavioral.IgnoreConditionTimestampsAndObservedGeneration,
				),
			)
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
	}

	ginkgo.By("Update the SparkApplication status to simulate progress")
	createdSparkApplication.Status.AppState.State = podsReadyTestSpec.AppState
	createdSparkApplication.Status.ExecutorState = executorStatesForAppState(createdSparkApplication, podsReadyTestSpec.AppState)
	gomega.ExpectWithOffset(1, k8sClient.Status().Update(ctx, createdSparkApplication)).Should(gomega.Succeed())
	gomega.ExpectWithOffset(1, k8sClient.Get(ctx, lookupKey, createdSparkApplication)).Should(gomega.Succeed())

	if podsReadyTestSpec.Suspended {
		ginkgo.By("Unset admission of the workload to suspend the SparkApplication")
		integration.SetQuotaReservation(ctx, k8sClient, wlLookupKey, nil)
		integration.SyncAdmittedConditionForWorkloads(ctx, k8sClient, createdWorkload)
	}

	ginkgo.By("Verify the PodsReady condition")
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
		g.Expect(apimeta.FindStatusCondition(createdWorkload.Status.Conditions, kueue.WorkloadPodsReady)).Should(
			gomega.BeComparableTo(
				podsReadyTestSpec.WantCondition,
				behavioral.IgnoreConditionTimestampsAndObservedGeneration,
			),
		)
	}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
}

// executorStatesForAppState mirrors what Spark Operator would publish in
// Status.ExecutorState given the SparkApplication's AppState. PodsReady
// requires len(running/completed executors) >= Spec.Executor.Instances, so
// tests that simulate AppState=Running must also simulate matching executor
// states; any other AppState clears the map.
func executorStatesForAppState(sparkApp *sparkv1beta2.SparkApplication, state sparkv1beta2.ApplicationStateType) map[string]sparkv1beta2.ExecutorState {
	if state != sparkv1beta2.ApplicationStateRunning {
		return nil
	}
	instances := ptr.Deref(sparkApp.Spec.Executor.Instances, 0)
	if instances <= 0 {
		return nil
	}
	states := make(map[string]sparkv1beta2.ExecutorState, instances)
	for i := int32(1); i <= instances; i++ {
		states[fmt.Sprintf("%s-exec-%d", sparkApp.Name, i)] = sparkv1beta2.ExecutorStateRunning
	}
	return states
}

// createExecutorPod emulates the Spark driver, which creates the executor pods with the Spark
// labels and from the executor labels, annotations and pod template. envtest runs no Spark driver.
// As in Spark, the name ends with the executor ID.
func createExecutorPod(ctx context.Context, k8sClient client.Client, sparkApp *sparkv1beta2.SparkApplication, name string) *corev1.Pod {
	ginkgo.GinkgoHelper()
	gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sparkApp), sparkApp)).Should(gomega.Succeed())
	executor := testingpod.MakePod(name, sparkApp.Namespace).
		Label(sparkcommon.LabelSparkAppName, sparkApp.Name).
		Label(sparkcommon.LabelSparkRole, sparkcommon.SparkRoleExecutor).
		Label(sparkcommon.LabelSubmissionID, submissionID).
		Label(sparkcommon.LabelSparkExecutorID, name[strings.LastIndex(name, "-")+1:]).
		Obj()
	maps.Copy(executor.Labels, sparkApp.Spec.Executor.Labels)
	executor.Annotations = maps.Clone(sparkApp.Spec.Executor.Annotations)
	executor.Spec.SchedulingGates = slices.Clone(sparkApp.Spec.Executor.Template.Spec.SchedulingGates)
	behavioral.MustCreate(ctx, k8sClient, executor)
	return executor
}

// setExecutorPodPhase emulates the kubelet, which moves the executor pod to the given phase.
// It creates the executor pod first if the driver has not created it yet.
func setExecutorPodPhase(ctx context.Context, k8sClient client.Client, sparkApp *sparkv1beta2.SparkApplication, name string, phase corev1.PodPhase) {
	ginkgo.GinkgoHelper()
	pod := &corev1.Pod{}
	if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: sparkApp.Namespace, Name: name}, pod); apierrors.IsNotFound(err) {
		pod = createExecutorPod(ctx, k8sClient, sparkApp, name)
	}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), pod)).Should(gomega.Succeed())
		pod.Status.Phase = phase
		g.Expect(k8sClient.Status().Update(ctx, pod)).Should(gomega.Succeed())
	}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
}
