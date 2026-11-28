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

package api

import (
	"maps"
	"unicode/utf8"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	maxEventMsgSize     = 1024
	maxConditionMsgSize = 32 * 1024
)

// TruncateEventMessage truncates a message if it hits the maxEventMessage.
func TruncateEventMessage(message string) string {
	return truncateMessage(message, maxEventMsgSize)
}

func TruncateConditionMessage(message string) string {
	return truncateMessage(message, maxConditionMsgSize)
}

// truncateMessage truncates a message to at most limit bytes, including the
// " ..." suffix, if it is longer than limit. The message is cut at a UTF-8
// character boundary, so the result is valid UTF-8 for valid UTF-8 input.
// The suffix is omitted when limit is too small to fit it.
func truncateMessage(message string, limit int) string {
	if len(message) <= limit {
		return message
	}
	suffix := " ..."
	if limit < len(suffix) {
		suffix = ""
	}
	cut := max(limit-len(suffix), 0)
	for cut > 0 && !utf8.RuneStart(message[cut]) {
		cut--
	}
	return message[:cut] + suffix
}

// CloneObjectMetaForCreation creates a copy of the provided ObjectMeta containing
// only the name, namespace, labels and annotations
func CloneObjectMetaForCreation(orig *metav1.ObjectMeta) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name:        orig.Name,
		Namespace:   orig.Namespace,
		Labels:      maps.Clone(orig.Labels),
		Annotations: maps.Clone(orig.Annotations),
	}
}
