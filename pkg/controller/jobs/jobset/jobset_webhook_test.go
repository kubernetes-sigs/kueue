/*
Copyright 2022 The Kubernetes Authors.

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
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	jobset "sigs.k8s.io/jobset/api/jobset/v1alpha2"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/controller/admissionchecks/multikueue"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/queue"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingutil "sigs.k8s.io/kueue/pkg/util/testingjobs/jobset"
)

const (
	invalidRFC1123Message = `a lowercase RFC 1123 subdomain must consist of lower case alphanumeric characters, '-' or '.', and must start and end with an alphanumeric character (e.g. 'example.com', regex used for validation is '[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*')`
)

var (
	labelsPath         = field.NewPath("metadata", "labels")
	queueNameLabelPath = labelsPath.Key(constants.QueueLabel)
)

func TestValidateCreate(t *testing.T) {
	testcases := []struct {
		name    string
		job     *jobset.JobSet
		wantErr error
	}{
		{
			name:    "simple",
			job:     testingutil.MakeJobSet("job", "default").Queue("queue").Obj(),
			wantErr: nil,
		},
		{
			name:    "invalid queue-name label",
			job:     testingutil.MakeJobSet("job", "default").Queue("queue_name").Obj(),
			wantErr: field.ErrorList{field.Invalid(queueNameLabelPath, "queue_name", invalidRFC1123Message)}.ToAggregate(),
		},
		{
			name:    "with prebuilt workload",
			job:     testingutil.MakeJobSet("job", "default").Queue("queue").Label(constants.PrebuiltWorkloadLabel, "prebuilt-workload").Obj(),
			wantErr: nil,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			jsw := &JobSetWebhook{}
			_, gotErr := jsw.ValidateCreate(context.Background(), tc.job)

			if diff := cmp.Diff(tc.wantErr, gotErr); diff != "" {
				t.Errorf("validateCreate() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestDefault(t *testing.T) {
	testCases := []struct {
		name              string
		jobSet            *jobset.JobSet
		queues            []kueue.LocalQueue
		clusterQueues     []kueue.ClusterQueue
		admissionCheck    *kueue.AdmissionCheck
		multiKueueEnabled bool
		wantManagedBy     *string
		wantErr           error
	}{
		{
			name: "TestDefault_JobSetManagedBy_jobsetapi.JobSetControllerName",
			jobSet: &jobset.JobSet{
				Spec: jobset.JobSetSpec{
					ManagedBy: ptr.To(jobset.JobSetControllerName),
				},
				ObjectMeta: ctrl.ObjectMeta{
					Labels: map[string]string{
						constants.QueueLabel: "local-queue",
					},
					Namespace: "default",
				},
			},
			queues: []kueue.LocalQueue{
				*utiltesting.MakeLocalQueue("local-queue", "default").
					ClusterQueue("cluster-queue").
					Obj(),
			},
			clusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("cluster-queue").
					AdmissionChecks("admission-check").
					Obj(),
			},
			admissionCheck: utiltesting.MakeAdmissionCheck("admission-check").
				ControllerName(multikueue.ControllerName).
				Active(metav1.ConditionTrue).
				Obj(),
			multiKueueEnabled: true,
			wantManagedBy:     ptr.To(multikueue.ControllerName),
		},
		{
			name: "TestDefault_WithQueueLabel",
			jobSet: &jobset.JobSet{
				ObjectMeta: ctrl.ObjectMeta{
					Labels: map[string]string{
						constants.QueueLabel: "local-queue",
					},
					Namespace: "default",
				},
			},
			queues: []kueue.LocalQueue{
				*utiltesting.MakeLocalQueue("local-queue", "default").
					ClusterQueue("cluster-queue").
					Obj(),
			},
			clusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("cluster-queue").
					AdmissionChecks("admission-check").
					Obj(),
			},
			admissionCheck: utiltesting.MakeAdmissionCheck("admission-check").
				ControllerName(multikueue.ControllerName).
				Active(metav1.ConditionTrue).
				Obj(),
			multiKueueEnabled: true,
			wantManagedBy:     ptr.To(multikueue.ControllerName),
		},
		{
			name: "TestDefault_WithoutQueueLabel",
			jobSet: &jobset.JobSet{
				ObjectMeta: ctrl.ObjectMeta{Namespace: "default"},
			},
			multiKueueEnabled: true,
			wantManagedBy:     nil,
		},
		{
			name: "TestDefault_InvalidQueueName",
			jobSet: &jobset.JobSet{
				ObjectMeta: ctrl.ObjectMeta{
					Labels:    map[string]string{constants.QueueLabel: "invalid-queue"},
					Namespace: "default",
				},
			},
			multiKueueEnabled: true,
			wantErr:           queue.ErrQueueDoesNotExist,
		},
		{
			name: "TestDefault_QueueNotFound",
			jobSet: &jobset.JobSet{
				ObjectMeta: ctrl.ObjectMeta{
					Labels: map[string]string{
						constants.QueueLabel: "non-existent-queue",
					},
					Namespace: "default",
				},
			},
			multiKueueEnabled: true,
			wantErr:           queue.ErrQueueDoesNotExist,
		},
		{
			name: "TestDefault_AdmissionCheckNotFound",
			jobSet: &jobset.JobSet{
				ObjectMeta: ctrl.ObjectMeta{
					Labels: map[string]string{
						constants.QueueLabel: "local-queue",
					},
					Namespace: "default",
				},
			},
			queues: []kueue.LocalQueue{
				*utiltesting.MakeLocalQueue("local-queue", "default").
					ClusterQueue("cluster-queue").
					Obj(),
			},
			clusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("cluster-queue").
					AdmissionChecks("non-existent-admission-check").
					Obj(),
			},
			multiKueueEnabled: true,
			wantManagedBy:     nil,
		},
		{
			name: "TestDefault_MultiKueueFeatureDisabled",
			jobSet: &jobset.JobSet{
				ObjectMeta: ctrl.ObjectMeta{
					Labels: map[string]string{
						constants.QueueLabel: "local-queue",
					},
					Namespace: "default",
				},
			},
			queues: []kueue.LocalQueue{
				*utiltesting.MakeLocalQueue("local-queue", "default").
					ClusterQueue("cluster-queue").
					Obj(),
			},
			clusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("cluster-queue").
					AdmissionChecks("admission-check").
					Obj(),
			},
			admissionCheck: utiltesting.MakeAdmissionCheck("admission-check").
				ControllerName(multikueue.ControllerName).
				Active(metav1.ConditionTrue).
				Obj(),
			multiKueueEnabled: false,
			wantManagedBy:     nil,
		},
		{
			name: "TestDefault_UserSpecifiedManagedBy",
			jobSet: &jobset.JobSet{
				Spec: jobset.JobSetSpec{
					ManagedBy: ptr.To("example.com/foo"),
				},
				ObjectMeta: ctrl.ObjectMeta{
					Labels: map[string]string{
						constants.QueueLabel: "local-queue",
					},
					Namespace: "default",
				},
			},
			queues: []kueue.LocalQueue{
				*utiltesting.MakeLocalQueue("local-queue", "default").
					ClusterQueue("cluster-queue").
					Obj(),
			},
			clusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("cluster-queue").
					AdmissionChecks("admission-check").
					Obj(),
			},
			admissionCheck: utiltesting.MakeAdmissionCheck("admission-check").
				ControllerName(multikueue.ControllerName).
				Active(metav1.ConditionTrue).
				Obj(),
			multiKueueEnabled: true,
			wantManagedBy:     ptr.To("example.com/foo"),
		},
		{
			name: "TestDefault_ClusterQueueWithoutAdmissionCheck",
			jobSet: &jobset.JobSet{
				ObjectMeta: ctrl.ObjectMeta{
					Labels: map[string]string{
						constants.QueueLabel: "local-queue",
					},
					Namespace: "default",
				},
			},
			queues: []kueue.LocalQueue{
				*utiltesting.MakeLocalQueue("local-queue", "default").
					ClusterQueue("cluster-queue").
					Obj(),
			},
			clusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("cluster-queue").
					Obj(),
			},
			multiKueueEnabled: true,
			wantManagedBy:     nil,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			defer features.SetFeatureGateDuringTest(t, features.MultiKueue, tc.multiKueueEnabled)()

			ctx, _ := utiltesting.ContextWithLog(t)

			clientBuilder := utiltesting.NewClientBuilder().
				WithObjects(
					&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
				)
			cl := clientBuilder.Build()
			cqCache := cache.New(cl)
			queueManager := queue.NewManager(cl, cqCache)

			for _, q := range tc.queues {
				if err := queueManager.AddLocalQueue(ctx, &q); err != nil {
					t.Fatalf("Inserting queue %s/%s in manager: %v", q.Namespace, q.Name, err)
				}
			}
			for _, cq := range tc.clusterQueues {
				if err := cqCache.AddClusterQueue(ctx, &cq); err != nil {
					t.Fatalf("Inserting clusterQueue %s in cache: %v", cq.Name, err)
				}
				if tc.admissionCheck != nil {
					cqCache.AddOrUpdateAdmissionCheck(tc.admissionCheck)
					if err := queueManager.AddClusterQueue(ctx, &cq); err != nil {
						t.Fatalf("Inserting clusterQueue %s in manager: %v", cq.Name, err)
					}
				}
			}
			webhook := &JobSetWebhook{
				manageJobsWithoutQueueName: false,
				queues:                     queueManager,
				cache:                      cqCache,
			}

			gotErr := webhook.Default(ctx, tc.jobSet)
			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Default() error mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantManagedBy, tc.jobSet.Spec.ManagedBy); diff != "" {
				t.Errorf("Default() jobSet.Spec.ManagedBy mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
