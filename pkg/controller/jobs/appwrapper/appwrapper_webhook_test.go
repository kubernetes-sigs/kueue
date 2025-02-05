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

package appwrapper

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	awv1beta2 "github.com/project-codeflare/appwrapper/api/v1beta2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/queue"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utilaw "sigs.k8s.io/kueue/pkg/util/testingjobs/appwrapper"
)

func TestBaseWebhookDefault(t *testing.T) {
	testcases := map[string]struct {
		manageJobsWithoutQueueName bool
		localQueueDefaulting       bool
		defaultLqExist             bool
		multiQueue                 bool
		job                        *awv1beta2.AppWrapper
		want                       *awv1beta2.AppWrapper
	}{
		"update the suspend field with 'manageJobsWithoutQueueName=false'": {
			job: utilaw.MakeAppWrapper("aw", "default").
				Label(constants.QueueLabel, "default").
				Obj(),
			want: utilaw.MakeAppWrapper("aw", "default").
				Label(constants.QueueLabel, "default").
				Suspend(true).
				Obj(),
		},
		"update the suspend field 'manageJobsWithoutQueueName=true'": {
			manageJobsWithoutQueueName: true,
			job: utilaw.MakeAppWrapper("aw", "default").
				Label(constants.QueueLabel, "default").
				Obj(),
			want: utilaw.MakeAppWrapper("aw", "default").
				Label(constants.QueueLabel, "default").
				Suspend(true).
				Obj(),
		},
		"LocalQueueDefaulting enabled, default lq is created, job doesn't have queue label": {
			localQueueDefaulting: true,
			defaultLqExist:       true,
			job: utilaw.MakeAppWrapper("job", "default").
				Obj(),
			want: utilaw.MakeAppWrapper("job", "default").
				Label(constants.QueueLabel, "default").
				Obj(),
		},
		"LocalQueueDefaulting enabled, default lq is created, job has queue label": {
			localQueueDefaulting: true,
			defaultLqExist:       true,
			job: utilaw.MakeAppWrapper("job", "default").
				Queue("queue").
				Obj(),
			want: utilaw.MakeAppWrapper("job", "default").
				Queue("queue").
				Obj(),
		},
		"LocalQueueDefaulting enabled, default lq isn't created, job doesn't have queue label": {
			localQueueDefaulting: true,
			defaultLqExist:       false,
			job: utilaw.MakeAppWrapper("job", "default").
				Obj(),
			want: utilaw.MakeAppWrapper("job", "default").
				Obj(),
		},
		"ManagedByDefaulting, targeting multikueue local queue": {
			job: utilaw.MakeAppWrapper("job", "default").
				Queue("multikueue").
				Obj(),
			want: utilaw.MakeAppWrapper("job", "default").
				Queue("multikueue").
				ManagedBy(kueue.MultiKueueControllerName).
				Obj(),
			multiQueue: true,
		},
		"ManagedByDefaulting, targeting multikueue local queue but already managaed by someone else": {
			job: utilaw.MakeAppWrapper("job", "default").
				Queue("multikueue").
				ManagedBy("someone-else").
				Obj(),
			want: utilaw.MakeAppWrapper("job", "default").
				Queue("multikueue").
				ManagedBy("someone-else").
				Obj(),
			multiQueue: true,
		},
		"ManagedByDefaulting, targeting non-multikueue local queue": {
			job: utilaw.MakeAppWrapper("job", "default").
				Queue("queue").
				Obj(),
			want: utilaw.MakeAppWrapper("job", "default").
				Queue("queue").
				Obj(),
			multiQueue: true,
		},
	}
	for name, tc := range testcases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			features.SetFeatureGateDuringTest(t, features.LocalQueueDefaulting, tc.localQueueDefaulting)
			features.SetFeatureGateDuringTest(t, features.MultiKueue, tc.multiQueue)
			clientBuilder := utiltesting.NewClientBuilder().
				WithObjects(
					&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
				)
			cl := clientBuilder.Build()
			cqCache := cache.New(cl)
			queueManager := queue.NewManager(cl, cqCache)
			if tc.defaultLqExist {
				if err := queueManager.AddLocalQueue(ctx, utiltesting.MakeLocalQueue("default", "default").
					ClusterQueue("cluster-queue").Obj()); err != nil {
					t.Fatalf("failed to create default local queue: %s", err)
				}
			}
			if tc.multiQueue {
				if err := queueManager.AddLocalQueue(ctx, utiltesting.MakeLocalQueue("multikueue", "default").
					ClusterQueue("cluster-queue").Obj()); err != nil {
					t.Fatalf("failed to create default local queue: %s", err)
				}
				cq := *utiltesting.MakeClusterQueue("cluster-queue").
					AdmissionChecks("admission-check").
					Obj()
				if err := cqCache.AddClusterQueue(ctx, &cq); err != nil {
					t.Fatalf("Inserting clusterQueue %s in cache: %v", cq.Name, err)
				}
				ac := utiltesting.MakeAdmissionCheck("admission-check").
					ControllerName(kueue.MultiKueueControllerName).
					Active(metav1.ConditionTrue).
					Obj()
				cqCache.AddOrUpdateAdmissionCheck(ac)
				if err := queueManager.AddClusterQueue(ctx, &cq); err != nil {
					t.Fatalf("Inserting clusterQueue %s in manager: %v", cq.Name, err)
				}
			}

			w := &jobframework.BaseWebhook{
				ManageJobsWithoutQueueName: tc.manageJobsWithoutQueueName,
				FromObject: func(o runtime.Object) jobframework.GenericJob {
					return fromObject(o)
				},
				Queues: queueManager,
				Cache:  cqCache,
			}
			if err := w.Default(context.Background(), tc.job); err != nil {
				t.Errorf("set defaults by base webhook")
			}
			if diff := cmp.Diff(tc.want, tc.job); len(diff) != 0 {
				t.Errorf("Default() mismatch (-want,+got):\n%s", diff)
			}
		})
	}
}
