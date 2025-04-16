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

package jobframework_test

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	kfmpi "github.com/kubeflow/mpi-operator/pkg/apis/kubeflow/v2beta1"
	awv1beta2 "github.com/project-codeflare/appwrapper/api/v1beta2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/clock"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/jobset/api/jobset/v1alpha2"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/util/kubeversion"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingaw "sigs.k8s.io/kueue/pkg/util/testingjobs/appwrapper"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/pkg/util/testingjobs/jobset"
	testingmpijob "sigs.k8s.io/kueue/pkg/util/testingjobs/mpijob"

	_ "sigs.k8s.io/kueue/pkg/controller/jobs"

	. "sigs.k8s.io/kueue/pkg/controller/jobframework"
)

func TestFindAncestorJobManagedByKueue(t *testing.T) {
	grandparentJobName := "test-job-grandparent"
	parentJobName := "test-job-parent"
	childJobName := "test-job-child"
	jobNamespace := "default"

	cronJob := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{
			UID:       types.UID("cronjob"),
			Name:      "cronjob",
			Namespace: jobNamespace,
		},
	}

	cronJobWithQueueNameLabel := cronJob.DeepCopy()
	cronJobWithQueueNameLabel.Labels = map[string]string{
		constants.QueueLabel: "test-q",
	}

	cases := map[string]struct {
		manageJobsWithoutQueueName bool
		integrations               []string
		ancestors                  []client.Object
		job                        client.Object
		wantManaged                client.Object
		wantErr                    error
		wantEvents                 []utiltesting.EventRecord
	}{
		"child job has ownerReference with unmanaged workload owner": {
			ancestors: []client.Object{cronJob.DeepCopy()},
			job: testingjob.MakeJob(childJobName, jobNamespace).
				OwnerReference(cronJob.Name, batchv1.SchemeGroupVersion.WithKind("CronJob")).
				Obj(),
		},
		"child job has ownerReference with unmanaged workload owner that has a queue-name": {
			ancestors: []client.Object{cronJobWithQueueNameLabel.DeepCopy()},
			job: testingjob.MakeJob(childJobName, jobNamespace).
				OwnerReference(cronJob.Name, batchv1.SchemeGroupVersion.WithKind("CronJob")).
				Obj(),
		},
		"child job has ownerReference with unknown non-existing workload owner": {
			job: testingjob.MakeJob(childJobName, jobNamespace).
				OwnerReference(cronJob.Name, kfmpi.SchemeGroupVersionKind).
				Obj(),
			wantErr: ErrWorkloadOwnerNotFound,
		},
		"child job has ownerReference with known non-existing workload owner": {
			integrations: []string{"kubeflow.org/mpijob"},
			job: testingjob.MakeJob(childJobName, jobNamespace).
				OwnerReference(parentJobName, kfmpi.SchemeGroupVersionKind).
				Obj(),
			wantErr: ErrWorkloadOwnerNotFound,
		},
		"child job has ownerReference with known existing workload owner, and the parent job has queue-name label": {
			integrations: []string{"kubeflow.org/mpijob"},
			ancestors: []client.Object{
				testingmpijob.MakeMPIJob(parentJobName, jobNamespace).
					UID(parentJobName).
					Queue("test-q").
					Obj(),
			},
			job: testingjob.MakeJob(childJobName, jobNamespace).
				OwnerReference(parentJobName, kfmpi.SchemeGroupVersionKind).
				Obj(),
			wantManaged: testingmpijob.MakeMPIJob(parentJobName, jobNamespace).
				UID(parentJobName).
				Queue("test-q").
				Obj(),
		},
		"child job has ownerReference with known existing workload owner, and the parent job doesn't has queue-name label": {
			integrations: []string{"kubeflow.org/mpijob"},
			ancestors: []client.Object{
				testingmpijob.MakeMPIJob(parentJobName, jobNamespace).
					UID(parentJobName).
					Obj(),
			},
			job: testingjob.MakeJob(childJobName, jobNamespace).
				OwnerReference(parentJobName, kfmpi.SchemeGroupVersionKind).
				Obj(),
		},
		"cyclic ownership links are properly handled": {
			integrations: []string{"kubeflow.org/mpijob", "workload.codeflare.dev/appwrapper", "batch/job"},
			ancestors: []client.Object{
				testingaw.MakeAppWrapper(grandparentJobName, jobNamespace).
					UID(grandparentJobName).
					OwnerReference(childJobName, batchv1.SchemeGroupVersion.WithKind("Job")).
					Obj(),
				testingmpijob.MakeMPIJob(parentJobName, jobNamespace).
					UID(parentJobName).
					OwnerReference(grandparentJobName, awv1beta2.GroupVersion.WithKind(awv1beta2.AppWrapperKind)).
					Obj(),
			},
			job: testingjob.MakeJob(childJobName, jobNamespace).
				OwnerReference(parentJobName, kfmpi.SchemeGroupVersionKind).
				Obj(),
			wantErr: ErrCyclicOwnership,
		},
		"cuts off ancestor traversal at the limit and generates an appropriate event": {
			integrations: []string{"batch/job"},
			ancestors: []client.Object{
				testingjob.MakeJob("ancestor-0", jobNamespace).UID("ancestor-0").Queue("test-q").Obj(),
				testingjob.MakeJob("ancestor-1", jobNamespace).UID("ancestor-1").OwnerReference("ancestor-0", batchv1.SchemeGroupVersion.WithKind("Job")).Obj(),
				testingjob.MakeJob("ancestor-2", jobNamespace).UID("ancestor-2").OwnerReference("ancestor-1", batchv1.SchemeGroupVersion.WithKind("Job")).Obj(),
				testingjob.MakeJob("ancestor-3", jobNamespace).UID("ancestor-3").OwnerReference("ancestor-2", batchv1.SchemeGroupVersion.WithKind("Job")).Obj(),
				testingjob.MakeJob("ancestor-4", jobNamespace).UID("ancestor-4").OwnerReference("ancestor-3", batchv1.SchemeGroupVersion.WithKind("Job")).Obj(),
				testingjob.MakeJob("ancestor-5", jobNamespace).UID("ancestor-5").OwnerReference("ancestor-4", batchv1.SchemeGroupVersion.WithKind("Job")).Obj(),
				testingjob.MakeJob("ancestor-6", jobNamespace).UID("ancestor-6").OwnerReference("ancestor-5", batchv1.SchemeGroupVersion.WithKind("Job")).Obj(),
				testingjob.MakeJob("ancestor-7", jobNamespace).UID("ancestor-7").OwnerReference("ancestor-6", batchv1.SchemeGroupVersion.WithKind("Job")).Obj(),
				testingjob.MakeJob("ancestor-8", jobNamespace).UID("ancestor-8").OwnerReference("ancestor-7", batchv1.SchemeGroupVersion.WithKind("Job")).Obj(),
				testingjob.MakeJob("ancestor-9", jobNamespace).UID("ancestor-9").OwnerReference("ancestor-8", batchv1.SchemeGroupVersion.WithKind("Job")).Obj(),
				testingjob.MakeJob("ancestor-10", jobNamespace).UID("ancestor-10").OwnerReference("ancestor-9", batchv1.SchemeGroupVersion.WithKind("Job")).Obj(),
				testingjob.MakeJob("ancestor-11", jobNamespace).UID("ancestor-11").OwnerReference("ancestor-10", batchv1.SchemeGroupVersion.WithKind("Job")).Obj(),
			},
			job: testingjob.MakeJob(childJobName, jobNamespace).
				OwnerReference("ancestor-11", batchv1.SchemeGroupVersion.WithKind("Job")).
				Obj(),
			wantErr: ErrManagedOwnersChainLimitReached,
		},
		"Job -> JobSet -> AppWrapper => nil": {
			integrations: []string{"jobset.x-k8s.io/jobset", "workload.codeflare.dev/appwrapper"},
			ancestors: []client.Object{
				testingaw.MakeAppWrapper("aw", jobNamespace).UID("aw").Obj(),
				jobset.MakeJobSet("jobset", jobNamespace).UID("jobset").
					OwnerReference("aw", awv1beta2.GroupVersion.WithKind(awv1beta2.AppWrapperKind)).
					Obj(),
			},
			job: testingjob.MakeJob("job", jobNamespace).UID("job").
				OwnerReference("jobset", v1alpha2.SchemeGroupVersion.WithKind("JobSet")).
				Obj(),
			wantManaged: nil,
		},
		"Job (queue-name) -> JobSet (queue-name) -> AppWrapper => JobSet": {
			integrations: []string{"jobset.x-k8s.io/jobset", "workload.codeflare.dev/appwrapper"},
			ancestors: []client.Object{
				testingaw.MakeAppWrapper("aw", jobNamespace).UID("aw").Obj(),
				jobset.MakeJobSet("jobset", jobNamespace).UID("jobset").
					OwnerReference("aw", awv1beta2.GroupVersion.WithKind(awv1beta2.AppWrapperKind)).
					Queue("test-q").
					Obj(),
			},
			job: testingjob.MakeJob("job", jobNamespace).UID("job").
				OwnerReference("jobset", v1alpha2.SchemeGroupVersion.WithKind("JobSet")).
				Queue("test-q").
				Obj(),
			wantManaged: jobset.MakeJobSet("jobset", jobNamespace).UID("jobset").
				OwnerReference("aw", awv1beta2.GroupVersion.WithKind(awv1beta2.AppWrapperKind)).
				Queue("test-q").
				Obj(),
		},
		"Job (queue-name) -> JobSet -> AppWrapper (queue-name) => AppWrapper": {
			integrations: []string{"jobset.x-k8s.io/jobset", "workload.codeflare.dev/appwrapper"},
			ancestors: []client.Object{
				testingaw.MakeAppWrapper("aw", jobNamespace).UID("aw").Queue("test-q").Obj(),
				jobset.MakeJobSet("jobset", jobNamespace).UID("jobset").
					OwnerReference("aw", awv1beta2.GroupVersion.WithKind(awv1beta2.AppWrapperKind)).
					Obj(),
			},
			job: testingjob.MakeJob("job", jobNamespace).UID("job").
				OwnerReference("jobset", v1alpha2.SchemeGroupVersion.WithKind("JobSet")).
				Queue("test-q").
				Obj(),
			wantManaged: testingaw.MakeAppWrapper("aw", jobNamespace).UID("aw").Queue("test-q").Obj(),
		},
		"Job (queue-name) -> JobSet (queue-name) -> AppWrapper (queue-name) => AppWrapper": {
			integrations: []string{"jobset.x-k8s.io/jobset", "workload.codeflare.dev/appwrapper"},
			ancestors: []client.Object{
				testingaw.MakeAppWrapper("aw", jobNamespace).UID("aw").
					Queue("test-q").
					Obj(),
				jobset.MakeJobSet("jobset", jobNamespace).UID("jobset").
					OwnerReference("aw", awv1beta2.GroupVersion.WithKind(awv1beta2.AppWrapperKind)).
					Queue("test-q").
					Obj(),
			},
			job: testingjob.MakeJob("job", jobNamespace).UID("job").
				OwnerReference("jobset", v1alpha2.SchemeGroupVersion.WithKind("JobSet")).
				Queue("test-q").
				Obj(),
			wantManaged: testingaw.MakeAppWrapper("aw", jobNamespace).UID("aw").Queue("test-q").Obj(),
		},
		"Job -> JobSet (disabled) -> AppWrapper (queue-name) => AppWrapper": {
			integrations: []string{"workload.codeflare.dev/appwrapper"},
			ancestors: []client.Object{
				testingaw.MakeAppWrapper("aw", jobNamespace).UID("aw").Queue("test-q").Obj(),
				jobset.MakeJobSet("jobset", jobNamespace).UID("jobset").
					OwnerReference("aw", awv1beta2.GroupVersion.WithKind(awv1beta2.AppWrapperKind)).
					Obj(),
			},
			job: testingjob.MakeJob("job", jobNamespace).UID("job").
				OwnerReference("jobset", v1alpha2.SchemeGroupVersion.WithKind("JobSet")).
				Obj(),
			wantManaged: testingaw.MakeAppWrapper("aw", jobNamespace).UID("aw").Queue("test-q").Obj(),
		},
		"Job -> JobSet -> AppWrapper => AppWrapper (manageJobsWithoutQueueName)": {
			manageJobsWithoutQueueName: true,
			integrations:               []string{"jobset.x-k8s.io/jobset", "workload.codeflare.dev/appwrapper"},
			ancestors: []client.Object{
				testingaw.MakeAppWrapper("aw", jobNamespace).UID("aw").Obj(),
				jobset.MakeJobSet("jobset", jobNamespace).UID("jobset").
					OwnerReference("aw", awv1beta2.GroupVersion.WithKind(awv1beta2.AppWrapperKind)).
					Obj(),
			},
			job: testingjob.MakeJob("job", jobNamespace).UID("job").
				OwnerReference("jobset", v1alpha2.SchemeGroupVersion.WithKind("JobSet")).
				Obj(),
			wantManaged: testingaw.MakeAppWrapper("aw", jobNamespace).UID("aw").Obj(),
		},
		"Job (queue-name) -> JobSet (queue-name) -> AppWrapper => AppWrapper (manageJobsWithoutQueueName)": {
			manageJobsWithoutQueueName: true,
			integrations:               []string{"jobset.x-k8s.io/jobset", "workload.codeflare.dev/appwrapper"},
			ancestors: []client.Object{
				testingaw.MakeAppWrapper("aw", jobNamespace).UID("aw").Obj(),
				jobset.MakeJobSet("jobset", jobNamespace).UID("jobset").
					OwnerReference("aw", awv1beta2.GroupVersion.WithKind(awv1beta2.AppWrapperKind)).
					Queue("test-q").
					Obj(),
			},
			job: testingjob.MakeJob("job", jobNamespace).UID("job").
				OwnerReference("jobset", v1alpha2.SchemeGroupVersion.WithKind("JobSet")).
				Queue("test-q").
				Obj(),
			wantManaged: testingaw.MakeAppWrapper("aw", jobNamespace).UID("aw").Obj(),
		},
		"Job (queue-name) -> JobSet -> AppWrapper (queue-name) => AppWrapper (manageJobsWithoutQueueName)": {
			manageJobsWithoutQueueName: true,
			integrations:               []string{"jobset.x-k8s.io/jobset", "workload.codeflare.dev/appwrapper"},
			ancestors: []client.Object{
				testingaw.MakeAppWrapper("aw", jobNamespace).UID("aw").Queue("test-q").Obj(),
				jobset.MakeJobSet("jobset", jobNamespace).UID("jobset").
					OwnerReference("aw", awv1beta2.GroupVersion.WithKind(awv1beta2.AppWrapperKind)).
					Obj(),
			},
			job: testingjob.MakeJob("job", jobNamespace).UID("job").
				OwnerReference("jobset", v1alpha2.SchemeGroupVersion.WithKind("JobSet")).
				Queue("test-q").
				Obj(),
			wantManaged: testingaw.MakeAppWrapper("aw", jobNamespace).UID("aw").Queue("test-q").Obj(),
		},
		"Job (queue-name) -> JobSet (queue-name) -> AppWrapper (queue-name) => AppWrapper (manageJobsWithoutQueueName)": {
			manageJobsWithoutQueueName: true,
			integrations:               []string{"jobset.x-k8s.io/jobset", "workload.codeflare.dev/appwrapper"},
			ancestors: []client.Object{
				testingaw.MakeAppWrapper("aw", jobNamespace).UID("aw").Queue("test-q").Obj(),
				jobset.MakeJobSet("jobset", jobNamespace).UID("jobset").
					OwnerReference("aw", awv1beta2.GroupVersion.WithKind(awv1beta2.AppWrapperKind)).
					Queue("test-q").
					Obj(),
			},
			job: testingjob.MakeJob("job", jobNamespace).UID("job").
				OwnerReference("jobset", v1alpha2.SchemeGroupVersion.WithKind("JobSet")).
				Queue("test-q").
				Obj(),
			wantManaged: testingaw.MakeAppWrapper("aw", jobNamespace).UID("aw").Queue("test-q").Obj(),
		},
		"Job -> JobSet (disabled) -> AppWrapper => AppWrapper (manageJobsWithoutQueueName)": {
			manageJobsWithoutQueueName: true,
			integrations:               []string{"workload.codeflare.dev/appwrapper"},
			ancestors: []client.Object{
				testingaw.MakeAppWrapper("aw", jobNamespace).UID("aw").Obj(),
				jobset.MakeJobSet("jobset", jobNamespace).UID("jobset").
					OwnerReference("aw", awv1beta2.GroupVersion.WithKind(awv1beta2.AppWrapperKind)).
					Obj(),
			},
			job: testingjob.MakeJob("job", jobNamespace).UID("job").
				OwnerReference("jobset", v1alpha2.SchemeGroupVersion.WithKind("JobSet")).
				Obj(),
			wantManaged: testingaw.MakeAppWrapper("aw", jobNamespace).UID("aw").Obj(),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Cleanup(EnableIntegrationsForTest(t, tc.integrations...))
			ctx, _ := utiltesting.ContextWithLog(t)
			recorder := &utiltesting.EventRecorder{}
			builder := utiltesting.NewClientBuilder(kfmpi.AddToScheme, awv1beta2.AddToScheme, v1alpha2.AddToScheme)
			builder = builder.WithObjects(tc.ancestors...)
			if tc.job != nil {
				builder = builder.WithObjects(tc.job)
			}
			cl := builder.Build()
			gotManaged, gotErr := FindAncestorJobManagedByKueue(ctx, cl, tc.job, tc.manageJobsWithoutQueueName)
			if diff := cmp.Diff(tc.wantManaged, gotManaged, cmp.Options{
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion"),
				cmpopts.EquateEmpty(),
			}); len(diff) != 0 {
				t.Errorf("Unexpected managed job (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.EquateErrors()); len(diff) != 0 {
				t.Errorf("Unexpected error (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantEvents, recorder.RecordedEvents); diff != "" {
				t.Errorf("Unexpected events (-want/+got):\n%s", diff)
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
