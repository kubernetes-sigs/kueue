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
	"fmt"
	"reflect"
	"slices"
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
	schedulingv1 "k8s.io/api/scheduling/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/component-base/featuregate"
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
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	kueueconstants "sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/controller/jobs"
	"sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/podset"
	"sigs.k8s.io/kueue/pkg/util/kubeversion"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingaw "sigs.k8s.io/kueue/pkg/util/testingjobs/appwrapper"
	testingdeployment "sigs.k8s.io/kueue/pkg/util/testingjobs/deployment"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/pkg/util/testingjobs/jobset"
	testingmpijob "sigs.k8s.io/kueue/pkg/util/testingjobs/mpijob"
	"sigs.k8s.io/kueue/pkg/workloadslicing"

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
	// No pod set assignments, so equivalence compares against the workload spec.
	reservedIn := &kueue.Admission{ClusterQueue: "cq"}
	reservedAt := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)

	testCases := map[string]struct {
		featureGates      map[featuregate.Feature]bool
		reconcilerOptions []Option
		req               types.NamespacedName
		job               *batchv1.Job
		podSets           []kueue.PodSet
		objs              []client.Object
		wantWorkloads     []kueue.Workload
		wantEvents        []utiltesting.EventRecord
		wantPodSets       []podset.PodSetInfo
	}{
		"handle job with no workload (elasticJobsViaWorkloadSlicesEnabled = false)": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: false},
			req:          baseReq,
			job:          baseJob.DeepCopy(),
			podSets:      basePodSets,
			wantWorkloads: []kueue.Workload{
				*baseWl.Clone().Name("job-test-job-ce737").Obj(),
			},
		},
		"handle job with no workload (elasticJobsViaWorkloadSlicesEnabled = false and elastic job annotation)": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: false},
			req:          baseReq,
			job: baseJob.Clone().
				SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				Obj(),
			podSets: basePodSets,
			wantWorkloads: []kueue.Workload{
				*baseWl.Clone().Name("job-test-job-ce737").Obj(),
			},
		},
		"handle job with no workload (elasticJobsViaWorkloadSlicesEnabled = true)": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			req:          baseReq,
			job:          baseJob.DeepCopy(),
			podSets:      basePodSets,
			wantWorkloads: []kueue.Workload{
				*baseWl.Clone().Name("job-test-job-ce737").Obj(),
			},
		},
		"handle job with no workload (elasticJobsViaWorkloadSlicesEnabled = true and elastic job annotation)": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			req:          baseReq,
			job: baseJob.Clone().
				SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				Obj(),
			podSets: basePodSets,
			wantWorkloads: []kueue.Workload{
				*baseWl.Clone().Name("job-test-job-3991b").
					Annotations(map[string]string{
						workloadslicing.EnabledAnnotationKey: workloadslicing.EnabledAnnotationValue,
						kueue.WorkloadSliceNameAnnotation:    "job-test-job-3991b",
					}).
					Obj(),
			},
		},
		"update workload to match job (one existing workload)": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			req:          baseReq,
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
		"update workload to match job preserves active=true": {
			req:     baseReq,
			job:     baseJob.DeepCopy(),
			podSets: basePodSets,
			objs: []client.Object{
				baseWl.Clone().Name("job-test-job-1").
					PodSets(*utiltestingapi.MakePodSet("old", 2).Obj()).
					Active(true).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*baseWl.Clone().Name("job-test-job-1").ResourceVersion("2").Active(true).Obj(),
			},
		},
		"update workload to match job preserves active=false": {
			req:     baseReq,
			job:     baseJob.DeepCopy(),
			podSets: basePodSets,
			objs: []client.Object{
				baseWl.Clone().Name("job-test-job-1").
					PodSets(*utiltestingapi.MakePodSet("old", 2).Obj()).
					Active(false).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*baseWl.Clone().Name("job-test-job-1").ResourceVersion("2").Active(false).Obj(),
			},
		},
		"update workload to match job with changed parallelism preserves active=false": {
			req: baseReq,
			job: baseJob.Clone().
				Parallelism(5).
				Obj(),
			podSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet("main", 5).Obj(),
			},
			objs: []client.Object{
				baseWl.Clone().Name("job-test-job-1").
					PodSets(*utiltestingapi.MakePodSet("main", 1).Obj()).
					Active(false).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*baseWl.Clone().Name("job-test-job-1").
					ResourceVersion("2").
					PodSets(*utiltestingapi.MakePodSet("main", 5).Obj()).
					Active(false).
					Obj(),
			},
		},
		"job with AdmissionGatedBy annotation should create workload with annotation": {
			featureGates: map[featuregate.Feature]bool{features.AdmissionGatedBy: true},
			req:          baseReq,
			job: baseJob.Clone().
				SetAnnotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1").
				Obj(),
			podSets: basePodSets,
			wantWorkloads: []kueue.Workload{
				*baseWl.Clone().
					Name("job-test-job-ce737").
					Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1").
					Obj(),
			},
		},
		"job with AdmissionGatedBy annotation removed should update workload": {
			featureGates: map[featuregate.Feature]bool{features.AdmissionGatedBy: true},
			req:          baseReq,
			job:          baseJob.DeepCopy(),
			podSets:      basePodSets,
			objs: []client.Object{
				baseWl.Clone().Name("job-test-job-1").
					Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*baseWl.Clone().Name("job-test-job-1").ResourceVersion("2").Obj(),
			},
		},
		"job with multiple AdmissionGatedBy gates should create workload with annotation": {
			featureGates: map[featuregate.Feature]bool{features.AdmissionGatedBy: true},
			req:          baseReq,
			job: baseJob.Clone().
				SetAnnotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1,example.com/controller2").
				Obj(),
			podSets: basePodSets,
			wantWorkloads: []kueue.Workload{
				*baseWl.Clone().
					Name("job-test-job-ce737").
					Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1,example.com/controller2").
					Obj(),
			},
		},
		"job with AdmissionGatedBy annotation when feature gate disabled should not propagate annotation": {
			featureGates: map[featuregate.Feature]bool{features.AdmissionGatedBy: false},
			req:          baseReq,
			job: baseJob.Clone().
				SetAnnotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1").
				Obj(),
			podSets: basePodSets,
			wantWorkloads: []kueue.Workload{
				*baseWl.Clone().
					Name("job-test-job-ce737").
					Obj(),
			},
		},
		"job with AdmissionGatedBy annotation unchanged should not emit event": {
			featureGates: map[featuregate.Feature]bool{features.AdmissionGatedBy: true},
			req:          baseReq,
			job: baseJob.Clone().
				SetAnnotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1").
				Obj(),
			podSets: basePodSets,
			objs: []client.Object{
				baseWl.Clone().Name("job-test-job-1").
					Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*baseWl.Clone().Name("job-test-job-1").
					Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1").
					Obj(),
			},
			wantEvents: nil,
		},
		"job with AdmissionGatedBy annotation changed should emit event": {
			featureGates: map[featuregate.Feature]bool{features.AdmissionGatedBy: true},
			req:          baseReq,
			job: baseJob.Clone().
				SetAnnotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1,example.com/controller2").
				Obj(),
			podSets: basePodSets,
			objs: []client.Object{
				baseWl.Clone().Name("job-test-job-1").
					Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*baseWl.Clone().Name("job-test-job-1").ResourceVersion("2").
					Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1,example.com/controller2").
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: testJobName, Namespace: metav1.NamespaceDefault},
					EventType: corev1.EventTypeNormal,
					Reason:    ReasonUpdatedWorkload,
					Message:   `Updated workload AdmissionGatedBy to "example.com/controller1,example.com/controller2"`,
				},
			},
		},
		"job with AdmissionGatedBy annotation changed when feature disabled should not emit event": {
			featureGates: map[featuregate.Feature]bool{features.AdmissionGatedBy: false},
			req:          baseReq,
			job: baseJob.Clone().
				SetAnnotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1,example.com/controller2").
				Obj(),
			podSets: basePodSets,
			objs: []client.Object{
				baseWl.Clone().Name("job-test-job-1").
					Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*baseWl.Clone().Name("job-test-job-1").
					Annotation(kueueconstants.AdmissionGatedByAnnotation, "example.com/controller1").
					Obj(),
			},
			wantEvents: nil,
		},
		"MultiKueue worker pod label is propagated to PodTemplate if workload has Multikueue origin label": {
			req: baseReq,
			job: baseJob.Clone().Label(kueue.MultiKueueOriginLabel, "origin").
				PrebuiltWorkloadLabel("job-test-job-1").
				Obj(),
			podSets: basePodSets,
			objs: []client.Object{
				baseWl.Clone().Name("job-test-job-1").
					Label(kueue.MultiKueueOriginLabel, "origin").
					Conditions(metav1.Condition{
						Type:   kueue.WorkloadQuotaReserved,
						Status: metav1.ConditionTrue,
					}, metav1.Condition{
						Type:   kueue.WorkloadAdmitted,
						Status: metav1.ConditionTrue,
					}).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").PodSets(
						kueue.PodSetAssignment{
							Name: "main",
						},
					).Obj(), time.Now().Truncate(time.Hour)).
					AdmittedAt(true, time.Now().Truncate(time.Hour)).
					Admission(&kueue.Admission{
						ClusterQueue: "default-cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							utiltestingapi.MakePodSetAssignment("main").
								Obj(),
						},
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*baseWl.Clone().Name("job-test-job-1").
					Label(kueue.MultiKueueOriginLabel, "origin").
					Conditions(metav1.Condition{
						Type:   kueue.WorkloadQuotaReserved,
						Status: metav1.ConditionTrue,
					}, metav1.Condition{
						Type:   kueue.WorkloadAdmitted,
						Status: metav1.ConditionTrue,
					}).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").PodSets(
						kueue.PodSetAssignment{
							Name: "main",
						},
					).Obj(), time.Now().Truncate(time.Hour)).
					AdmittedAt(true, time.Now().Truncate(time.Hour)).
					Admission(&kueue.Admission{
						ClusterQueue: "default-cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:          "main",
								Flavors:       nil,
								ResourceUsage: nil,
								Count:         new(int32(1)),
							},
						},
					}).
					Obj(),
			},
			wantPodSets: []podset.PodSetInfo{
				{
					Name:  "main",
					Count: 1,
					Annotations: map[string]string{
						kueue.WorkloadAnnotation: "job-test-job-1",
					},
					Labels: map[string]string{
						kueueconstants.ClusterQueueLabel:       "default-cq",
						kueueconstants.LocalQueueLabel:         "test-lq",
						kueue.MultiKueueWorkerWorkloadPodLabel: kueue.MultiKueueWorkerWorkloadPodValue,
						kueueconstants.PodSetLabel:             "main",
					},
					Affinity:        nil,
					NodeSelector:    map[string]string{},
					Tolerations:     nil,
					SchedulingGates: nil,
				},
			},
		},
		"handle job with annotations to copy": {
			featureGates: map[featuregate.Feature]bool{
				features.CustomMetricLabels: true,
			},
			reconcilerOptions: []Option{
				WithLabelKeysToCopy(sets.New("toCopyKey")),
				WithAnnotationsToCopy(sets.New("toCopyAnnotation")),
			},
			req: baseReq,
			job: baseJob.Clone().
				Label("toCopyKey", "toCopyValue").
				Label("dontCopyKey", "dontCopyValue").
				SetAnnotation("toCopyAnnotation", "toCopyAnnValue").
				SetAnnotation("dontCopyAnnotation", "dontCopyAnnValue").
				Obj(),
			podSets: basePodSets,
			wantWorkloads: []kueue.Workload{
				*baseWl.Clone().
					Name("job-test-job-ce737").
					Label("toCopyKey", "toCopyValue").
					Annotations(map[string]string{"toCopyAnnotation": "toCopyAnnValue"}).
					Obj(),
			},
		},
		"setup workload annotations for pods": {
			featureGates: map[featuregate.Feature]bool{
				features.SchedulerLibraryIntegration: true,
				features.TopologyAwareScheduling:     false,
			},
			req:     baseReq,
			job:     baseJob.Clone().Obj(),
			podSets: basePodSets,
			objs: []client.Object{
				baseWl.Clone().Name("job-test-job-1").
					Conditions(metav1.Condition{
						Type:   kueue.WorkloadQuotaReserved,
						Status: metav1.ConditionTrue,
					}, metav1.Condition{
						Type:   kueue.WorkloadAdmitted,
						Status: metav1.ConditionTrue,
					}).
					Admission(&kueue.Admission{
						ClusterQueue: "default-cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  "main",
								Count: new(int32(1)),
							},
						},
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*baseWl.Clone().Name("job-test-job-1").
					Conditions(metav1.Condition{
						Type:   kueue.WorkloadQuotaReserved,
						Status: metav1.ConditionTrue,
					}, metav1.Condition{
						Type:   kueue.WorkloadAdmitted,
						Status: metav1.ConditionTrue,
					}).
					Admission(&kueue.Admission{
						ClusterQueue: "default-cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  "main",
								Count: new(int32(1)),
							},
						},
					}).
					Obj(),
			},
			wantPodSets: []podset.PodSetInfo{
				{
					Name:  "main",
					Count: 1,
					Annotations: map[string]string{
						kueue.WorkloadAnnotation: "job-test-job-1",
					},
					Labels: map[string]string{
						kueueconstants.ClusterQueueLabel: "default-cq",
						kueueconstants.LocalQueueLabel:   "test-lq",
						kueueconstants.PodSetLabel:       "main",
					},
					NodeSelector: map[string]string{},
				},
			},
		},
		// Group and kind are frozen while quota is reserved.
		"quota-reserved workload is not moved onto a pod priority class": {
			req: baseReq,
			job: baseJob.DeepCopy(),
			podSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet("main", 1).PriorityClass("podpc").Obj(),
			},
			objs: []client.Object{
				&schedulingv1.PriorityClass{ObjectMeta: metav1.ObjectMeta{Name: "podpc"}, Value: 50},
				baseWl.Clone().Name("job-test-job-1").
					PodSets(*utiltestingapi.MakePodSet("main", 1).PriorityClass("podpc").Obj()).
					WorkloadPriorityClassRef("high").Priority(100).
					ReserveQuotaAt(reservedIn, reservedAt).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*baseWl.Clone().Name("job-test-job-1").
					PodSets(*utiltestingapi.MakePodSet("main", 1).PriorityClass("podpc").Obj()).
					WorkloadPriorityClassRef("high").Priority(100).
					ReserveQuotaAt(reservedIn, reservedAt).
					Obj(),
			},
		},
		// A nil resolved ref is a removal, frozen while quota is reserved.
		"quota-reserved workload keeps its priority class when the owner's stops resolving": {
			req:     baseReq,
			job:     baseJob.DeepCopy(),
			podSets: basePodSets,
			objs: []client.Object{
				baseWl.Clone().Name("job-test-job-1").
					WorkloadPriorityClassRef("high").Priority(100).
					ReserveQuotaAt(reservedIn, reservedAt).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*baseWl.Clone().Name("job-test-job-1").
					WorkloadPriorityClassRef("high").Priority(100).
					ReserveQuotaAt(reservedIn, reservedAt).
					Obj(),
			},
		},
		// Same group and kind, so the rename is legal while reserved.
		"quota-reserved workload follows the owner to another workload priority class": {
			req:     baseReq,
			job:     baseJob.Clone().WorkloadPriorityClass("low").Obj(),
			podSets: basePodSets,
			objs: []client.Object{
				utiltestingapi.MakeWorkloadPriorityClass("low").PriorityValue(10).Obj(),
				baseWl.Clone().Name("job-test-job-1").
					WorkloadPriorityClassRef("high").Priority(100).
					ReserveQuotaAt(reservedIn, reservedAt).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*baseWl.Clone().Name("job-test-job-1").ResourceVersion("2").
					WorkloadPriorityClassRef("low").Priority(10).
					ReserveQuotaAt(reservedIn, reservedAt).
					Obj(),
			},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)

			ctx, _ := utiltesting.ContextWithLog(t)
			mockctrl := gomock.NewController(t)

			mgj := mocks.NewMockGenericJob(mockctrl)
			mgj.EXPECT().Object().Return(tc.job).AnyTimes()
			mgj.EXPECT().GVK().Return(testGVK).AnyTimes()
			mgj.EXPECT().IsSuspended().Return(ptr.Deref(tc.job.Spec.Suspend, false)).AnyTimes()
			mgj.EXPECT().IsActive().Return(tc.job.Status.Active != 0).AnyTimes()
			mgj.EXPECT().RunWithPodSetsInfo(gomock.Any(), gomock.Any(), tc.wantPodSets).Return(nil).AnyTimes()
			mgj.EXPECT().Finished(gomock.Any()).Return("", false, false).AnyTimes()
			mgj.EXPECT().PodSets(gomock.Any(), gomock.Any()).Return(tc.podSets, nil).AnyTimes()

			cl := utiltesting.NewClientBuilder(batchv1.AddToScheme, kueue.AddToScheme).
				WithObjects(utiltesting.MakeNamespace(tc.req.Namespace)).
				WithObjects(tc.objs...).
				WithObjects(tc.job).
				WithIndex(&kueue.Workload{}, indexer.OwnerReferenceIndexKey(testGVK), indexer.WorkloadOwnerIndexFunc(testGVK)).
				Build()

			recorder := &utiltesting.EventRecorder{}
			rec := NewReconciler(cl, recorder, tc.reconcilerOptions...)
			_, err := rec.ReconcileGenericJob(ctx, controllerruntime.Request{NamespacedName: tc.req}, mgj)
			if err != nil {
				t.Fatalf("Failed to Reconcile GenericJob: %v", err)
			}

			wls := kueue.WorkloadList{}
			err = cl.List(ctx, &wls)
			if err != nil {
				t.Fatalf("Failed to List workloads: %v", err)
			}

			if diff := cmp.Diff(tc.wantWorkloads, wls.Items, cmpopts.IgnoreFields(corev1.ResourceRequirements{}, "Requests")); diff != "" {
				t.Errorf("Workloads mismatch (-want +got):\n%s", diff)
			}

			// Only check events if wantEvents is explicitly set in the test case
			if tc.wantEvents != nil {
				if diff := cmp.Diff(tc.wantEvents, recorder.RecordedEvents); diff != "" {
					t.Errorf("Unexpected events (-want +got):\n%s", diff)
				}
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
			initialActive:  new(false),
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
				wl.Spec.Active = new(*tc.initialActive)
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
			mgj.MockGenericJob.EXPECT().PodSets(gomock.Any(), gomock.Any()).Return(basePodSets, nil).AnyTimes()
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
				UID(childJobName).
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
							Controller: new(true),
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
						UID:       "cronjob",
						OwnerReferences: []metav1.OwnerReference{{
							Name:       "aw",
							APIVersion: awv1beta2.GroupVersion.String(),
							Kind:       awv1beta2.AppWrapperKind,
							UID:        "aw",
							Controller: new(true),
						}},
					},
				},
			},
			job: testingjob.MakeJob("job", jobNamespace).UID("job").
				OwnerReference("cronjob", batchv1.SchemeGroupVersion.WithKind("CronJob")).
				Obj(),
			wantManaged: testingaw.MakeAppWrapper("aw", jobNamespace).UID("aw").Queue("test-q").Obj(),
		},
		"child job has ownerReference whose UID does not match the referenced object => nil": {
			integrations: []string{"kubeflow.org/mpijob"},
			ancestors: []client.Object{
				testingmpijob.MakeMPIJob(parentJobName, jobNamespace).
					UID(parentJobName).
					Queue("test-q").
					Obj(),
			},
			job: func() client.Object {
				job := testingjob.MakeJob(childJobName, jobNamespace).
					OwnerReference(parentJobName, kfmpi.SchemeGroupVersionKind).
					Obj()
				// Point owner reference at the real parent by name with a mismatched UID.
				job.OwnerReferences[0].UID = "forged-uid"
				return job
			}(),
			wantManaged: nil,
		},
		"Pod -> ReplicaSet -> Deployment (queue-name) => Deployment": {
			integrations: []string{"pod", "deployment"},
			ancestors: []client.Object{
				testingdeployment.MakeDeployment("deploy", jobNamespace).UID("deploy").Queue("test-q").Obj(),
				&appsv1.ReplicaSet{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "rs",
						Namespace: jobNamespace,
						UID:       "rs",
						OwnerReferences: []metav1.OwnerReference{{
							Name:       "deploy",
							APIVersion: appsv1.SchemeGroupVersion.String(),
							Kind:       "Deployment",
							UID:        "deploy",
							Controller: new(true),
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
			integrationManager := jobs.NewIntegrationManager()
			t.Cleanup(integrationManager.EnableIntegrationsForTest(t, tc.integrations...))
			t.Cleanup(integrationManager.EnableExternalIntegrationsForTest(t, tc.externalFrameworks...))
			ctx, _ := utiltesting.ContextWithLog(t)
			recorder := &utiltesting.EventRecorder{}
			builder := utiltesting.NewClientBuilder(kfmpi.AddToScheme, awv1beta2.AddToScheme, v1alpha2.AddToScheme)
			builder = builder.WithObjects(tc.ancestors...)
			if tc.job != nil {
				builder = builder.WithObjects(tc.job)
			}
			cl := builder.Build()
			gotManaged, gotErr := integrationManager.FindAncestorJobManagedByKueue(ctx, cl, tc.job, tc.manageJobsWithoutQueueName)
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
				WithWaitForPodsReady(&configapi.WaitForPodsReady{}),
				WithKubeServerVersion(&kubeversion.ServerVersionFetcher{}),
				WithLabelKeysToCopy(sets.New("toCopyKey")),
				WithAnnotationsToCopy(sets.New("toCopyAnnotation")),
				WithClock(fakeClock),
			},
			wantOpts: Options{
				ManageJobsWithoutQueueName: true,
				WaitForPodsReady:           true,
				KubeServerVersion:          &kubeversion.ServerVersionFetcher{},
				IntegrationOptions:         nil,
				LabelKeysToCopy:            sets.New("toCopyKey"),
				AnnotationsToCopy:          sets.New("toCopyAnnotation"),
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
				AnnotationsToCopy:          nil,
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

func TestProcessOptionsWithIntegrationManager(t *testing.T) {
	manager := NewIntegrationManager()

	options := ProcessOptions(WithIntegrationManager(manager))

	if options.IntegrationManager != manager {
		t.Error("ProcessOptions() did not preserve the integration manager")
	}
}

func TestNewReconcilerInitializesIntegrationManager(t *testing.T) {
	reconciler := NewReconciler(nil, nil)
	integrationManager := reflect.ValueOf(reconciler).Elem().FieldByName("integrationManager")
	if integrationManager.IsNil() {
		t.Error("NewReconciler() integrationManager is nil")
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
		"update podready condition recovery success": {
			workload: utiltestingapi.MakeWorkload("job-test-job-podready-recovery", metav1.NamespaceDefault).
				Finalizers(kueue.ResourceInUseFinalizerName).
				Label(constants.JobUIDLabel, "job-test-job-podready-recovery").
				ControllerReference(testGVK, "test-job-podready-recovery", "test-job-podready-recovery").
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
					Reason:             kueue.WorkloadWaitForRecovery,
					Message:            "Not all pods are ready or succeeded",
					LastTransitionTime: metav1.NewTime(time.Now()),
				}).
				Admission(&kueue.Admission{
					ClusterQueue: "default-cq",
				}).
				Obj(),
			job: (*job.Job)(testingjob.MakeJob("test-job-podready-recovery", metav1.NamespaceDefault).
				UID("test-job-podready-recovery").
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
				WithClock(fakeClock),
				WithWaitForPodsReady(&configapi.WaitForPodsReady{}),
				WithCache(schdcache.New(cl)),
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

func TestReconcileGenericJob_EvictionClearsQuotaReservation(t *testing.T) {
	scenarios := []map[featuregate.Feature]bool{
		{
			features.UnadmittedWorkloadsObservability: false,
		},
		{
			features.UnadmittedWorkloadsObservability: true,
		},
	}

	for _, scenario := range scenarios {
		t.Run(fmt.Sprintf("UnadmittedWorkloadsObservability enabled: %t", scenario[features.UnadmittedWorkloadsObservability]), func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, scenario)
			ctx, _ := utiltesting.ContextWithLog(t)
			mockctrl := gomock.NewController(t)

			podSets := []kueue.PodSet{
				*utiltestingapi.MakePodSet("main", 1).Obj(),
			}

			job := testingjob.MakeJob("job-1", "ns").Queue("cq").UID("job-1").Suspend(true).Obj()

			wl := utiltestingapi.MakeWorkload("job-1", "ns").
				PodSets(podSets...).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), time.Now()).
				Condition(metav1.Condition{
					Type:   kueue.WorkloadEvicted,
					Status: metav1.ConditionTrue,
					Reason: kueue.WorkloadEvictedByPreemption,
				}).
				ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job-1", "job-1").
				Obj()

			mgj := mocks.NewMockGenericJob(mockctrl)
			mgj.EXPECT().Object().Return(job).AnyTimes()
			mgj.EXPECT().GVK().Return(batchv1.SchemeGroupVersion.WithKind("Job")).AnyTimes()
			mgj.EXPECT().IsSuspended().Return(true).AnyTimes()
			mgj.EXPECT().IsActive().Return(false).AnyTimes()
			mgj.EXPECT().Finished(gomock.Any()).Return("", false, false).AnyTimes()
			mgj.EXPECT().PodSets(gomock.Any(), gomock.Any()).Return(podSets, nil).AnyTimes()

			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns"}}
			gvk := batchv1.SchemeGroupVersion.WithKind("Job")
			clientBuilder := utiltesting.NewClientBuilder().
				WithObjects(job, wl, ns).
				WithStatusSubresource(job, wl).
				WithIndex(&kueue.Workload{}, indexer.OwnerReferenceIndexKey(gvk), indexer.WorkloadOwnerIndexFunc(gvk))
			cl := clientBuilder.Build()

			recorder := &utiltesting.EventRecorder{}
			r := NewReconciler(cl, recorder)

			req := controllerruntime.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "job-1"}}
			_, err := r.ReconcileGenericJob(ctx, req, mgj)
			if err != nil {
				t.Fatalf("ReconcileGenericJob() error: %v", err)
			}

			var gotWl kueue.Workload
			if err := cl.Get(ctx, types.NamespacedName{Namespace: "ns", Name: "job-1"}, &gotWl); err != nil {
				t.Fatalf("failed to get workload: %v", err)
			}

			wantReason := kueue.WorkloadPending //nolint:staticcheck // SA1019: fallback
			if scenario[features.UnadmittedWorkloadsObservability] {
				wantReason = kueue.WorkloadQuotaReservedReasonPendingEvaluation
			}

			cond := apimeta.FindStatusCondition(gotWl.Status.Conditions, kueue.WorkloadQuotaReserved)
			if cond == nil {
				t.Fatalf("QuotaReserved condition not found")
			}
			if cond.Status != metav1.ConditionFalse || cond.Reason != wantReason {
				t.Errorf("Unexpected QuotaReserved condition status/reason: got %s/%s, want False/%s", cond.Status, cond.Reason, wantReason)
			}
		})
	}
}

// prebuiltJobState is the part of a job the out-of-sync path reads and moves.
type prebuiltJobState struct {
	suspended bool
	active    bool
	finished  bool
	success   bool
	restored  bool
	// suspendedAtWrite is what suspended was when the workload's status was
	// last written, and workloadWritten records that it was written at all.
	suspendedAtWrite bool
	workloadWritten  bool
	// finishedOnReread makes Finished report terminal only from the reload on, modeling a job
	// that completes while stopping. finishedCalls finds the reload: it is the third read.
	finishedOnReread bool
	finishedCalls    int
	// finishedAfterWrite makes Finished report terminal only once the workload's status has been
	// written, modeling a job that ends while the OutOfSync write is in flight.
	finishedAfterWrite bool
	// activeOnReread makes IsActive report running only from the reload on, modeling a controller
	// that created a pod from a view older than the stop. activeCalls counts them the same way.
	activeOnReread bool
	activeCalls    int
	// uidChangesOnReload models a job deleted and recreated under the same name between the
	// reconcile's own read and the reload. jobGets counts the reads to tell the two apart.
	uidChangesOnReload bool
	jobGets            int
	// uidChangesAfterWrite models the same recreation landing while the OutOfSync write is in flight.
	uidChangesAfterWrite bool
}

// countingStopJob wraps a GenericJob mock and implements JobWithCustomStop so a second stop is
// observable: a plain mock's generic stop would silently no-op once suspended.
type countingStopJob struct {
	*mocks.MockGenericJob
	stops *int
}

func (j *countingStopJob) Stop(context.Context, client.Client, []podset.PodSetInfo, StopReason, string) (bool, error) {
	*j.stops++
	return true, nil
}

// countingFinalizeJob wraps a GenericJob mock and implements JobWithFinalize so finalizing is
// observable: only the pod integration implements it, and there it removes pod finalizers.
type countingFinalizeJob struct {
	*mocks.MockGenericJob
	finalizes *int
}

func (j *countingFinalizeJob) Finalize(context.Context, client.Client) error {
	*j.finalizes++
	return nil
}

func TestReconcileGenericJob_PrebuiltOutOfSync(t *testing.T) {
	const wlName = "prebuilt"
	gvk := batchv1.SchemeGroupVersion.WithKind("Job")

	testCases := map[string]struct {
		state    prebuiltJobState
		failStop bool
		// countStops swaps in a JobWithCustomStop so a second stop is observable.
		countStops bool
		// podsGoAway drops IsActive and reconciles again, which is how the wait ends.
		podsGoAway bool
		// secondPass reconciles again without changing anything. The pass that submits the stop
		// never decides, so every case that reaches a decision needs one.
		secondPass bool
		// finishedOutOfSync starts the workload already finished as out of sync, which is what
		// an earlier pass leaves behind when the correction could not be written.
		finishedOutOfSync bool
		// matching gives the job the pod set the workload reserved, so the pair is equivalent.
		matching bool
		// countFinalizes swaps in a JobWithFinalize so finalizing the job is observable.
		countFinalizes bool

		wantErr           bool
		wantStopped       bool
		wantRestored      bool
		wantReason        string
		wantFinalizerGone bool
		wantStops         int
		wantFinalizes     int
		// wantRequeue is the poll a pass that is still waiting for the stop has to leave behind.
		wantRequeue bool
		// wantStopMessage is the Stopped event's message, which names why the job was stopped.
		wantStopMessage string
	}{
		"a job with running pods is stopped, and its workload waits until they are gone": {
			state:             prebuiltJobState{active: true},
			podsGoAway:        true,
			wantStopped:       true,
			wantRestored:      true,
			wantReason:        kueue.WorkloadFinishedReasonOutOfSync,
			wantFinalizerGone: true,
			wantRequeue:       true,
			wantStopMessage:   "The prebuilt workload is out of sync with its user job",
		},
		// The job has no pods at this instant but is not suspended, so it is free to
		// create one. Stopping it is what makes finishing the workload safe. It also anchors the
		// replaced-job case below: same path, job unchanged.
		"a job with no pods yet is stopped before its workload is finished": {
			secondPass:        true,
			countFinalizes:    true,
			wantStopped:       true,
			wantRestored:      true,
			wantReason:        kueue.WorkloadFinishedReasonOutOfSync,
			wantFinalizerGone: true,
			wantFinalizes:     1,
			wantRequeue:       true,
		},
		"a job that cannot be stopped keeps its quota": {
			failStop:     true,
			wantErr:      true,
			wantStopped:  true,
			wantRestored: true,
		},
		"a job that succeeded keeps its own reason": {
			state:      prebuiltJobState{finished: true, success: true},
			wantReason: kueue.WorkloadFinishedReasonSucceeded,
		},
		"a job that failed keeps its own reason": {
			state:      prebuiltJobState{finished: true},
			wantReason: kueue.WorkloadFinishedReasonFailed,
		},
		// Finished is false at the first check and true from the reload on: the job completed
		// while stopping. The workload must finish as Succeeded — the input to MultiKueue's
		// retry decision — never OutOfSync.
		"a job that finishes while stopping keeps its own reason, not OutOfSync": {
			state:        prebuiltJobState{finishedOnReread: true, success: true},
			secondPass:   true,
			wantStopped:  true,
			wantRestored: true,
			wantReason:   kueue.WorkloadFinishedReasonSucceeded,
			wantRequeue:  true,
		},
		// The job ends while the OutOfSync write is in flight, so both checks before it read a
		// running job. The reason left behind is what MultiKueue re-dispatches on, so the read
		// after the write has to put the job's own reason back.
		"a job that ends while the OutOfSync write is in flight has its reason corrected": {
			state:             prebuiltJobState{finishedAfterWrite: true, success: true},
			secondPass:        true,
			wantStopped:       true,
			wantRestored:      true,
			wantReason:        kueue.WorkloadFinishedReasonSucceeded,
			wantFinalizerGone: true,
			wantRequeue:       true,
		},
		// IsActive is false at the first check and true from the reload on: the external
		// controller created a pod from a view older than the stop. The reload is the whole
		// reason the second check exists — the first one ran against the object this reconcile
		// started with, which is by then stale.
		"a job that is running again at the reload is waited for, not marked OutOfSync": {
			state:        prebuiltJobState{activeOnReread: true},
			secondPass:   true,
			wantStopped:  true,
			wantRestored: true,
			wantRequeue:  true,
		},
		// Without the UID check the reload would hand this reconcile a different job that happens
		// to carry the same name, and the workload it holds is not that job's.
		// It falls through to the no-workload path, which is where a prebuilt job whose workload
		// is not its own belongs, and that path reports the workload as not found.
		"a job replaced under the same name between the read and the reload is not usable": {
			state:       prebuiltJobState{suspended: true, uidChangesOnReload: true},
			wantErr:     true,
			wantStopped: true,
		},
		// The pair matches again, which every other path treats as nothing to do. The reason an
		// earlier pass left behind still has to be put right before the finalizer comes off.
		"a workload finished as OutOfSync is corrected even once the pair matches again": {
			state:             prebuiltJobState{finished: true, success: true},
			matching:          true,
			finishedOutOfSync: true,
			wantReason:        kueue.WorkloadFinishedReasonSucceeded,
			wantFinalizerGone: true,
		},
		// An earlier pass finished the workload as out of sync and could not write the
		// correction. The finalizer is still on, so the reason the job ended with can replace it.
		"a workload already finished as OutOfSync is corrected before it is finalized": {
			state:             prebuiltJobState{finished: true, success: true},
			finishedOutOfSync: true,
			wantReason:        kueue.WorkloadFinishedReasonSucceeded,
			wantFinalizerGone: true,
		},
		// The pair matches again, but the quota the earlier OutOfSync finish released does not come
		// back with it. Releasing the workload here would leave the job running unaccounted for.
		"a workload finished as OutOfSync stops a running job even once the pair matches again": {
			state:             prebuiltJobState{active: true},
			matching:          true,
			finishedOutOfSync: true,
			wantStopped:       true,
			wantRestored:      true,
			wantReason:        kueue.WorkloadFinishedReasonOutOfSync,
			wantRequeue:       true,
			wantStopMessage:   "The prebuilt workload is already finished",
		},
		// The read after the write returns a different job. Finalizing it would remove Kueue's
		// finalizer from the replacement's pods; only the workload's own finalizer is owed.
		"a job replaced under the same name during the OutOfSync write is not finalized": {
			state:             prebuiltJobState{uidChangesAfterWrite: true},
			secondPass:        true,
			countFinalizes:    true,
			wantStopped:       true,
			wantRestored:      true,
			wantReason:        kueue.WorkloadFinishedReasonOutOfSync,
			wantFinalizerGone: true,
			wantFinalizes:     0,
			wantRequeue:       true,
		},
		// The old bool return turned "waiting" into wl==nil, so the reconciler fell into
		// handleJobWithNoWorkload and stopped the job a second time as "missing workload".
		"the job is stopped exactly once and waiting never enters the missing-workload path": {
			state:       prebuiltJobState{active: true},
			countStops:  true,
			wantStops:   1,
			wantRequeue: true,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			mockctrl := gomock.NewController(t)
			st := tc.state
			wlKey := types.NamespacedName{Namespace: "ns", Name: wlName}

			// The job wants one pod against two reserved, so the pair no longer matches.
			jobCount := 1
			if tc.matching {
				jobCount = 2
			}
			jobPodSets := []kueue.PodSet{*utiltestingapi.MakePodSet("main", jobCount).Obj()}
			wlPodSets := []kueue.PodSet{*utiltestingapi.MakePodSet("main", 2).Obj()}

			job := testingjob.MakeJob("job-1", "ns").
				Queue("cq").
				UID("job-1").
				PrebuiltWorkloadLabel(wlName).
				Suspend(st.suspended).
				Obj()
			wlWrapper := utiltestingapi.MakeWorkload(wlName, "ns").
				PodSets(wlPodSets...).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), time.Now()).
				Finalizers(kueue.ResourceInUseFinalizerName).
				ControllerReference(gvk, "job-1", "job-1")
			if tc.finishedOutOfSync {
				wlWrapper = wlWrapper.Condition(metav1.Condition{
					Type:    kueue.WorkloadFinished,
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadFinishedReasonOutOfSync,
					Message: "The prebuilt workload is out of sync with its user job",
				})
			}
			wl := wlWrapper.Obj()

			mgj := mocks.NewMockGenericJob(mockctrl)
			mgj.EXPECT().Object().Return(job).AnyTimes()
			mgj.EXPECT().GVK().Return(gvk).AnyTimes()
			mgj.EXPECT().PodSets(gomock.Any(), gomock.Any()).Return(jobPodSets, nil).AnyTimes()
			mgj.EXPECT().IsSuspended().DoAndReturn(func() bool { return st.suspended }).AnyTimes()
			mgj.EXPECT().IsActive().DoAndReturn(func() bool {
				st.activeCalls++
				return st.active || (st.activeOnReread && st.activeCalls >= 2)
			}).AnyTimes()
			mgj.EXPECT().Suspend().Do(func() { st.suspended = true }).AnyTimes()
			mgj.EXPECT().RestorePodSetsInfo(gomock.Any(), gomock.Any()).
				DoAndReturn(func(_ context.Context, info []podset.PodSetInfo) bool {
					st.restored = len(info) > 0
					return true
				}).AnyTimes()
			mgj.EXPECT().Finished(gomock.Any()).
				DoAndReturn(func(context.Context) (string, bool, bool) {
					st.finishedCalls++
					finished := st.finished ||
						(st.finishedOnReread && st.finishedCalls >= 3) ||
						(st.finishedAfterWrite && st.workloadWritten)
					return "by the job", st.success, finished
				}).AnyTimes()

			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ns"}}
			cl := utiltesting.NewClientBuilder().
				WithObjects(job, wl, ns).
				WithStatusSubresource(job, wl).
				WithIndex(&kueue.Workload{}, indexer.OwnerReferenceIndexKey(gvk), indexer.WorkloadOwnerIndexFunc(gvk)).
				WithInterceptorFuncs(interceptor.Funcs{
					Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						if err := c.Get(ctx, key, obj, opts...); err != nil {
							return err
						}
						if reloaded, isJob := obj.(*batchv1.Job); isJob {
							st.jobGets++
							if (st.uidChangesOnReload && st.jobGets >= 2) ||
								(st.uidChangesAfterWrite && st.workloadWritten) {
								reloaded.UID = "a-different-job"
							}
						}
						return nil
					},
					Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
						if _, isJob := obj.(*batchv1.Job); isJob && tc.failStop {
							return errors.New("the job could not be stopped")
						}
						return c.Patch(ctx, obj, patch, opts...)
					},
					SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
						if _, isWorkload := obj.(*kueue.Workload); isWorkload {
							st.suspendedAtWrite = st.suspended
							st.workloadWritten = true
						}
						return c.SubResource(sub).Patch(ctx, obj, patch, opts...)
					},
				}).
				Build()
			recorder := &utiltesting.EventRecorder{}
			r := NewReconciler(cl, recorder)
			req := controllerruntime.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "job-1"}}

			// The wrappers are alternatives: each replaces the mock, so a case picks one.
			var genericJob GenericJob = mgj
			stops, finalizes := 0, 0
			switch {
			case tc.countStops:
				genericJob = &countingStopJob{MockGenericJob: mgj, stops: &stops}
			case tc.countFinalizes:
				genericJob = &countingFinalizeJob{MockGenericJob: mgj, finalizes: &finalizes}
			}

			res, err := r.ReconcileGenericJob(ctx, req, genericJob)
			if gotErr := err != nil; gotErr != tc.wantErr {
				t.Fatalf("ReconcileGenericJob() error = %v, wantErr %v", err, tc.wantErr)
			}
			if gotRequeue := res.RequeueAfter > 0; gotRequeue != tc.wantRequeue {
				t.Errorf("the pass requeued = %v, want %v", gotRequeue, tc.wantRequeue)
			}

			if tc.podsGoAway || tc.secondPass {
				var mid kueue.Workload
				if err := cl.Get(ctx, wlKey, &mid); err != nil {
					t.Fatalf("getting the workload: %v", err)
				}
				if cond := apimeta.FindStatusCondition(mid.Status.Conditions, kueue.WorkloadFinished); cond != nil && cond.Status == metav1.ConditionTrue {
					t.Errorf("the workload was finished for %q on the pass that submitted the stop", cond.Reason)
				}
				st.active = false
				// The workload is handled here, so the reconcile has nothing left to
				// report, rather than the not-found it used to retry on.
				if _, err := r.ReconcileGenericJob(ctx, req, genericJob); err != nil {
					t.Fatalf("second reconcile: %v", err)
				}
			}

			var got kueue.Workload
			if err := cl.Get(ctx, wlKey, &got); err != nil {
				t.Fatalf("getting the workload: %v", err)
			}
			gotReason := ""
			if cond := apimeta.FindStatusCondition(got.Status.Conditions, kueue.WorkloadFinished); cond != nil && cond.Status == metav1.ConditionTrue {
				gotReason = cond.Reason
			}
			if gotReason != tc.wantReason {
				t.Errorf("the workload finished for %q, want %q", gotReason, tc.wantReason)
			}
			if st.suspended != tc.wantStopped {
				t.Errorf("the job was stopped = %v, want %v", st.suspended, tc.wantStopped)
			}
			if st.restored != tc.wantRestored {
				t.Errorf("the pod set info was restored = %v, want %v, which is how the workload reaches the stop", st.restored, tc.wantRestored)
			}
			// Only when this run finished it; a seeded reason comes from an earlier pass.
			if tc.wantReason == kueue.WorkloadFinishedReasonOutOfSync && !tc.finishedOutOfSync && !st.suspendedAtWrite {
				t.Error("the workload was finished before the job was stopped")
			}
			if gone := len(got.Finalizers) == 0; gone != tc.wantFinalizerGone {
				t.Errorf("the workload finalizers %v, want gone = %v", got.Finalizers, tc.wantFinalizerGone)
			}
			if stops != tc.wantStops {
				t.Errorf("the job was stopped %d times, want %d", stops, tc.wantStops)
			}
			if finalizes != tc.wantFinalizes {
				t.Errorf("the job was finalized %d times, want %d", finalizes, tc.wantFinalizes)
			}
			if tc.wantStopMessage != "" {
				var stopped []string
				for _, e := range recorder.RecordedEvents {
					if e.Reason == ReasonStopped {
						stopped = append(stopped, e.Message)
					}
				}
				if !slices.Contains(stopped, tc.wantStopMessage) {
					t.Errorf("the Stopped events said %q, want one saying %q", stopped, tc.wantStopMessage)
				}
			}
		})
	}
}
