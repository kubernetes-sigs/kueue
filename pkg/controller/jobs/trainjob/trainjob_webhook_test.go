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
	kftrainerapi "github.com/kubeflow/trainer/v2/pkg/apis/trainer/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingtrainjob "sigs.k8s.io/kueue/pkg/util/testingjobs/trainjob"
)

const (
	invalidRFC1123Message = `a lowercase RFC 1123 subdomain must consist of lower case alphanumeric characters, '-' or '.', and must start and end with an alphanumeric character (e.g. 'example.com', regex used for validation is '[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*')`
)

var (
	labelsPath         = field.NewPath("metadata", "labels")
	queueNameLabelPath = labelsPath.Key(controllerconstants.QueueLabel)
)

func TestValidateCreate(t *testing.T) {
	testTrainJob := testingtrainjob.MakeTrainJob("trainjob", "ns").Suspend(false)
	testcases := map[string]struct {
		name     string
		trainJob *kftrainerapi.TrainJob
		wantErr  error
	}{
		"base": {
			trainJob: testTrainJob.Clone().Queue("local-queue").Obj(),
			wantErr:  nil,
		},
		"invalid queue-name label": {
			trainJob: testTrainJob.Clone().Queue("queue_name").Obj(),
			wantErr:  field.ErrorList{field.Invalid(queueNameLabelPath, "queue_name", invalidRFC1123Message)}.ToAggregate(),
		},
		"with prebuilt workload": {
			trainJob: testTrainJob.Clone().Queue("local-queue").Label(controllerconstants.PrebuiltWorkloadLabel, "prebuilt-workload").Obj(),
			wantErr:  nil,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			webhook := &TrainJobWebhook{}
			_, gotErr := webhook.ValidateCreate(t.Context(), tc.trainJob)

			if diff := cmp.Diff(tc.wantErr, gotErr); diff != "" {
				t.Errorf("validateCreate() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestDefault(t *testing.T) {
	testNamespace := utiltesting.MakeNamespaceWrapper("ns").Label(corev1.LabelMetadataName, "ns")
	testTrainJob := testingtrainjob.MakeTrainJob("trainjob", testNamespace.Name).Suspend(false)
	testClusterQueue := utiltesting.MakeClusterQueue("cluster-queue")
	testLocalQueue := utiltesting.MakeLocalQueue("local-queue", testNamespace.Name).ClusterQueue(testClusterQueue.Name)
	testCases := map[string]struct {
		trainJob                     *kftrainerapi.TrainJob
		defaultQueue                 *kueue.LocalQueue
		localQueueDefaultingEnabled  bool
		manageJobsWithoutQueueName   bool
		withMultiKueueAdmissionCheck bool
		withDefaultLocalQueue        bool
		multiKueueEnabled            bool
		wantTrainJob                 *kftrainerapi.TrainJob
		wantErr                      error
	}{
		"should suspend a TrainJob with a queue label": {
			trainJob: testTrainJob.Clone().Queue(testLocalQueue.Name).Obj(),
			wantTrainJob: testTrainJob.Clone().Queue(testLocalQueue.Name).
				Suspend(true).
				JobSetLabel(controllerconstants.QueueLabel, testLocalQueue.Name).
				Obj(),
		},
		"should not suspend a TrainJob without a queue label if manageJobsWithoutQueueName is not enabled": {
			trainJob:     testTrainJob.Clone().Obj(),
			wantTrainJob: testTrainJob.Clone().Obj(),
		},
		"should suspend a TrainJob without a queue label if manageJobsWithoutQueueName is enabled": {
			trainJob: testTrainJob.Clone().Obj(),
			wantTrainJob: testTrainJob.Clone().
				Suspend(true).
				Obj(),
			manageJobsWithoutQueueName: true,
		},
		"should set the default local queue if enabled and the user didn't specify any": {
			trainJob: testTrainJob.Clone().Obj(),
			wantTrainJob: testTrainJob.Clone().
				Suspend(true).
				Queue(string(controllerconstants.DefaultLocalQueueName)).
				JobSetLabel(controllerconstants.QueueLabel, string(controllerconstants.DefaultLocalQueueName)).
				Obj(),
			localQueueDefaultingEnabled: true,
			withDefaultLocalQueue:       true,
		},
		"should not set the default local queue if doesn't exists": {
			trainJob:                    testTrainJob.Clone().Obj(),
			wantTrainJob:                testTrainJob.Clone().Obj(),
			localQueueDefaultingEnabled: true,
			withDefaultLocalQueue:       false,
		},
		"should set managedBy to multiKueue if the user didn't specify any": {
			trainJob: testTrainJob.Clone().Queue(testLocalQueue.Name).Obj(),
			wantTrainJob: testTrainJob.Clone().Queue(testLocalQueue.Name).
				Suspend(true).
				JobSetLabel(controllerconstants.QueueLabel, testLocalQueue.Name).
				ManagedBy(kueue.MultiKueueControllerName).
				Obj(),
			multiKueueEnabled:            true,
			withMultiKueueAdmissionCheck: true,
		},
		"should not set managedBy to multiKueue if already specified by the user": {
			trainJob: testTrainJob.Clone().Queue(testLocalQueue.Name).ManagedBy("user").Obj(),
			wantTrainJob: testTrainJob.Clone().Queue(testLocalQueue.Name).
				Suspend(true).
				JobSetLabel(controllerconstants.QueueLabel, testLocalQueue.Name).
				ManagedBy("user").
				Obj(),
			multiKueueEnabled:            true,
			withMultiKueueAdmissionCheck: true,
		},
		"should not set managedBy to multiKueue if the selected clusterQueue doesn't have the corresponding admissionCheck": {
			trainJob: testTrainJob.Clone().Queue(testLocalQueue.Name).Obj(),
			wantTrainJob: testTrainJob.Clone().Queue(testLocalQueue.Name).
				Suspend(true).
				JobSetLabel(controllerconstants.QueueLabel, testLocalQueue.Name).
				Obj(),
			multiKueueEnabled:            true,
			withMultiKueueAdmissionCheck: false,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.MultiKueue, tc.multiKueueEnabled)
			features.SetFeatureGateDuringTest(t, features.LocalQueueDefaulting, tc.localQueueDefaultingEnabled)

			ctx, log := utiltesting.ContextWithLog(t)

			kClient := utiltesting.NewClientBuilder().WithObjects(testNamespace.Obj()).Build()
			cqCache := schdcache.New(kClient)
			queueManager := qcache.NewManager(kClient, cqCache)

			cq := testClusterQueue.Clone()
			if tc.withMultiKueueAdmissionCheck {
				admissionCheck := utiltesting.MakeAdmissionCheck("admission-check").
					ControllerName(kueue.MultiKueueControllerName).
					Active(metav1.ConditionTrue).
					Obj()
				cqCache.AddOrUpdateAdmissionCheck(log, admissionCheck)
				cq.AdmissionChecks("admission-check")
			}

			if err := cqCache.AddClusterQueue(ctx, cq.Obj()); err != nil {
				t.Fatalf("Inserting clusterQueue %s in cache: %v", cq.Name, err)
			}

			if err := queueManager.AddLocalQueue(ctx, testLocalQueue.Obj()); err != nil {
				t.Fatalf("Inserting queue %s/%s in manager: %v", testLocalQueue.Namespace, testLocalQueue.Name, err)
			}

			if tc.withDefaultLocalQueue {
				if err := queueManager.AddLocalQueue(ctx, utiltesting.MakeLocalQueue("default", testNamespace.Name).
					ClusterQueue(cq.Name).Obj()); err != nil {
					t.Fatalf("failed to create default local queue: %s", err)
				}
			}

			webhook := &TrainJobWebhook{
				manageJobsWithoutQueueName: tc.manageJobsWithoutQueueName,
				queues:                     queueManager,
				cache:                      cqCache,
			}

			err := webhook.Default(ctx, tc.trainJob)
			if err != nil {
				t.Errorf("Default() errored:%v", err)
			}
			if diff := cmp.Diff(tc.wantTrainJob, tc.trainJob); diff != "" {
				t.Errorf("Default() mismatch (-want,+got):\n%s", diff)
			}
		})
	}
}
