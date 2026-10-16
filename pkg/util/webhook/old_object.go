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

package webhook

import (
	"context"
	"encoding/json"

	admissionv1 "k8s.io/api/admission/v1"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// OldObjectFromContext returns the object as it was before the update when ctx
// carries an UPDATE admission request, and nil otherwise. It lets a defaulter,
// which only receives the new object, see the old one.
func OldObjectFromContext[T any](ctx context.Context) (*T, error) {
	if req, err := admission.RequestFromContext(ctx); err == nil && req.Operation == admissionv1.Update {
		oldObj := new(T)
		if err := json.Unmarshal(req.OldObject.Raw, oldObj); err != nil {
			return nil, err
		}
		return oldObj, nil
	}
	return nil, nil
}
