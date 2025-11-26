package job

import (
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
)

func withNodeAffinity(j *batchv1.Job, affinity *corev1.Affinity) *batchv1.Job {
	j.Spec.Template.Spec.Affinity = affinity
	return j
}
