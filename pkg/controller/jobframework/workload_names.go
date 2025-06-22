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
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	// nameDelimiter is the delimiter character used for joining name value tokens.
	nameDelimiter = "-"
	// hashLength is the name suffix length.
	hashLength = 5
	// 253 is the maximum length for a CRD name. We need to subtract one for '-', and the hash length.
	maxPrefixLength = validation.DNS1123SubdomainMaxLength - 1 - hashLength
)

// workloadSuffix generates a deterministic or random suffix for workload names,
// with a configurable length.
//
// If no input values are provided, it generates a random lowercase string of the
// specified 'length'. This is useful for creating unique names when no other
// identifying information is available.
//
// If input values are provided, it generates a deterministic suffix by:
//  1. Joining the input strings with newline characters.
//  2. Computing the SHA1 hash of the joined string.
//  3. Encoding the hash as a hexadecimal string.
//  4. Truncating the hexadecimal string to the specified 'length'.
//
// This deterministic approach ensures that the same input values always produce the
// same suffix, which is crucial for consistent workload naming.
func workloadSuffix(maxLength uint, values ...string) string {
	if len(values) == 0 {
		return strings.ToLower(rand.String(int(maxLength)))
	}
	h := sha1.New()
	h.Write([]byte(strings.Join(values, "\n")))
	return hex.EncodeToString(h.Sum(nil))[:maxLength]
}

// workloadPrefix generates a workload name prefix derived from the provided values,
// ensuring it does not exceed the specified maximum length.
//
// The function joins the input values with a "-" delimiter,
// converts the resulting string to lowercase, and then trims any trailing delimiters.
//
// If the resulting prefix exceeds the 'maxLength', it is truncated to that length.
//
// Note: The output of this function might be shorter than 'maxLength' due to the
// removal of the trailing delimiter. For example, if the joined string is "a-b-c"
// and maxLength is 4, the result will be "a-b" (after trimming the trailing "-"),
// rather than "a-b-".
func workloadPrefix(maxLength uint, values ...string) string {
	prefix := strings.ToLower(strings.Join(values, nameDelimiter))
	if len(prefix) > int(maxLength) {
		return prefix[:maxLength]
	}
	return prefix
}

// GetWorkloadNameForOwnerWithGVK returns a deterministic workload name for a given job.
func GetWorkloadNameForOwnerWithGVK(ownerName string, ownerUID types.UID, ownerGVK schema.GroupVersionKind) string {
	return workloadPrefix(uint(maxPrefixLength), ownerGVK.Kind, ownerName) + nameDelimiter +
		workloadSuffix(hashLength, ownerGVK.Kind, ownerGVK.Group, ownerName, string(ownerUID))
}
