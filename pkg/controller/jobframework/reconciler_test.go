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
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/clock"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
	"sigs.k8s.io/kueue/pkg/util/kubeversion"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingaw "sigs.k8s.io/kueue/pkg/util/testingjobs/appwrapper"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/pkg/util/testingjobs/leaderworkerset"
	testingmpijob "sigs.k8s.io/kueue/pkg/util/testingjobs/mpijob"
	"sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/pkg/util/testingjobs/statefulset"

	_ "sigs.k8s.io/kueue/pkg/controller/jobs"

	. "sigs.k8s.io/kueue/pkg/controller/jobframework"
)

func TestIsAncestorJobManaged(t *testing.T) {
	grandparentJobName := "test-job-grandparent"
	parentJobName := "test-job-parent"
	childJobName := "test-job-child"
	jobNamespace := "default"
	cases := map[string]struct {
		enableIntegrations []string
		ancestors          []client.Object
		job                client.Object
		wantManaged        bool
		wantErr            error
		wantEvents         []utiltesting.EventRecord
	}{
		"child job has ownerReference with unmanaged workload owner": {
			enableIntegrations: []string{"batch/job", "kubeflow.org/mpijob", "workload.codeflare.dev/appwrapper"},
			ancestors: []client.Object{
				testingjob.MakeJob(parentJobName, jobNamespace).UID(parentJobName).Obj(),
			},
			job: testingjob.MakeJob(childJobName, jobNamespace).
				OwnerReference(parentJobName, batchv1.SchemeGroupVersion.WithKind("CronJob")).
				Obj(),
			wantManaged: false,
		},
		"child job has ownerReference with known non-existing workload owner": {
			enableIntegrations: []string{"batch/job", "kubeflow.org/mpijob", "workload.codeflare.dev/appwrapper"},
			job: testingjob.MakeJob(childJobName, jobNamespace).
				OwnerReference(parentJobName, kfmpi.SchemeGroupVersionKind).
				Obj(),
			wantErr: ErrWorkloadOwnerNotFound,
		},
		"child job has ownerReference with known existing workload owner, and the parent job has queue-name label": {
			enableIntegrations: []string{"batch/job", "kubeflow.org/mpijob", "workload.codeflare.dev/appwrapper"},
			ancestors: []client.Object{
				testingmpijob.MakeMPIJob(parentJobName, jobNamespace).
					UID(parentJobName).
					Queue("test-q").
					Obj(),
			},
			job: testingjob.MakeJob(childJobName, jobNamespace).
				OwnerReference(parentJobName, kfmpi.SchemeGroupVersionKind).
				Obj(),
			wantManaged: true,
		},
		"child job has ownerReference with known existing workload owner, and the parent job doesn't has queue-name label": {
			enableIntegrations: []string{"batch/job", "kubeflow.org/mpijob", "workload.codeflare.dev/appwrapper"},
			ancestors: []client.Object{
				testingmpijob.MakeMPIJob(parentJobName, jobNamespace).
					UID(parentJobName).
					Obj(),
			},
			job: testingjob.MakeJob(childJobName, jobNamespace).
				OwnerReference(parentJobName, kfmpi.SchemeGroupVersionKind).
				Obj(),
		},
		"child job has managed parent and grandparent and grandparent has a queue-name label": {
			enableIntegrations: []string{"batch/job", "kubeflow.org/mpijob", "workload.codeflare.dev/appwrapper"},
			ancestors: []client.Object{
				testingaw.MakeAppWrapper(grandparentJobName, jobNamespace).
					UID(grandparentJobName).
					Queue("test-q").
					Obj(),
				testingmpijob.MakeMPIJob(parentJobName, jobNamespace).
					UID(parentJobName).
					OwnerReference(grandparentJobName, awv1beta2.GroupVersion.WithKind(awv1beta2.AppWrapperKind)).
					Obj(),
			},
			job: testingjob.MakeJob(childJobName, jobNamespace).
				OwnerReference(parentJobName, kfmpi.SchemeGroupVersionKind).
				Obj(),
			wantManaged: true,
		},
		"child job has managed parent and grandparent and grandparent doesn't have a queue-name label": {
			enableIntegrations: []string{"batch/job", "kubeflow.org/mpijob", "workload.codeflare.dev/appwrapper"},
			ancestors: []client.Object{
				testingaw.MakeAppWrapper(grandparentJobName, jobNamespace).
					UID(grandparentJobName).
					Obj(),
				testingmpijob.MakeMPIJob(parentJobName, jobNamespace).
					UID(parentJobName).
					OwnerReference(grandparentJobName, awv1beta2.GroupVersion.WithKind(awv1beta2.AppWrapperKind)).
					Obj(),
			},
			job: testingjob.MakeJob(childJobName, jobNamespace).
				OwnerReference(parentJobName, kfmpi.SchemeGroupVersionKind).
				Obj(),
			wantManaged: false,
		},
		"cyclic ownership links are properly handled": {
			enableIntegrations: []string{"batch/job", "kubeflow.org/mpijob", "workload.codeflare.dev/appwrapper"},
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
			wantManaged: false,
		},
		"cuts off ancestor traversal at the limit and generates an appropriate event": {
			enableIntegrations: []string{"batch/job", "kubeflow.org/mpijob", "workload.codeflare.dev/appwrapper"},
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
			wantManaged: false,
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: jobNamespace, Name: childJobName},
					EventType: corev1.EventTypeWarning,
					Reason:    ReasonJobNestingTooDeep,
					Message:   "Terminated search for Kueue-managed Job because ancestor depth exceeded limit of 10",
				},
			},
		},
		"pod (enabled) -> statefulset (enabled) -> pod (enabled) -> statefulset (enabled) -> leaderworkerset (enabled) has a queue-name label": {
			enableIntegrations: []string{"pod", "statefulset", "leaderworkerset.x-k8s.io/leaderworkerset"},
			ancestors: []client.Object{
				leaderworkerset.MakeLeaderWorkerSet("lws", jobNamespace).UID("lws").
					Queue("test-q").
					Obj(),
				statefulset.MakeStatefulSet("leader-sts", jobNamespace).UID("leader-sts").
					OwnerReference("lws", leaderworkersetv1.GroupVersion.WithKind("LeaderWorkerSet")).
					Obj(),
				pod.MakePod("pod-leader-sts", jobNamespace).UID("pod-leader-sts").
					OwnerReference("leader-sts", appsv1.SchemeGroupVersion.WithKind("StatefulSet")).
					Obj(),
				statefulset.MakeStatefulSet("worker-sts-0", jobNamespace).UID("worker-sts-0").
					OwnerReference("pod-leader-sts", corev1.SchemeGroupVersion.WithKind("Pod")).
					Obj(),
			},
			job: pod.MakePod("pod-worker-sts-0-1", jobNamespace).UID("pod-worker-sts-0-1").
				OwnerReference("worker-sts-0", appsv1.SchemeGroupVersion.WithKind("StatefulSet")).
				Obj(),
			wantManaged: true,
		},
		"pod (enabled) -> statefulset (enabled) -> pod (enabled) -> statefulset (enabled) -> leaderworkerset (enabled) doesn't have a queue-name label": {
			enableIntegrations: []string{"pod", "statefulset", "leaderworkerset.x-k8s.io/leaderworkerset"},
			ancestors: []client.Object{
				leaderworkerset.MakeLeaderWorkerSet("lws", jobNamespace).UID("lws").
					Obj(),
				statefulset.MakeStatefulSet("leader-sts", jobNamespace).UID("leader-sts").
					OwnerReference("lws", leaderworkersetv1.GroupVersion.WithKind("LeaderWorkerSet")).
					Obj(),
				pod.MakePod("pod-leader-sts", jobNamespace).UID("pod-leader-sts").
					OwnerReference("leader-sts", appsv1.SchemeGroupVersion.WithKind("StatefulSet")).
					Obj(),
				statefulset.MakeStatefulSet("worker-sts-0", jobNamespace).UID("worker-sts-0").
					OwnerReference("pod-leader-sts", corev1.SchemeGroupVersion.WithKind("Pod")).
					Obj(),
			},
			job: pod.MakePod("pod-worker-sts-0-1", jobNamespace).UID("pod-worker-sts-0-1").
				OwnerReference("worker-sts-0", appsv1.SchemeGroupVersion.WithKind("StatefulSet")).
				Obj(),
			wantManaged: false,
		},
		"pod (enabled) -> statefulset (disabled) -> pod (enabled) -> statefulset (disabled) -> leaderworkerset (enabled) doesn't have a queue-name label": {
			enableIntegrations: []string{"pod", "statefulset", "leaderworkerset.x-k8s.io/leaderworkerset"},
			ancestors: []client.Object{
				leaderworkerset.MakeLeaderWorkerSet("lws", jobNamespace).UID("lws").
					Obj(),
				statefulset.MakeStatefulSet("leader-sts", jobNamespace).UID("leader-sts").
					OwnerReference("lws", leaderworkersetv1.GroupVersion.WithKind("LeaderWorkerSet")).
					Obj(),
				pod.MakePod("pod-leader-sts", jobNamespace).UID("pod-leader-sts").
					OwnerReference("leader-sts", appsv1.SchemeGroupVersion.WithKind("StatefulSet")).
					Obj(),
				statefulset.MakeStatefulSet("worker-sts-0", jobNamespace).UID("worker-sts-0").
					OwnerReference("pod-leader-sts", corev1.SchemeGroupVersion.WithKind("Pod")).
					Obj(),
			},
			job: pod.MakePod("pod-worker-sts-0-1", jobNamespace).UID("pod-worker-sts-0-1").
				OwnerReference("worker-sts-0", appsv1.SchemeGroupVersion.WithKind("StatefulSet")).
				Obj(),
			wantManaged: false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Cleanup(EnableIntegrationsForTest(t, tc.enableIntegrations...))
			ctx, _ := utiltesting.ContextWithLog(t)
			recorder := &utiltesting.EventRecorder{}
			builder := utiltesting.NewClientBuilder(kfmpi.AddToScheme, awv1beta2.AddToScheme, leaderworkersetv1.AddToScheme)
			builder = builder.WithObjects(tc.ancestors...)
			if tc.job != nil {
				builder = builder.WithObjects(tc.job)
			}
			cl := builder.Build()
			got, gotErr := IsAncestorJobManaged(ctx, cl, recorder, tc.job)
			if tc.wantManaged != got {
				t.Errorf("Unexpected response from IsAncestorJobManaged want: %v,got: %v", tc.wantManaged, got)
			}
			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.EquateErrors()); len(diff) != 0 {
				t.Errorf("Unexpected error (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantEvents, recorder.RecordedEvents); diff != "" {
				t.Errorf("unexpected events (-want/+got):\n%s", diff)
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
