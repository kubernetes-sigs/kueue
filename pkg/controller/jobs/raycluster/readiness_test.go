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

package raycluster

import (
	"testing"
	"time"

	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingrayutil "sigs.k8s.io/kueue/pkg/util/testingjobs/raycluster"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

func TestReconcilePodsReadyWithPendingScaleUp(t *testing.T) {
	cases := map[string]struct {
		headStatus      metav1.ConditionStatus
		previouslyReady bool
		recovering      bool
		wantStatus      metav1.ConditionStatus
		wantReason      string
	}{
		"head becomes ready while worker waits for quota": {headStatus: metav1.ConditionTrue, wantStatus: metav1.ConditionTrue, wantReason: kueue.WorkloadStarted},
		"head remains unready":                            {headStatus: metav1.ConditionFalse, wantStatus: metav1.ConditionFalse, wantReason: kueue.WorkloadWaitForStart},
		"head loses readiness despite stale ready state":  {headStatus: metav1.ConditionFalse, previouslyReady: true, wantStatus: metav1.ConditionFalse, wantReason: kueue.WorkloadWaitForRecovery},
		"head readiness becomes unknown":                  {headStatus: metav1.ConditionUnknown, previouslyReady: true, wantStatus: metav1.ConditionFalse, wantReason: kueue.WorkloadWaitForRecovery},
		"head condition missing":                          {wantStatus: metav1.ConditionFalse, wantReason: kueue.WorkloadWaitForStart},
		"head recovers":                                   {headStatus: metav1.ConditionTrue, recovering: true, wantStatus: metav1.ConditionTrue, wantReason: kueue.WorkloadRecovered},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlices, true)
			ctx, _ := utiltesting.ContextWithLog(t)
			now := time.Now().Truncate(time.Second)
			clock := testingclock.NewFakeClock(now)
			cluster := testingrayutil.MakeCluster("cluster", "ns").Queue("q").Suspend(false).
				SetAnnotation(constants.ElasticJobAnnotation, "true").ScaleFirstWorkerGroup(1).Obj()
			cluster.UID = "cluster-uid"
			cluster.Status.State = rayv1.Ready
			if tc.headStatus != "" {
				cluster.Status.Conditions = []metav1.Condition{{Type: string(rayv1.HeadPodReady), Status: tc.headStatus}}
			}
			job := (*RayCluster)(cluster)
			podSets, err := job.PodSets(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			pending := utiltestingapi.MakeWorkload("pending", "ns").Queue("q").
				ControllerReference(gvk, cluster.Name, string(cluster.GetUID())).
				Annotation(kueue.WorkloadSliceNameAnnotation, "retained").
				Annotation(workloadslicing.WorkloadSliceReplacementFor, "ns/retained").
				PodSets(podSets...).Obj()
			retained := utiltestingapi.MakeWorkload("retained", "ns").Queue("q").
				ControllerReference(gvk, cluster.Name, string(cluster.GetUID())).
				PodSets(podSets...).ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), now).
				AdmittedAt(true, now).Obj()
			retained.Spec.PodSets = retained.DeepCopy().Spec.PodSets
			retained.Spec.PodSets[1].Count = 0
			initialStatus, initialReason := metav1.ConditionFalse, kueue.WorkloadWaitForStart
			if tc.previouslyReady {
				initialStatus, initialReason = metav1.ConditionTrue, kueue.WorkloadStarted
			}
			if tc.recovering {
				initialStatus, initialReason = metav1.ConditionFalse, kueue.WorkloadWaitForRecovery
			}
			apimeta.SetStatusCondition(
				&retained.Status.Conditions,
				metav1.Condition{Type: kueue.WorkloadPodsReady, Status: initialStatus, Reason: initialReason, LastTransitionTime: metav1.NewTime(now)},
			)
			builder := utiltesting.NewClientBuilder(rayv1.AddToScheme).
				WithObjects(utiltesting.MakeNamespace("ns"), cluster, retained, pending).
				WithStatusSubresource(cluster, retained, pending).
				WithInterceptorFuncs(interceptor.Funcs{SubResourceApply: utiltesting.TreatSSAAsStrategicMergeForApplyConfiguration})
			if err := SetupIndexes(ctx, utiltesting.AsIndexer(builder)); err != nil {
				t.Fatal(err)
			}
			cl := builder.Build()
			r := jobframework.NewReconciler(cl, &utiltesting.EventRecorder{}, jobframework.WithClock(clock),
				jobframework.WithCache(schdcache.New(cl)), jobframework.WithWaitForPodsReady(&configapi.WaitForPodsReady{Timeout: metav1.Duration{Duration: 2 * time.Minute}}))
			if _, err := r.ReconcileGenericJob(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(cluster)}, job); err != nil {
				t.Fatal(err)
			}
			got := &kueue.Workload{}
			if err := cl.Get(ctx, client.ObjectKeyFromObject(retained), got); err != nil {
				t.Fatal(err)
			}
			c := apimeta.FindStatusCondition(got.Status.Conditions, kueue.WorkloadPodsReady)
			if c == nil || c.Status != tc.wantStatus || c.Reason != tc.wantReason {
				t.Errorf("retained PodsReady = %+v, want %s/%s", c, tc.wantStatus, tc.wantReason)
			}
			if err := cl.Get(ctx, client.ObjectKeyFromObject(pending), got); err != nil {
				t.Fatal(err)
			}
			if apimeta.IsStatusConditionTrue(got.Status.Conditions, kueue.WorkloadPodsReady) {
				t.Error("pending replacement must not become ready before admission")
			}
		})
	}
}

