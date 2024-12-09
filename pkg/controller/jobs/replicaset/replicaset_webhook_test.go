/*
Copyright 2024 The Kubernetes Authors.

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

package replicaset

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/queue"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingreplicaset "sigs.k8s.io/kueue/pkg/util/testingjobs/replicaset"
)

func TestDefault(t *testing.T) {
	testCases := map[string]struct {
		replicaset           *appsv1.ReplicaSet
		localQueueDefaulting bool
		defaultLqExist       bool
		want                 *appsv1.ReplicaSet
	}{
		"replicaset without queue": {
			replicaset: testingreplicaset.MakeReplicaSet("test-pod", "").Obj(),
			want:       testingreplicaset.MakeReplicaSet("test-pod", "").Obj(),
		},
		"replicaset with queue": {
			replicaset: testingreplicaset.MakeReplicaSet("test-pod", "").
				Queue("test-queue").
				Obj(),
			want: testingreplicaset.MakeReplicaSet("test-pod", "").
				Queue("test-queue").
				PodTemplateSpecQueue("test-queue").
				Obj(),
		},
		"replicaset with queue and pod template spec queue": {
			replicaset: testingreplicaset.MakeReplicaSet("test-pod", "").
				Queue("new-test-queue").
				PodTemplateSpecQueue("test-queue").
				Obj(),
			want: testingreplicaset.MakeReplicaSet("test-pod", "").
				Queue("new-test-queue").
				PodTemplateSpecQueue("new-test-queue").
				Obj(),
		},
		"replicaset without queue with pod template spec queue": {
			replicaset: testingreplicaset.MakeReplicaSet("test-pod", "").PodTemplateSpecQueue("test-queue").Obj(),
			want:       testingreplicaset.MakeReplicaSet("test-pod", "").PodTemplateSpecQueue("test-queue").Obj(),
		},
		"LocalQueueDefaulting enabled, default lq is created, job doesn't have queue label": {
			localQueueDefaulting: true,
			defaultLqExist:       true,
			replicaset:           testingreplicaset.MakeReplicaSet("test-pod", "default").Obj(),
			want: testingreplicaset.MakeReplicaSet("test-pod", "default").
				Queue("default").
				PodTemplateSpecQueue("default").
				Obj(),
		},
		"LocalQueueDefaulting enabled, default lq is created, job has queue label": {
			localQueueDefaulting: true,
			defaultLqExist:       true,
			replicaset:           testingreplicaset.MakeReplicaSet("test-pod", "").Queue("test-queue").Obj(),
			want: testingreplicaset.MakeReplicaSet("test-pod", "").
				Queue("test-queue").
				PodTemplateSpecQueue("test-queue").
				Obj(),
		},
		"LocalQueueDefaulting enabled, default lq isn't created, job doesn't have queue label": {
			localQueueDefaulting: true,
			defaultLqExist:       false,
			replicaset:           testingreplicaset.MakeReplicaSet("test-pod", "").Obj(),
			want: testingreplicaset.MakeReplicaSet("test-pod", "").
				Obj(),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			features.SetFeatureGateDuringTest(t, features.LocalQueueDefaulting, tc.localQueueDefaulting)
			t.Cleanup(jobframework.EnableIntegrationsForTest(t, "pod"))
			builder := utiltesting.NewClientBuilder()
			client := builder.Build()
			cqCache := cache.New(client)
			queueManager := queue.NewManager(client, cqCache)
			if tc.defaultLqExist {
				if err := queueManager.AddLocalQueue(ctx, utiltesting.MakeLocalQueue("default", "default").
					ClusterQueue("cluster-queue").
					Obj()); err != nil {
					t.Fatalf("failed to create default local queue: %s", err)
				}
			}
			w := &Webhook{
				client: client,
				queues: queueManager,
			}

			if err := w.Default(ctx, tc.replicaset); err != nil {
				t.Errorf("failed to set defaults for v1/replicaset: %s", err)
			}
			if diff := cmp.Diff(tc.want, tc.replicaset); len(diff) != 0 {
				t.Errorf("Default() mismatch (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestValidateCreate(t *testing.T) {
	testCases := map[string]struct {
		replicaset *appsv1.ReplicaSet
		wantErr    error
		wantWarns  admission.Warnings
	}{
		"without queue": {
			replicaset: testingreplicaset.MakeReplicaSet("test-pod", "").Obj(),
		},
		"valid queue name": {
			replicaset: testingreplicaset.MakeReplicaSet("test-pod", "").
				Queue("test-queue").
				Obj(),
		},
		"invalid queue name": {
			replicaset: testingreplicaset.MakeReplicaSet("test-pod", "").
				Queue("test/queue").
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "metadata.labels[kueue.x-k8s.io/queue-name]",
				},
			}.ToAggregate(),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			t.Cleanup(jobframework.EnableIntegrationsForTest(t, "pod"))

			builder := utiltesting.NewClientBuilder()
			client := builder.Build()

			w := &Webhook{client: client}

			ctx, _ := utiltesting.ContextWithLog(t)

			warns, err := w.ValidateCreate(ctx, tc.replicaset)
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.IgnoreFields(field.Error{}, "BadValue", "Detail")); diff != "" {
				t.Errorf("Unexpected error (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(warns, tc.wantWarns); diff != "" {
				t.Errorf("Expected different list of warnings (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestValidateUpdate(t *testing.T) {
	testCases := map[string]struct {
		oldReplicaSet *appsv1.ReplicaSet
		newReplicaSet *appsv1.ReplicaSet
		wantErr       error
		wantWarns     admission.Warnings
	}{
		"without queue (no changes)": {
			oldReplicaSet: testingreplicaset.MakeReplicaSet("test-pod", "").Obj(),
			newReplicaSet: testingreplicaset.MakeReplicaSet("test-pod", "").Obj(),
		},
		"without queue": {
			oldReplicaSet: testingreplicaset.MakeReplicaSet("test-pod", "").
				Queue("test-queue").
				Obj(),
			newReplicaSet: testingreplicaset.MakeReplicaSet("test-pod", "").Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "metadata.labels[kueue.x-k8s.io/queue-name]",
				},
			}.ToAggregate(),
		},
		"with queue (no changes)": {
			oldReplicaSet: testingreplicaset.MakeReplicaSet("test-pod", "").
				Queue("test-queue").
				Obj(),
			newReplicaSet: testingreplicaset.MakeReplicaSet("test-pod", "").
				Queue("test-queue").
				Obj(),
		},
		"with queue": {
			oldReplicaSet: testingreplicaset.MakeReplicaSet("test-pod", "").Obj(),
			newReplicaSet: testingreplicaset.MakeReplicaSet("test-pod", "").
				Queue("test-queue").
				Obj(),
		},
		"with queue (invalid)": {
			oldReplicaSet: testingreplicaset.MakeReplicaSet("test-pod", "").
				Queue("test/queue").
				Obj(),
			newReplicaSet: testingreplicaset.MakeReplicaSet("test-pod", "").
				Queue("test/queue").
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "metadata.labels[kueue.x-k8s.io/queue-name]",
				},
			}.ToAggregate(),
		},
		"with queue (ready replicas)": {
			oldReplicaSet: testingreplicaset.MakeReplicaSet("test-pod", "").
				Queue("test-queue").
				ReadyReplicas(1).
				Obj(),
			newReplicaSet: testingreplicaset.MakeReplicaSet("test-pod", "").
				Queue("test-queue-new").
				ReadyReplicas(1).
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "metadata.labels[kueue.x-k8s.io/queue-name]",
				},
			}.ToAggregate(),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			t.Cleanup(jobframework.EnableIntegrationsForTest(t, "pod"))

			builder := utiltesting.NewClientBuilder()
			client := builder.Build()

			w := &Webhook{client: client}

			ctx, _ := utiltesting.ContextWithLog(t)

			warns, err := w.ValidateUpdate(ctx, tc.oldReplicaSet, tc.newReplicaSet)
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.IgnoreFields(field.Error{}, "BadValue", "Detail")); diff != "" {
				t.Errorf("Unexpected error (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(warns, tc.wantWarns); diff != "" {
				t.Errorf("Expected different list of warnings (-want,+got):\n%s", diff)
			}
		})
	}
}
