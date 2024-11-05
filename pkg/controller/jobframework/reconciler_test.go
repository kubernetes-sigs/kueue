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

package jobframework_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	kfmpi "github.com/kubeflow/mpi-operator/pkg/apis/kubeflow/v2beta1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/clock"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
	"sigs.k8s.io/kueue/pkg/util/kubeversion"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	testingmpijob "sigs.k8s.io/kueue/pkg/util/testingjobs/mpijob"

	_ "sigs.k8s.io/kueue/pkg/controller/jobs"

	. "sigs.k8s.io/kueue/pkg/controller/jobframework"
)

func TestIsParentJobManaged(t *testing.T) {
	parentJobName := "test-job-parent"
	childJobName := "test-job-child"
	jobNamespace := "default"
	cases := map[string]struct {
		parentJob   client.Object
		job         client.Object
		wantManaged bool
		wantErr     error
	}{
		"child job has ownerReference with unknown workload owner": {
			parentJob: testingjob.MakeJob(parentJobName, jobNamespace).
				UID(parentJobName).
				Obj(),
			job: testingjob.MakeJob(childJobName, jobNamespace).
				OwnerReference(parentJobName, batchv1.SchemeGroupVersion.WithKind("CronJob")).
				Obj(),
			wantErr: ErrUnknownWorkloadOwner,
		},
		"child job has ownerReference with known non-existing workload owner": {
			job: testingjob.MakeJob(childJobName, jobNamespace).
				OwnerReference(parentJobName, kfmpi.SchemeGroupVersionKind).
				Obj(),
			wantErr: ErrWorkloadOwnerNotFound,
		},
		"child job has ownerReference with known existing workload owner, and the parent job has queue-name label": {
			parentJob: testingmpijob.MakeMPIJob(parentJobName, jobNamespace).
				UID(parentJobName).
				Queue("test-q").
				Obj(),
			job: testingjob.MakeJob(childJobName, jobNamespace).
				OwnerReference(parentJobName, kfmpi.SchemeGroupVersionKind).
				Obj(),
			wantManaged: true,
		},
		"child job has ownerReference with known existing workload owner, and the parent job doesn't has queue-name label": {
			parentJob: testingmpijob.MakeMPIJob(parentJobName, jobNamespace).
				UID(parentJobName).
				Obj(),
			job: testingjob.MakeJob(childJobName, jobNamespace).
				OwnerReference(parentJobName, kfmpi.SchemeGroupVersionKind).
				Obj(),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Cleanup(EnableIntegrationsForTest(t, "kubeflow.org/mpijob"))
			builder := utiltesting.NewClientBuilder(kfmpi.AddToScheme)
			if tc.parentJob != nil {
				builder = builder.WithObjects(tc.parentJob)
			}
			cl := builder.Build()
			r := NewReconciler(cl, nil)
			got, gotErr := r.IsParentJobManaged(context.Background(), tc.job, jobNamespace)
			if tc.wantManaged != got {
				t.Errorf("Unexpected response from isParentManaged want: %v,got: %v", tc.wantManaged, got)
			}
			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.EquateErrors()); len(diff) != 0 {
				t.Errorf("Unexpected error (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestProcessOptions(t *testing.T) {
	fakeClock := testingclock.NewFakeClock(time.Now())
	cases := map[string]struct {
		inputOpts []Option
		wantOpts  Options
	}{
		"all options are passed": {
			inputOpts: []Option{
				WithManageJobsWithoutQueueName(true),
				WithWaitForPodsReady(&configapi.WaitForPodsReady{Enable: true}),
				WithKubeServerVersion(&kubeversion.ServerVersionFetcher{}),
				WithIntegrationOptions(corev1.SchemeGroupVersion.WithKind("Pod").String(), &configapi.PodIntegrationOptions{
					PodSelector: &metav1.LabelSelector{},
				}),
				WithLabelKeysToCopy([]string{"toCopyKey"}),
				WithClock(t, fakeClock),
			},
			wantOpts: Options{
				ManageJobsWithoutQueueName: true,
				WaitForPodsReady:           true,
				KubeServerVersion:          &kubeversion.ServerVersionFetcher{},
				IntegrationOptions: map[string]any{
					corev1.SchemeGroupVersion.WithKind("Pod").String(): &configapi.PodIntegrationOptions{
						PodSelector: &metav1.LabelSelector{},
					},
				},
				LabelKeysToCopy: []string{"toCopyKey"},
				Clock:           fakeClock,
			},
		},
		"a single option is passed": {
			inputOpts: []Option{
				WithManageJobsWithoutQueueName(true),
			},
			wantOpts: Options{
				ManageJobsWithoutQueueName: true,
				WaitForPodsReady:           false,
				KubeServerVersion:          nil,
				IntegrationOptions:         nil,
				Clock:                      clock.RealClock{},
			},
		},
		"no options are passed": {
			wantOpts: Options{
				ManageJobsWithoutQueueName: false,
				WaitForPodsReady:           false,
				KubeServerVersion:          nil,
				IntegrationOptions:         nil,
				LabelKeysToCopy:            nil,
				Clock:                      clock.RealClock{},
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			gotOpts := ProcessOptions(tc.inputOpts...)
			if diff := cmp.Diff(tc.wantOpts, gotOpts,
				cmpopts.IgnoreUnexported(kubeversion.ServerVersionFetcher{}, testingclock.FakePassiveClock{}, testingclock.FakeClock{})); len(diff) != 0 {
				t.Errorf("Unexpected error from ProcessOptions (-want,+got):\n%s", diff)
			}
		})
	}
}
