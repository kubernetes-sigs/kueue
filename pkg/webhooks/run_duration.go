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

package webhooks

import (
	"k8s.io/apimachinery/pkg/util/validation/field"

	"sigs.k8s.io/kueue/pkg/features"
)

func validateRunDurationSeconds(value, old *int32, path *field.Path) field.ErrorList {
	if value == nil {
		return nil
	}
	var errs field.ErrorList
	if *value < 1 {
		errs = append(errs, field.Invalid(path, *value, "must be positive"))
	}
	if !features.Enabled(features.WorkloadRunDuration) && (old == nil || *old != *value) {
		errs = append(errs, field.Forbidden(path, "requires the WorkloadRunDuration feature gate"))
	}
	return errs
}