func TestPodsReadyWorkload(t *testing.T) {
	cases := map[string]struct {
		elastic          bool
		gate             bool
		prebuilt         bool
		selectedAdmitted bool
		retainedAdmitted bool
		wantName         string
	}{
		"pending replacement uses admitted predecessor":       {elastic: true, gate: true, retainedAdmitted: true, wantName: "retained"},
		"non elastic keeps selected workload":                 {gate: true, retainedAdmitted: true, wantName: "selected"},
		"disabled gate keeps selected workload":               {elastic: true, retainedAdmitted: true, wantName: "selected"},
		"prebuilt workload keeps its assigned workload":       {elastic: true, gate: true, prebuilt: true, retainedAdmitted: true, wantName: "selected"},
		"admitted replacement keeps selected workload":        {elastic: true, gate: true, selectedAdmitted: true, retainedAdmitted: true, wantName: "selected"},
		"quota reservation without admission is insufficient": {elastic: true, gate: true, wantName: "selected"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlices, tc.gate)
			ctx, _ := utiltesting.ContextWithLog(t)
			cluster := testingrayutil.MakeCluster("cluster", "ns")
			if tc.elastic {
				cluster.SetAnnotation(constants.ElasticJobAnnotation, "true")
			}
			if tc.prebuilt {
				cluster.PrebuiltWorkloadAnnotation("selected")
			}
			now := time.Now()
			selected := utiltestingapi.MakeWorkload("selected", "ns").Obj()
			if tc.selectedAdmitted {
				selected = utiltestingapi.MakeWorkload("selected", "ns").ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), now).AdmittedAt(true, now).Obj()
			}
			retained := utiltestingapi.MakeWorkload("retained", "ns").
				ControllerReference(gvk, cluster.Name, string(cluster.GetUID())).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), now).
				AdmittedAt(tc.retainedAdmitted, now).
				Obj()
			builder := utiltesting.NewClientBuilder(rayv1.AddToScheme).WithObjects(retained)
			if err := SetupIndexes(ctx, utiltesting.AsIndexer(builder)); err != nil {
				t.Fatal(err)
			}
			got, err := (*RayCluster)(cluster.Obj()).PodsReadyWorkload(ctx, builder.Build(), selected)
			if err != nil {
				t.Fatal(err)
			}
			if got.Name != tc.wantName {
				t.Errorf("PodsReadyWorkload() = %q, want %q", got.Name, tc.wantName)
			}
		})
	}
}
