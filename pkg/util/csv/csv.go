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

<<<<<<<< HEAD:pkg/util/csv/csv.go
package csv

import "strings"

// Parse parses a string containing comma-separated names and removes
// leading and trailing spaces around names.
func Parse(s string) []string {
	if strings.TrimSpace(s) == "" {
		return []string{}
	}
	parts := strings.Split(s, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// Serialize joins a slice of strings with commas.
func Serialize(elems []string) string {
	return strings.Join(elems, ",")
========
package internalversion

import (
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/conversion"
)

func Convert_v1_ListOptions_To_internalversion_ListOptions(in *v1.ListOptions, out *ListOptions, s conversion.Scope) error {
	return autoConvert_v1_ListOptions_To_internalversion_ListOptions(in, out, s)
}

func Convert_internalversion_ListOptions_To_v1_ListOptions(in *ListOptions, out *v1.ListOptions, s conversion.Scope) error {
	return autoConvert_internalversion_ListOptions_To_v1_ListOptions(in, out, s)
>>>>>>>> a227e98c2 (vendor):vendor/k8s.io/apimachinery/pkg/apis/meta/internalversion/conversion.go
}
