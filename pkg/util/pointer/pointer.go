/*
Copyright 2022 The Kubernetes Authors.

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

package pointer

import (
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/utils/pointer"
)

func Quantity(q resource.Quantity) *resource.Quantity {
	return &q
}

var (
	Int32 = pointer.Int32
	Int64 = pointer.Int64
	Bool  = pointer.Bool
)
