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
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	kfmpi "github.com/kubeflow/mpi-operator/pkg/apis/kubeflow/v2beta1"
	awv1beta2 "github.com/project-codeflare/appwrapper/api/v1beta2"
	"go.uber.org/mock/gomock"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/clock"
	testingclock "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/jobset/api/jobset/v1alpha2"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	mocks "sigs.k8s.io/kueue/internal/mocks/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/kubeversion"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingaw "sigs.k8s.io/kueue/pkg/util/testingjobs/appwrapper"
	testingdeployment "sigs.k8s.io/kueue/pkg/util/testingjobs/deployment"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/pkg/util/testingjobs/jobset"
	testingmpijob "sigs.k8s.io/kueue/pkg/util/testingjobs/mpijob"
	"sigs.k8s.io/kueue/pkg/workloadslicing"

	_ "sigs.k8s.io/kueue/pkg/controller/jobs"

	. "sigs.k8s.io/kueue/pkg/controller/jobframework"
)

func TestReconcileGenericJob(t *testing.T) {
	var (
		testJobName        = "test-job"
		testLocalQueueName = kueue.LocalQueueName("test-lq")
		testGVK            = batchv1.SchemeGroupVersion.WithKind("Job")
	)

	baseReq := types.NamespacedName{Name: testJobName, Namespace: metav1.NamespaceDefault}
	baseJob := testingjob.MakeJob(testJobName, metav1.NamespaceDefault).UID(testJobName).Queue(testLocalQueueName)
	basePodSets := []kueue.PodSet{
		*utiltestingapi.MakePodSet("main", 1).Obj(),
	}
	baseWl := utiltestingapi.MakeWorkload("job-test-job", metav1.NamespaceDefault).
		ResourceVersion("1").
		Finalizers(kueue.ResourceInUseFinalizerName).
		Label(constants.JobUIDLabel, testJobName).
		ControllerReference(testGVK, testJobName, testJobName).
		Queue(testLocalQueueName).
		PodSets(basePodSets...).
		Priority(0)

	testCases := map[string]struct {
		elasticJobsViaWorkloadSlicesEnabled bool
		req                                 types.NamespacedName
		job                                 *batchv1.Job
		podSets                             []kueue.PodSet
		objs                                []client.Object
		wantWorkloads                       []kueue.Workload
	}{
		"handle job with no workload (elasticJobsViaWorkloadSlicesEnabled = false)": {
			elasticJobsViaWorkloadSlicesEnabled: false,
			req:                                 baseReq,
			job:                                 baseJob.DeepCopy(),
			podSets:                             basePodSets,
			wantWorkloads: []kueue.Workload{
				*baseWl.Clone().Name("job-test-job-ce737").Obj(),
			},
		},
		"handle job with no workload (elasticJobsViaWorkloadSlicesEnabled = false and elastic job annotation)": {
			elasticJobsViaWorkloadSlicesEnabled: false,
			req:                                 baseReq,
			job: baseJob.Clone().
				SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				Obj(),
			podSets: basePodSets,
			wantWorkloads: []kueue.Workload{
				*baseWl.Clone().Name("job-test-job-ce737").Obj(),
			},
		},
		"handle job with no workload (elasticJobsViaWorkloadSlicesEnabled = true)": {
			elasticJobsViaWorkloadSlicesEnabled: true,
			req:                                 baseReq,
			job:                                 baseJob.DeepCopy(),
			podSets:                             basePodSets,
			wantWorkloads: []kueue.Workload{
				*baseWl.Clone().Name("job-test-job-ce737").Obj(),
			},
		},
		"handle job with no workload (elasticJobsViaWorkloadSlicesEnabled = true and elastic job annotation)": {
			elasticJobsViaWorkloadSlicesEnabled: true,
			req:                                 baseReq,
			job: baseJob.Clone().
				SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				Obj(),
			podSets: basePodSets,
			wantWorkloads: []kueue.Workload{
				*baseWl.Clone().Name("job-test-job-3991b").
					Annotations(map[string]string{workloadslicing.EnabledAnnotationKey: workloadslicing.EnabledAnnotationValue}).
					Obj(),
			},
		},
		"update workload to match job (one existing workload)": {
			elasticJobsViaWorkloadSlicesEnabled: true,
			req:                                 baseReq,
			job: baseJob.Clone().
				SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				Obj(),
			podSets: basePodSets,
			objs: []client.Object{
				baseWl.Clone().Name("job-test-job-1").
					PodSets(*utiltestingapi.MakePodSet("old", 2).Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*baseWl.Clone().Name("job-test-job-1").ResourceVersion("2").Obj(),
			},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlices, tc.elasticJobsViaWorkloadSlicesEnabled)

			ctx, _ := utiltesting.ContextWithLog(t)
			mockctrl := gomock.NewController(t)

			mgj := mocks.NewMockGenericJob(mockctrl)
			mgj.EXPECT().Object().Return(tc.job).AnyTimes()
			mgj.EXPECT().GVK().Return(testGVK).AnyTimes()
			mgj.EXPECT().IsSuspended().Return(ptr.Deref(tc.job.Spec.Suspend, false)).AnyTimes()
			mgj.EXPECT().IsActive().Return(tc.job.Status.Active != 0).AnyTimes()
			mgj.EXPECT().Finished(gomock.Any()).Return("", false, false).AnyTimes()
			mgj.EXPECT().PodSets(gomock.Any()).Return(tc.podSets, nil).AnyTimes()

			cl := utiltesting.NewClientBuilder(batchv1.AddToScheme, kueue.AddToScheme).
				WithObjects(utiltesting.MakeNamespace(tc.req.Namespace)).
				WithObjects(tc.objs...).
				WithObjects(tc.job).
				WithIndex(&kueue.Workload{}, indexer.OwnerReferenceIndexKey(testGVK), indexer.WorkloadOwnerIndexFunc(testGVK)).
				Build()

			recorder := &utiltesting.EventRecorder{}
			rec := NewReconciler(cl, recorder)
			_, err := rec.ReconcileGenericJob(ctx, controllerruntime.Request{NamespacedName: tc.req}, mgj)
			if err != nil {
				t.Fatalf("Failed to Reconcile GenericJob: %v", err)
			}

			wls := kueue.WorkloadList{}
			err = cl.List(ctx, &wls)
			if err != nil {
				t.Fatalf("Failed to List workloads: %v", err)
			}

			if diff := cmp.Diff(wls.Items, tc.wantWorkloads, cmpopts.IgnoreFields(corev1.ResourceRequirements{}, "Requests")); diff != "" {
				t.Errorf("Workloads mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestReconcileGenericJobWithCustomWorkloadActivation(t *testing.T) {
	const (
		testJobName = "test-job"
		testNS      = metav1.NamespaceDefault
	)

	var (
		testLocalQueueName = kueue.LocalQueueName("test-lq")
		testGVK            = batchv1.SchemeGroupVersion.WithKind("Job")
		req                = types.NamespacedName{Name: testJobName, Namespace: testNS}
	)

	baseJob := testingjob.MakeJob(testJobName, testNS).UID(testJobName).Queue(testLocalQueueName)
	basePodSets := []kueue.PodSet{
		*utiltestingapi.MakePodSet("main", 1).Obj(),
	}
	baseWl := utiltestingapi.MakeWorkload("job-test-job", testNS).
		ResourceVersion("1").
		Finalizers(kueue.ResourceInUseFinalizerName).
		Label(constants.JobUIDLabel, testJobName).
		ControllerReference(testGVK, testJobName, testJobName).
		Queue(testLocalQueueName).
		PodSets(basePodSets...).
		Priority(0)

	testCases := map[string]struct {
		initialActive  *bool
		jobActive      bool
		expectedActive bool
	}{
		"marks workload inactive when job requests": {
			initialActive:  nil,
			jobActive:      false,
			expectedActive: false,
		},
		"marks workload active when job requests": {
			initialActive:  ptr.To(false),
			jobActive:      true,
			expectedActive: true,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			mockctrl := gomock.NewController(t)

			job := baseJob.DeepCopy()
			wl := baseWl.Clone().Name("job-test-job-1").Obj()
			if tc.initialActive == nil {
				wl.Spec.Active = nil
			} else {
				wl.Spec.Active = ptr.To(*tc.initialActive)
			}

			cl := utiltesting.NewClientBuilder(batchv1.AddToScheme, kueue.AddToScheme).
				WithObjects(utiltesting.MakeNamespace(testNS)).
				WithObjects(job, wl).
				WithIndex(&kueue.Workload{}, indexer.OwnerReferenceIndexKey(testGVK), indexer.WorkloadOwnerIndexFunc(testGVK)).
				Build()

			recorder := &utiltesting.EventRecorder{}
			reconciler := NewReconciler(cl, recorder)

			mgj := &struct {
				*mocks.MockGenericJob
				*mocks.MockJobWithCustomWorkloadActivation
			}{
				MockGenericJob:                      mocks.NewMockGenericJob(mockctrl),
				MockJobWithCustomWorkloadActivation: mocks.NewMockJobWithCustomWorkloadActivation(mockctrl),
			}
			mgj.MockGenericJob.EXPECT().Object().Return(job).AnyTimes()
			mgj.MockGenericJob.EXPECT().GVK().Return(testGVK).AnyTimes()
			mgj.MockGenericJob.EXPECT().IsSuspended().Return(ptr.Deref(job.Spec.Suspend, false)).AnyTimes()
			mgj.MockGenericJob.EXPECT().Finished(gomock.Any()).Return("", false, false).AnyTimes()
			mgj.MockGenericJob.EXPECT().PodSets(gomock.Any()).Return(basePodSets, nil).AnyTimes()
			mgj.MockJobWithCustomWorkloadActivation.EXPECT().IsWorkloadActive().Return(tc.jobActive).MaxTimes(1)

			if _, err := reconciler.ReconcileGenericJob(ctx, controllerruntime.Request{NamespacedName: req}, mgj); err != nil {
				t.Fatalf("Failed to Reconcile GenericJob: %v", err)
			}

			updated := &kueue.Workload{}
			if err := cl.Get(ctx, client.ObjectKey{Name: wl.Name, Namespace: wl.Namespace}, updated); err != nil {
				t.Fatalf("Failed to get workload: %v", err)
			}

			if updated.Spec.Active == nil {
				t.Fatalf("Workload.Spec.Active is nil, want %t", tc.expectedActive)
			}
			if *updated.Spec.Active != tc.expectedActive {
				t.Fatalf("Workload.Spec.Active = %t, want %t", *updated.Spec.Active, tc.expectedActive)
			}
		})
	}
}

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
		externalFrameworks         []string
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
		"Job -> CronJob (external framework, not enabled) -> AppWrapper (queue-name) => AppWrapper": {
			integrations: []string{"workload.codeflare.dev/appwrapper"},
			ancestors: []client.Object{
				testingaw.MakeAppWrapper("aw", jobNamespace).UID("aw").Queue("test-q").Obj(),
				&batchv1.CronJob{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "cronjob",
						Namespace: jobNamespace,
						OwnerReferences: []metav1.OwnerReference{{
							Name:       "aw",
							APIVersion: awv1beta2.GroupVersion.String(),
							Kind:       awv1beta2.AppWrapperKind,
							UID:        "aw",
							Controller: ptr.To(true),
						}},
					},
				},
			},
			job: testingjob.MakeJob("job", jobNamespace).UID("job").
				OwnerReference("cronjob", batchv1.SchemeGroupVersion.WithKind("CronJob")).
				Obj(),
		},
		"Job -> CronJob (external framework, enabled) -> AppWrapper (queue-name) => AppWrapper": {
			integrations:       []string{"workload.codeflare.dev/appwrapper"},
			externalFrameworks: []string{"CronJob.v1.batch"},
			ancestors: []client.Object{
				testingaw.MakeAppWrapper("aw", jobNamespace).UID("aw").Queue("test-q").Obj(),
				&batchv1.CronJob{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "cronjob",
						Namespace: jobNamespace,
						OwnerReferences: []metav1.OwnerReference{{
							Name:       "aw",
							APIVersion: awv1beta2.GroupVersion.String(),
							Kind:       awv1beta2.AppWrapperKind,
							UID:        "aw",
							Controller: ptr.To(true),
						}},
					},
				},
			},
			job: testingjob.MakeJob("job", jobNamespace).UID("job").
				OwnerReference("cronjob", batchv1.SchemeGroupVersion.WithKind("CronJob")).
				Obj(),
			wantManaged: testingaw.MakeAppWrapper("aw", jobNamespace).UID("aw").Queue("test-q").Obj(),
		},
		"Pod -> ReplicaSet -> Deployment (queue-name) => Deployment": {
			integrations: []string{"pod", "deployment"},
			ancestors: []client.Object{
				testingdeployment.MakeDeployment("deploy", jobNamespace).UID("deploy").Queue("test-q").Obj(),
				&appsv1.ReplicaSet{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "rs",
						Namespace: jobNamespace,
						OwnerReferences: []metav1.OwnerReference{{
							Name:       "deploy",
							APIVersion: appsv1.SchemeGroupVersion.String(),
							Kind:       "Deployment",
							UID:        "deploy",
							Controller: ptr.To(true),
						}},
					},
				},
			},
			job: testingjob.MakeJob("pod", jobNamespace).UID("pod").
				OwnerReference("rs", appsv1.SchemeGroupVersion.WithKind("ReplicaSet")).
				Obj(),
			wantManaged: testingdeployment.MakeDeployment("deploy", jobNamespace).UID("deploy").Queue("test-q").Obj(),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Cleanup(EnableIntegrationsForTest(t, tc.integrations...))
			t.Cleanup(EnableExternalIntegrationsForTest(t, tc.externalFrameworks...))
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
				WithLabelKeysToCopy([]string{"toCopyKey"}),
				WithClock(t, fakeClock),
			},
			wantOpts: Options{
				ManageJobsWithoutQueueName: true,
				WaitForPodsReady:           true,
				KubeServerVersion:          &kubeversion.ServerVersionFetcher{},
				IntegrationOptions:         nil,
				LabelKeysToCopy:            []string{"toCopyKey"},
				Clock:                      fakeClock,
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

func TestReconcileGenericJobWithWaitForPodsReady(t *testing.T) {
	var (
		testLocalQueueName = kueue.LocalQueueName("default")
		testGVK            = batchv1.SchemeGroupVersion.WithKind("Job")
	)
	testCases := map[string]struct {
		workload  *kueue.Workload
		job       GenericJob
		wantError error
	}{
		"update podready condition failed": {
			workload: utiltestingapi.MakeWorkload("job-test-job-podready-fail", metav1.NamespaceDefault).
				Finalizers(kueue.ResourceInUseFinalizerName).
				Label(constants.JobUIDLabel, "test-job-podready-fail").
				ControllerReference(testGVK, "test-job-podready-fail", "test-job-podready-fail").
				Queue(testLocalQueueName).
				PodSets(*utiltestingapi.MakePodSet("main", 1).Obj()).
				Conditions(metav1.Condition{
					Type:               kueue.WorkloadAdmitted,
					Status:             metav1.ConditionTrue,
					Reason:             "Admitted",
					Message:            "The workload is admitted",
					LastTransitionTime: metav1.NewTime(time.Now()),
				}, metav1.Condition{
					Type:               kueue.WorkloadPodsReady,
					Status:             metav1.ConditionFalse,
					Reason:             kueue.WorkloadWaitForStart,
					Message:            "Not all pods are ready or succeeded",
					LastTransitionTime: metav1.NewTime(time.Now()),
				}).
				Admission(&kueue.Admission{
					ClusterQueue: "default-cq",
				}).
				Obj(),
			job: (*job.Job)(testingjob.MakeJob("test-job-podready-fail", metav1.NamespaceDefault).
				UID("test-job-podready-fail").
				Label(constants.QueueLabel, string(testLocalQueueName)).
				Parallelism(1).
				Suspend(false).
				Containers(corev1.Container{
					Name: "c",
					Resources: corev1.ResourceRequirements{
						Requests: make(corev1.ResourceList),
					},
				}).
				Ready(1).
				Obj()),
			wantError: apierrors.NewInternalError(errors.New("failed calling webhook")),
		},
		"update podready condition success": {
			workload: utiltestingapi.MakeWorkload("job-test-job-podready-success", metav1.NamespaceDefault).
				Finalizers(kueue.ResourceInUseFinalizerName).
				Label(constants.JobUIDLabel, "job-test-job-podready-success").
				ControllerReference(testGVK, "test-job-podready-success", "test-job-podready-success").
				Queue(testLocalQueueName).
				PodSets(*utiltestingapi.MakePodSet("main", 1).Obj()).
				Conditions(metav1.Condition{
					Type:               kueue.WorkloadAdmitted,
					Status:             metav1.ConditionTrue,
					Reason:             "Admitted",
					Message:            "The workload is admitted",
					LastTransitionTime: metav1.NewTime(time.Now()),
				}, metav1.Condition{
					Type:               kueue.WorkloadPodsReady,
					Status:             metav1.ConditionFalse,
					Reason:             kueue.WorkloadWaitForStart,
					Message:            "Not all pods are ready or succeeded",
					LastTransitionTime: metav1.NewTime(time.Now()),
				}).
				Admission(&kueue.Admission{
					ClusterQueue: "default-cq",
				}).
				Obj(),
			job: (*job.Job)(testingjob.MakeJob("test-job-podready-success", metav1.NamespaceDefault).
				UID("test-job-podready-success").
				Label(constants.QueueLabel, string(testLocalQueueName)).
				Parallelism(1).
				Suspend(false).
				Containers(corev1.Container{
					Name: "c",
					Resources: corev1.ResourceRequirements{
						Requests: make(corev1.ResourceList),
					},
				}).
				Ready(1).
				Obj()),
			wantError: nil,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			managedNamespace := utiltesting.MakeNamespaceWrapper(metav1.NamespaceDefault).
				Label("managed-by-kueue", "true").
				Obj()
			builder := utiltesting.NewClientBuilder(batchv1.AddToScheme, kueue.AddToScheme).
				WithObjects(tc.workload, tc.job.Object(), managedNamespace).
				WithStatusSubresource(tc.workload, tc.job.Object()).
				WithIndex(&kueue.Workload{}, indexer.OwnerReferenceIndexKey(testGVK), indexer.WorkloadOwnerIndexFunc(testGVK)).
				WithInterceptorFuncs(interceptor.Funcs{
					SubResourcePatch: func(ctx context.Context, client client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
						if _, ok := obj.(*kueue.Workload); ok && subResourceName == "status" && tc.wantError != nil {
							return tc.wantError
						}
						return utiltesting.TreatSSAAsStrategicMerge(ctx, client, subResourceName, obj, patch, opts...)
					},
				})

			cl := builder.Build()

			testStartTime := time.Now().Truncate(time.Second)

			fakeClock := testingclock.NewFakeClock(testStartTime)
			options := []Option{
				WithClock(nil, fakeClock),
				WithWaitForPodsReady(&configapi.WaitForPodsReady{
					Enable: true,
				}),
			}
			recorder := &utiltesting.EventRecorder{}
			r := NewReconciler(cl, recorder, options...)
			_, err := r.ReconcileGenericJob(ctx, controllerruntime.Request{
				NamespacedName: types.NamespacedName{
					Name:      tc.job.Object().GetName(),
					Namespace: tc.job.Object().GetNamespace(),
				}}, tc.job)
			if !errors.Is(err, tc.wantError) {
				t.Errorf("unexpected reconcile error want %s got %s)", tc.wantError, err)
			}
		})
	}
}
