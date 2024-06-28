/*
Copyright 2024 The Kubernetes Authors.

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

package wrappers

import (
	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/apis/v1alpha1"
)

// SupportedModeWrapper wraps a SupportedMode.
type SupportedModeWrapper struct{ v1alpha1.SupportedMode }

// MakeSupportedMode creates a wrapper for a SupportedMode
func MakeSupportedMode(name v1alpha1.ApplicationProfileMode, template v1alpha1.TemplateReference) *SupportedModeWrapper {
	return &SupportedModeWrapper{
		SupportedMode: v1alpha1.SupportedMode{
			Name:     name,
			Template: template,
		},
	}
}

// Obj returns the inner SupportedMode.
func (m *SupportedModeWrapper) Obj() *v1alpha1.SupportedMode {
	return &m.SupportedMode
}
