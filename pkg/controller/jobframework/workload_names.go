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

package jobframework

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/kueue/pkg/features"
)

const (
	hashLength                = 5
	longMaxWorkloadNameLength = 253
	// shortMaxWorkloadNameLength is set to 63 to comply with Kubernetes label value length constraints.
	// Kubernetes label values must be 63 characters or less per the Kubernetes specification.
	// Since workload names are used as label values, they must respect this limit.
	// See https://github.com/kubernetes-sigs/kueue/issues/9872 and
	// https://github.com/kubernetes-sigs/kueue/issues/10098 for more details.
	shortMaxWorkloadNameLength = 63
)

// maxWorkloadNameLength returns the maximum allowed length for a workload name based on the enabled feature configuration.
func maxWorkloadNameLength() int {
	if features.Enabled(features.ShortWorkloadNames) {
		return shortMaxWorkloadNameLength
	}
	return longMaxWorkloadNameLength
}

// maxPrefixLength calculates the maximum length of the workload name prefix, subtracting the hash length and separator.
func maxPrefixLength() int {
	return maxWorkloadNameLength() - 1 - hashLength
}

// truncate returns the first n characters of a string.
func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func GetWorkloadNameForOwnerWithGVK(ownerName string, ownerUID types.UID, ownerGVK schema.GroupVersionKind) string {
	return generateWorkloadName(ownerName, ownerUID, ownerGVK, nil)
}

func GetWorkloadNameForOwnerWithGVKAndGeneration(ownerName string, ownerUID types.UID, ownerGVK schema.GroupVersionKind, generation int64) string {
	return generateWorkloadName(ownerName, ownerUID, ownerGVK, &generation)
}

func generateWorkloadName(ownerName string, ownerUID types.UID, ownerGVK schema.GroupVersionKind, generation *int64) string {
	return fmt.Sprintf(
		"%s-%s",
		truncate(fmt.Sprintf("%s-%s", strings.ToLower(ownerGVK.Kind), ownerName), maxPrefixLength()),
		getHash(ownerName, ownerUID, ownerGVK, generation)[:hashLength],
	)
}

func getHash(ownerName string, ownerUID types.UID, gvk schema.GroupVersionKind, generation *int64) string {
	h := sha1.New()
	h.Write([]byte(gvk.Kind))
	h.Write([]byte("\n"))
	h.Write([]byte(gvk.Group))
	h.Write([]byte("\n"))
	h.Write([]byte(ownerName))
	h.Write([]byte("\n"))
	h.Write([]byte(ownerUID))
	if generation != nil {
		h.Write([]byte("\n"))
		h.Write([]byte(strconv.FormatInt(ptr.Deref(generation, 0), 10)))
	}
	return hex.EncodeToString(h.Sum(nil))
}
