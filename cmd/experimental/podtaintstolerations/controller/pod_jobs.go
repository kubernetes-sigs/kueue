/*
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

package controller

import (
	"context"
	"sort"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
)

const (
	ControllerName = "kueue-podtaintstolerations"
	FrameworkName  = "core/pod"
)

var (
	AdmissionTaintKey = "kueue.x-k8s.io/kueue-admission"
	GVK               = corev1.SchemeGroupVersion.WithKind("Pod")
	NewReconciler     = jobframework.NewGenericReconciler(func() jobframework.GenericJob { return &Pod{} }, nil)
)

var (
	_ jobframework.GenericJob           = (*Pod)(nil)
	_ jobframework.JobWithCustomStop    = (*Pod)(nil)
	_ jobframework.JobWithPriorityClass = (*Pod)(nil)
)

type Pod corev1.Pod

func (j *Pod) Object() client.Object {
	return (*corev1.Pod)(j)
}

func (j *Pod) GetGVK() schema.GroupVersionKind {
	return GVK
}

func (j *Pod) IsSuspended() bool {
	for _, t := range j.Spec.Tolerations {
		if t.Key == AdmissionTaintKey && t.Operator == corev1.TolerationOpExists {
			return false
		}
	}
	return true
}

func (j *Pod) IsActive() bool {
	return j.Status.Phase == corev1.PodRunning
}

func (p *Pod) Suspend() {
	// Not used, see Stop()
}

func (p *Pod) Stop(ctx context.Context, c client.Client, _ []jobframework.PodSetInfo) error {
	if err := client.IgnoreNotFound(c.Delete(ctx, p.Object())); err != nil {
		return err
	}
	return nil
}

func (j *Pod) GVK() schema.GroupVersionKind {
	return GVK
}

func (p *Pod) PodSets() []kueue.PodSet {
	return []kueue.PodSet{
		{
			Name: kueue.DefaultPodSetName,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: *p.ObjectMeta.DeepCopy(),
				Spec:       *p.Spec.DeepCopy(),
			},
			Count: 1,
		},
	}
}

func (j *Pod) RunWithPodSetsInfo(podSetsInfo []jobframework.PodSetInfo) {
	info := podSetsInfo[0]

	var admissionTaintIsSet bool
	selectorIsSet := map[string]bool{}
	for i := range j.Spec.Tolerations {
		// Ensure the admission toleration is set correctly.
		if j.Spec.Tolerations[i].Key == AdmissionTaintKey {
			j.Spec.Tolerations[i].Value = ""
			j.Spec.Tolerations[i].Operator = corev1.TolerationOpExists
			j.Spec.Tolerations[i].Effect = corev1.TaintEffect("")
			admissionTaintIsSet = true
		}

		for k, v := range info.NodeSelector {
			if j.Spec.Tolerations[i].Key == k {
				j.Spec.Tolerations[i].Value = v
				j.Spec.Tolerations[i].Operator = corev1.TolerationOpEqual
				j.Spec.Tolerations[i].Effect = corev1.TaintEffect("")
				selectorIsSet[k] = true
			}
		}
	}

	if !admissionTaintIsSet {
		j.Spec.Tolerations = append(j.Spec.Tolerations, corev1.Toleration{
			Key:      AdmissionTaintKey,
			Operator: corev1.TolerationOpExists,
		})
	}

	var unsetSelectors []string
	for k := range info.NodeSelector {
		if !selectorIsSet[k] {
			unsetSelectors = append(unsetSelectors, k)
		}
	}
	sort.Strings(unsetSelectors)

	for _, k := range unsetSelectors {
		j.Spec.Tolerations = append(j.Spec.Tolerations, corev1.Toleration{
			Key:      k,
			Value:    info.NodeSelector[k],
			Operator: corev1.TolerationOpEqual,
		})
	}
}

func (p *Pod) RestorePodSetsInfo(_ []jobframework.PodSetInfo) {
	// Existing Pod tolerations cannot be removed.
	// Restoring is not needed anyways b/c suspending == deleting for Pods.
}

func (j *Pod) Finished() (metav1.Condition, bool) {
	condition := metav1.Condition{
		Type:   kueue.WorkloadFinished,
		Status: metav1.ConditionTrue,
		Reason: "JobFinished",
	}

	var finished bool
	switch j.Status.Phase {
	case corev1.PodSucceeded:
		finished = true
		condition.Message = "Pod finished successfully"
	case corev1.PodFailed:
		finished = true
		condition.Message = "Pod failed"
	}

	return condition, finished
}

func (j *Pod) PriorityClass() string {
	return j.Spec.PriorityClassName
}

func (j *Pod) PodsReady() bool {
	for _, c := range j.Status.Conditions {
		if c.Type == corev1.PodReady {
			if c.Status == corev1.ConditionTrue {
				return true
			} else {
				return false
			}
		}
	}
	return false
}

func SetupIndexes(ctx context.Context, indexer client.FieldIndexer) error {
	return jobframework.SetupWorkloadOwnerIndex(ctx, indexer, GVK)
}

func GetWorkloadNameForJob(jobName string) string {
	return jobframework.GetWorkloadNameForOwnerWithGVK(jobName, GVK)
}
