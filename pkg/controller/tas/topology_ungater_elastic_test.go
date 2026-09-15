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

package tas

import (
	"testing"
	"time"

	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	coreindexer "sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/controller/tas/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

func TestTopologyUngaterElasticSlice(t *testing.T) {
	for name, tc := range map[string]struct {
		deleteOrigin bool
		podReference string
		finished     bool
		evicted      bool
		admitted     bool
		wantUngated  bool
	}{
		"late pod references original slice":                  {podReference: "origin", admitted: true, wantUngated: true},
		"original slice has been deleted":                     {deleteOrigin: true, podReference: "origin", admitted: true, wantUngated: true},
		"pod references replacement but keeps chain identity": {podReference: "replacement", admitted: true, wantUngated: true},
		"finished replacement cannot ungate":                  {podReference: "origin", admitted: true, finished: true},
		"evicted replacement cannot ungate":                   {podReference: "origin", admitted: true, evicted: true},
		"quota reservation without admission cannot ungate":   {podReference: "origin"},
	} {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlices, true)
			ctx, log := utiltesting.ContextWithLog(t)
			g := gomega.NewWithT(t)
			now := time.Now()
			makeSlice := func(name string, count int32) *kueue.Workload {
				return utiltestingapi.MakeWorkload(name, "ns").
					Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					Annotation(kueue.WorkloadSliceNameAnnotation, "origin").
					PodSets(*utiltestingapi.MakePodSet("workers", int(count)).Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").PodSets(
						utiltestingapi.MakePodSetAssignment("workers").Count(count).
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
								Domains(utiltestingapi.MakeTopologyDomainAssignment([]string{"node"}, count).Obj()).Obj()).Obj(),
					).Obj(), now).AdmittedAt(true, now).Obj()
			}
			origin := makeSlice("origin", 1)
			origin.Status.Conditions = append(origin.Status.Conditions, metav1.Condition{Type: kueue.WorkloadFinished, Status: metav1.ConditionTrue})
			replacement := makeSlice("replacement", 2)
			replacement.CreationTimestamp = metav1.NewTime(now.Add(time.Second))
			if !tc.admitted {
				for i := range replacement.Status.Conditions {
					if replacement.Status.Conditions[i].Type == kueue.WorkloadAdmitted {
						replacement.Status.Conditions[i].Status = metav1.ConditionFalse
					}
				}
			}
			if tc.finished {
				replacement.Status.Conditions = append(replacement.Status.Conditions, metav1.Condition{Type: kueue.WorkloadFinished, Status: metav1.ConditionTrue})
			}
			if tc.evicted {
				replacement.Status.Conditions = append(replacement.Status.Conditions, metav1.Condition{Type: kueue.WorkloadEvicted, Status: metav1.ConditionTrue})
			}
			running := testingpod.MakePod("running", "ns").UID("running-uid").
				Annotation(kueue.WorkloadAnnotation, "origin").Annotation(kueue.WorkloadSliceNameAnnotation, "origin").
				Label(constants.PodSetLabel, "workers").NodeSelector(corev1.LabelHostname, "node").Obj()
			builder := utiltesting.NewClientBuilder()
			g.Expect(indexer.SetupIndexes(ctx, utiltesting.AsIndexer(builder))).To(gomega.Succeed())
			builder.WithIndex(&corev1.Pod{}, coreindexer.WorkloadSliceNameKey, coreindexer.IndexPodWorkloadSliceName)
			builder.WithIndex(&kueue.Workload{}, coreindexer.WorkloadSliceNameKey, coreindexer.IndexWorkloadSliceName)
			builder.WithObjects(running, replacement).WithStatusSubresource(&kueue.Workload{})
			if !tc.deleteOrigin {
				builder.WithObjects(origin)
			}
			c := builder.Build()
			r := newTopologyUngater(c, nil)
			// Admission is processed before the additional Pod exists.
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(replacement)})
			g.Expect(err).NotTo(gomega.HaveOccurred())
			late := testingpod.MakePod("late", "ns").UID("late-uid").
				Annotation(kueue.WorkloadAnnotation, tc.podReference).Annotation(kueue.WorkloadSliceNameAnnotation, "origin").
				Annotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true").
				Label(constants.PodSetLabel, "workers").TopologySchedulingGate().Obj()
			g.Expect(c.Create(ctx, late)).To(gomega.Succeed())
			h := podHandler{expectationsStore: r.expectationsStore}
			q := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
			defer q.ShutDown()
			h.Create(ctx, event.CreateEvent{Object: late}, q)
			g.Eventually(q.Len, 5*time.Second, 10*time.Millisecond).Should(gomega.Equal(1))
			req, shutdown := q.Get()
			g.Expect(shutdown).To(gomega.BeFalse())
			g.Expect(req.Name).To(gomega.Equal("origin"))
			_, err = r.Reconcile(ctx, req)
			q.Done(req)
			g.Expect(err).NotTo(gomega.HaveOccurred())
			updated := &corev1.Pod{}
			g.Expect(c.Get(ctx, client.ObjectKeyFromObject(late), updated)).To(gomega.Succeed())
			g.Expect(utilpod.HasGate(updated, kueue.TopologySchedulingGate)).To(gomega.Equal(!tc.wantUngated))
			if tc.wantUngated {
				g.Expect(updated.Spec.NodeSelector[corev1.LabelHostname]).To(gomega.Equal("node"))
				g.Expect(r.expectationsStore.Satisfied(log, client.ObjectKeyFromObject(origin))).To(gomega.BeFalse())
				h.Update(ctx, event.UpdateEvent{ObjectOld: late, ObjectNew: updated}, q)
				g.Expect(r.expectationsStore.Satisfied(log, client.ObjectKeyFromObject(origin))).To(gomega.BeTrue())
				_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(replacement)})
				g.Expect(err).NotTo(gomega.HaveOccurred())
			}
		})
	}
}
