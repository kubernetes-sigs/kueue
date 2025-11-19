package taints

import corev1 "k8s.io/api/core/v1"

// TaintKeyExists checks if the given taint key exists in list of taints. Returns true if exists false otherwise.
func TaintKeyExists(taints []corev1.Taint, taintKeyToMatch string) bool {
	for _, taint := range taints {
		if taint.Key == taintKeyToMatch {
			return true
		}
	}
	return false
}
