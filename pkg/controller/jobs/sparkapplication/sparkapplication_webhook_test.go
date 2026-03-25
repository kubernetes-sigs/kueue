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
	"testing"

	"github.com/google/go-cmp/cmp"
	sparkappv1beta2 "github.com/kubeflow/spark-operator/v2/api/v1beta2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/component-base/featuregate"
	"k8s.io/utils/ptr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	sparkapplicationtesting "sigs.k8s.io/kueue/pkg/util/testingjobs/sparkapplication"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

func TestValidateCreate(t *testing.T) {
	testSparkApp := sparkapplicationtesting.MakeSparkApplication("test-sparkapp", "test").Suspend(false)
	testcases := map[string]struct {
		sparkApp     *sparkappv1beta2.SparkApplication
		featureGates map[featuregate.Feature]bool
		wantErr      error
	}{
		"base": {
			sparkApp: testSparkApp.Clone().Queue("local-queue").Obj(),
			wantErr:  nil,
		},
		"dynamicAllocation without elastic job feature": {
			sparkApp: testSparkApp.Clone().Queue("local-queue").DynamicAllocation(&sparkappv1beta2.DynamicAllocation{
				Enabled:          true,
				MinExecutors:     ptr.To(int32(1)),
				InitialExecutors: ptr.To(int32(2)),
				MaxExecutors:     ptr.To(int32(3)),
			}).Obj(),
			wantErr: field.ErrorList{field.Invalid(
				dynamicAllocationEnabledPath,
				true,
				"a kueue managed job can use dynamicAllocation only when the ElasticJobsViaWorkloadSlices feature gate is on and the job is an elastic job",
			)}.ToAggregate(),
		},
		"dynamicAllocation with elastic job feature": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			sparkApp: testSparkApp.Clone().Queue("local-queue").Annotation(
				workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue,
			).DynamicAllocation(&sparkappv1beta2.DynamicAllocation{
				Enabled:          true,
				MinExecutors:     ptr.To(int32(1)),
				InitialExecutors: ptr.To(int32(2)),
				MaxExecutors:     ptr.To(int32(3)),
			}).Obj(),
			wantErr: field.ErrorList{field.Invalid(
				elasticJobEnabledPath,
				workloadslicing.EnabledAnnotationValue,
				"elastic job is not supported in SparkApplication",
			)}.ToAggregate(),
		},
		"base with TAS": {
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: true},
			sparkApp: testSparkApp.Clone().Queue("local-queue").ExecutorAnnotation(
				kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block",
			).Obj(),
			wantErr: nil,
		},
		"invalid TAS configuration": {
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: true},
			sparkApp: testSparkApp.Clone().Queue("local-queue").ExecutorAnnotation(
				kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block",
			).ExecutorAnnotation(
				kueue.PodSetPreferredTopologyAnnotation, "cloud.com/block",
			).Obj(),
			wantErr: field.ErrorList{field.Invalid(
				executorSpecPath.Child("annotations"),
				field.OmitValueType{},
				`must not contain more than one topology annotation: ["kueue.x-k8s.io/podset-required-topology", "kueue.x-k8s.io/podset-preferred-topology", "kueue.x-k8s.io/podset-unconstrained-topology"]`,
			)}.ToAggregate(),
		},
	}

	for name, tc := range testcases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)

			webhook := &SparkApplicationWebhook{}
			ctx, _ := utiltesting.ContextWithLog(t)
			_, gotErr := webhook.ValidateCreate(ctx, tc.sparkApp)

			if diff := cmp.Diff(tc.wantErr, gotErr); diff != "" {
				t.Errorf("validateCreate() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestDefault(t *testing.T) {
	testManagedNamespace := utiltesting.MakeNamespaceWrapper("ns").Label(corev1.LabelMetadataName, "ns")
	testUnManagedNamespace := utiltesting.MakeNamespaceWrapper("ns-unmanaged").Label(corev1.LabelMetadataName, "ns-unmanaged")
	testSparkApp := sparkapplicationtesting.MakeSparkApplication("test-sparkapp", testManagedNamespace.Name).Suspend(false)
	testClusterQueue := utiltestingapi.MakeClusterQueue("cluster-queue")
	testLocalQueue := utiltestingapi.MakeLocalQueue("local-queue", testManagedNamespace.Name).ClusterQueue(testClusterQueue.Name)

	testCases := map[string]struct {
		sparkApp                      *sparkappv1beta2.SparkApplication
		defaultQueue                  *kueue.LocalQueue
		managedJobsNamespacesSelector labels.Selector
		manageJobsWithoutQueueName    bool
		withDefaultLocalQueue         bool
		wantSparkApp                  *sparkappv1beta2.SparkApplication
		wantErr                       error
	}{
		"should not suspend a SparkApplication in unmanaged namespace": {
			managedJobsNamespacesSelector: labels.SelectorFromSet(labels.Set{corev1.LabelMetadataName: testManagedNamespace.Name}),
			sparkApp:                      sparkapplicationtesting.MakeSparkApplication("test-sparkapp", testUnManagedNamespace.Name).Suspend(false).Obj(),
			wantSparkApp:                  sparkapplicationtesting.MakeSparkApplication("test-sparkapp", testUnManagedNamespace.Name).Suspend(false).Obj(),
		},
		"should suspend a SparkApplication": {
			sparkApp: testSparkApp.Clone().Queue(testLocalQueue.Name).Obj(),
			wantSparkApp: testSparkApp.Clone().Queue(testLocalQueue.Name).
				Suspend(true).
				Obj(),
		},
		"should not suspend a SparkApplication without a queue label if manageJobsWithoutQueueName is not enabled": {
			sparkApp:     testSparkApp.Clone().Obj(),
			wantSparkApp: testSparkApp.Clone().Obj(),
		},
		"should suspend a SparkApplication without a queue label if manageJobsWithoutQueueName is enabled": {
			sparkApp: testSparkApp.Clone().Obj(),
			wantSparkApp: testSparkApp.Clone().
				Suspend(true).
				Obj(),
			manageJobsWithoutQueueName: true,
		},
		"should set the default local queue if enabled and the user didn't specify any": {
			sparkApp: testSparkApp.Clone().Obj(),
			wantSparkApp: testSparkApp.Clone().
				Suspend(true).
				Queue(string(controllerconstants.DefaultLocalQueueName)).
				Obj(),
			withDefaultLocalQueue: true,
		},
		"should not set the default local queue if doesn't exists": {
			sparkApp:              testSparkApp.Clone().Obj(),
			wantSparkApp:          testSparkApp.Clone().Obj(),
			withDefaultLocalQueue: false,
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)

			kClient := utiltesting.NewClientBuilder().WithObjects(testManagedNamespace.Obj()).Build()
			cqCache := schdcache.New(kClient)
			queueManager := qcache.NewManagerForUnitTests(kClient, cqCache)

			cq := testClusterQueue.Clone()

			if err := cqCache.AddClusterQueue(ctx, cq.Obj()); err != nil {
				t.Fatalf("Inserting clusterQueue %s in cache: %v", cq.Name, err)
			}

			if err := queueManager.AddLocalQueue(ctx, testLocalQueue.Obj()); err != nil {
				t.Fatalf("Inserting queue %s/%s in manager: %v", testLocalQueue.Namespace, testLocalQueue.Name, err)
			}

			if tc.withDefaultLocalQueue {
				if err := queueManager.AddLocalQueue(ctx, utiltestingapi.MakeLocalQueue("default", testManagedNamespace.Name).
					ClusterQueue(cq.Name).Obj()); err != nil {
					t.Fatalf("failed to create default local queue: %s", err)
				}
			}

			webhook := &SparkApplicationWebhook{
				client:                       kClient,
				manageJobsWithoutQueueName:   tc.manageJobsWithoutQueueName,
				managedJobsNamespaceSelector: tc.managedJobsNamespacesSelector,
				queues:                       queueManager,
				cache:                        cqCache,
			}

			err := webhook.Default(ctx, tc.sparkApp)
			if err != nil {
				t.Errorf("Default() errored:%v", err)
			}
			if diff := cmp.Diff(tc.wantSparkApp, tc.sparkApp); diff != "" {
				t.Errorf("Default() mismatch (-want,+got):\n%s", diff)
			}
		})
	}
}
