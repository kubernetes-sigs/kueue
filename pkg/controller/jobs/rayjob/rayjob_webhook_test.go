/*
Copyright 2023 The Kubernetes Authors.

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

package rayjob

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	rayjobapi "github.com/ray-project/kuberay/ray-operator/apis/ray/v1alpha1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/kueue/pkg/controller/constants"
	testingrayutil "sigs.k8s.io/kueue/pkg/util/testingjobs/rayjob"
)

func TestValidateDefault(t *testing.T) {
	testcases := map[string]struct {
		oldJob    *rayjobapi.RayJob
		newJob    *rayjobapi.RayJob
		manageAll bool
	}{
		"unmanaged": {
			oldJob: testingrayutil.MakeJob("job", "ns").
				Suspend(false).
				Obj(),
			newJob: testingrayutil.MakeJob("job", "ns").
				Suspend(false).
				Obj(),
		},
		"managed - by config": {
			oldJob: testingrayutil.MakeJob("job", "ns").
				Suspend(false).
				Obj(),
			newJob: testingrayutil.MakeJob("job", "ns").
				Suspend(true).
				Obj(),
			manageAll: true,
		},
		"managed - by queue": {
			oldJob: testingrayutil.MakeJob("job", "ns").
				Queue("queue").
				Suspend(false).
				Obj(),
			newJob: testingrayutil.MakeJob("job", "ns").
				Queue("queue").
				Suspend(true).
				Obj(),
		},
	}

	for name, tc := range testcases {
		t.Run(name, func(t *testing.T) {
			wh := &RayJobWebhook{
				manageJobsWithoutQueueName: tc.manageAll,
			}
			result := tc.oldJob.DeepCopy()
			if err := wh.Default(context.Background(), result); err != nil {
				t.Errorf("unexpected Default() error: %s", err)
			}
			if diff := cmp.Diff(tc.newJob, result); diff != "" {
				t.Errorf("Default() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestValidateCreate(t *testing.T) {
	worker := rayjobapi.WorkerGroupSpec{}
	bigWorkerGroup := []rayjobapi.WorkerGroupSpec{worker, worker, worker, worker, worker, worker, worker, worker}

	testcases := map[string]struct {
		job       *rayjobapi.RayJob
		manageAll bool
		wantErr   error
	}{
		"invalid unmanaged": {
			job: testingrayutil.MakeJob("job", "ns").
				ShutdownAfterJobFinishes(false).
				Obj(),
			wantErr: nil,
		},
		"invalid managed - by config": {
			job: testingrayutil.MakeJob("job", "ns").
				ShutdownAfterJobFinishes(false).
				Obj(),
			manageAll: true,
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec", "shutdownAfterJobFinishes"), false, "a kueue managed job should delete the cluster after finishing"),
			}.ToAggregate(),
		},
		"invalid managed - by queue": {
			job: testingrayutil.MakeJob("job", "ns").Queue("queue").
				ShutdownAfterJobFinishes(false).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec", "shutdownAfterJobFinishes"), false, "a kueue managed job should delete the cluster after finishing"),
			}.ToAggregate(),
		},
		"invalid managed - has cluster selector": {
			job: testingrayutil.MakeJob("job", "ns").Queue("queue").
				ClusterSelector(map[string]string{
					"k1": "v1",
				}).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec", "clusterSelector"), map[string]string{"k1": "v1"}, "a kueue managed job should not use an existing cluster"),
			}.ToAggregate(),
		},
		"invalid managed - has auto scaler": {
			job: testingrayutil.MakeJob("job", "ns").Queue("queue").
				WithEnableAutoscaling(ptr.To(true)).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec", "rayClusterSpec", "enableInTreeAutoscaling"), ptr.To(true), "a kueue managed job should not use autoscaling"),
			}.ToAggregate(),
		},
		"invalid managed - too many worker groups": {
			job: testingrayutil.MakeJob("job", "ns").Queue("queue").
				WithWorkerGroups(bigWorkerGroup...).
				Obj(),
			wantErr: field.ErrorList{
				field.TooMany(field.NewPath("spec", "rayClusterSpec", "workerGroupSpecs"), 8, 7),
			}.ToAggregate(),
		},
		"worker group uses head name": {
			job: testingrayutil.MakeJob("job", "ns").Queue("queue").
				WithWorkerGroups(rayjobapi.WorkerGroupSpec{
					GroupName: headGroupPodSetName,
				}).
				Obj(),
			wantErr: field.ErrorList{
				field.Forbidden(field.NewPath("spec", "rayClusterSpec", "workerGroupSpecs").Index(0).Child("groupName"), fmt.Sprintf("%q is reserved for the head group", headGroupPodSetName)),
			}.ToAggregate(),
		},
	}

	for name, tc := range testcases {
		t.Run(name, func(t *testing.T) {
			wh := &RayJobWebhook{
				manageJobsWithoutQueueName: tc.manageAll,
			}
			_, result := wh.ValidateCreate(context.Background(), tc.job)
			if diff := cmp.Diff(tc.wantErr, result); diff != "" {
				t.Errorf("ValidateCreate() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestValidateUpdate(t *testing.T) {
	testcases := map[string]struct {
		oldJob    *rayjobapi.RayJob
		newJob    *rayjobapi.RayJob
		manageAll bool
		wantErr   error
	}{
		"invalid unmanaged": {
			oldJob: testingrayutil.MakeJob("job", "ns").
				ShutdownAfterJobFinishes(true).
				Obj(),
			newJob: testingrayutil.MakeJob("job", "ns").
				ShutdownAfterJobFinishes(false).
				Obj(),
			wantErr: nil,
		},
		"invalid new managed - by config": {
			oldJob: testingrayutil.MakeJob("job", "ns").
				ShutdownAfterJobFinishes(true).
				Obj(),
			newJob: testingrayutil.MakeJob("job", "ns").
				ShutdownAfterJobFinishes(false).
				Obj(),
			manageAll: true,
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec", "shutdownAfterJobFinishes"), false, "a kueue managed job should delete the cluster after finishing"),
			}.ToAggregate(),
		},
		"invalid new managed - by queue": {
			oldJob: testingrayutil.MakeJob("job", "ns").
				Queue("queue").
				ShutdownAfterJobFinishes(true).
				Obj(),
			newJob: testingrayutil.MakeJob("job", "ns").
				Queue("queue").
				ShutdownAfterJobFinishes(false).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec", "shutdownAfterJobFinishes"), false, "a kueue managed job should delete the cluster after finishing"),
			}.ToAggregate(),
		},
		"invalid managed - queue name should not change while unsuspended": {
			oldJob: testingrayutil.MakeJob("job", "ns").
				Queue("queue").
				Suspend(false).
				ShutdownAfterJobFinishes(true).
				Obj(),
			newJob: testingrayutil.MakeJob("job", "ns").
				Queue("queue2").
				Suspend(false).
				ShutdownAfterJobFinishes(true).
				Obj(),
			wantErr: field.ErrorList{
				field.Forbidden(field.NewPath("metadata", "labels").Key(constants.QueueLabel), "must not update queue name when job is unsuspend"),
			}.ToAggregate(),
		},
		"managed - queue name can change while suspended": {
			oldJob: testingrayutil.MakeJob("job", "ns").
				Queue("queue").
				Suspend(true).
				ShutdownAfterJobFinishes(true).
				Obj(),
			newJob: testingrayutil.MakeJob("job", "ns").
				Queue("queue2").
				Suspend(true).
				ShutdownAfterJobFinishes(true).
				Obj(),
			wantErr: nil,
		},
	}

	for name, tc := range testcases {
		t.Run(name, func(t *testing.T) {
			wh := &RayJobWebhook{
				manageJobsWithoutQueueName: tc.manageAll,
			}
			_, result := wh.ValidateUpdate(context.Background(), tc.oldJob, tc.newJob)
			if diff := cmp.Diff(tc.wantErr, result); diff != "" {
				t.Errorf("ValidateUpdate() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
