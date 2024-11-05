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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/apis/v1alpha1"
)

// ApplicationProfileWrapper wraps an ApplicationProfile.
type ApplicationProfileWrapper struct{ v1alpha1.ApplicationProfile }

// MakeApplicationProfile creates a wrapper for an ApplicationProfile
func MakeApplicationProfile(name, ns string) *ApplicationProfileWrapper {
	return &ApplicationProfileWrapper{
		ApplicationProfile: v1alpha1.ApplicationProfile{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: ns,
			},
			Spec: v1alpha1.ApplicationProfileSpec{
				SupportedModes: nil,
				VolumeBundles:  nil,
			},
		},
	}
}

// Obj returns the inner ApplicationProfile.
func (ap *ApplicationProfileWrapper) Obj() *v1alpha1.ApplicationProfile {
	return &ap.ApplicationProfile
}

// WithSupportedMode adds the SupportedMode
func (ap *ApplicationProfileWrapper) WithSupportedMode(supportedMode v1alpha1.SupportedMode) *ApplicationProfileWrapper {
	ap.Spec.SupportedModes = append(ap.Spec.SupportedModes, supportedMode)
	return ap
}

// WithVolumeBundleReferences adds WithVolumeBundleReferences to VolumeBundles.
func (ap *ApplicationProfileWrapper) WithVolumeBundleReferences(volumeBundleReferences ...v1alpha1.VolumeBundleReference) *ApplicationProfileWrapper {
	ap.Spec.VolumeBundles = append(ap.Spec.VolumeBundles, volumeBundleReferences...)
	return ap
}
