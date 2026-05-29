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

package indexer

import (
	"context"
	"testing"

	resourceapi "k8s.io/api/resource/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/pkg/features"
)

// fakeFieldIndexer implements client.FieldIndexer for testing Setup().
// It returns noMatchErr for DeviceClass objects when set, simulating a cluster
// where the DeviceClass API is not available.
type fakeFieldIndexer struct {
	noMatchErr error
}

func (f *fakeFieldIndexer) IndexField(_ context.Context, obj client.Object, _ string, _ client.IndexerFunc) error {
	if _, ok := obj.(*resourceapi.DeviceClass); ok && f.noMatchErr != nil {
		return f.noMatchErr
	}
	return nil
}

func TestSetupToleratesNoMatchErrorForDeviceClass(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.DRAExtendedResources, true)

	noMatchErr := &apimeta.NoKindMatchError{
		GroupKind:        schema.GroupKind{Group: "resource.k8s.io", Kind: "DeviceClass"},
		SearchedVersions: []string{"v1"},
	}

	cases := map[string]struct {
		indexer *fakeFieldIndexer
		wantErr bool
	}{
		"DeviceClass API available": {
			indexer: &fakeFieldIndexer{},
			wantErr: false,
		},
		"DeviceClass API not available (NoKindMatchError)": {
			indexer: &fakeFieldIndexer{noMatchErr: noMatchErr},
			wantErr: false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := Setup(t.Context(), tc.indexer)
			if (err != nil) != tc.wantErr {
				t.Errorf("Setup() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}
