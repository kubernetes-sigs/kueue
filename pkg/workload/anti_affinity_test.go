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

package workload

import (
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/kueue/pkg/features"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
)

func TestPinnedBatchJobSelfAntiAffinityConflict(t *testing.T) {
	const uid = "job-uid"
	podSet := utiltestingapi.MakePodSet("main", 2).
		NodeSelector(map[string]string{corev1.LabelHostname: "node-a"}).Obj()
	podSet.Template.Spec.Affinity = &corev1.Affinity{PodAntiAffinity: &corev1.PodAntiAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{
			TopologyKey:   corev1.LabelHostname,
			LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{batchv1.ControllerUidLabel: uid}},
		}},
	}}
	assignment := utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
		Domain(utiltas.TopologyDomainAssignment{Values: []string{"node-a"}, Count: 2}).Obj()
	workload := utiltestingapi.MakeWorkload("wl", "ns").
		PodSets(*podSet).
		ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job", uid).
		Admission(utiltestingapi.MakeAdmission("cq").PodSets(
			utiltestingapi.MakePodSetAssignment("main").TopologyAssignment(assignment).Obj(),
		).Obj()).Obj()
	matchingJob := testingjob.MakeJob("job", "ns").UID(uid).
		PodLabel(batchv1.ControllerUidLabel, uid).Obj()
	otherLabelJob := matchingJob.DeepCopy()
	otherLabelJob.Spec.Template.Labels[batchv1.ControllerUidLabel] = "other-uid"
	otherLabelJob.Spec.ManualSelector = new(true)
	otherLabelJob.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{batchv1.ControllerUidLabel: "other-uid"}}

	cases := map[string]struct {
		job          *batchv1.Job
		tasEnabled   bool
		wantConflict bool
	}{
		"Job template label matches anti-affinity":  {job: matchingJob, tasEnabled: true, wantConflict: true},
		"Job template label differs from owner UID": {job: otherLabelJob, tasEnabled: true},
		"TAS disabled ignores persisted assignment": {job: matchingJob},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, tc.tasEnabled)
			cl := utiltesting.NewClientBuilder().WithObjects(tc.job).Build()
			_, _, conflict, err := PinnedBatchJobSelfAntiAffinityConflict(t.Context(), cl, workload.DeepCopy())
			if err != nil {
				t.Fatalf("PinnedBatchJobSelfAntiAffinityConflict() error = %v", err)
			}
			if conflict != tc.wantConflict {
				t.Errorf("PinnedBatchJobSelfAntiAffinityConflict() conflict = %t, want %t", conflict, tc.wantConflict)
			}
		})
	}
}
