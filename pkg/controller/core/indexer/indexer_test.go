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

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
)

// helpers to build test objects without importing any package that transitively
// imports this (indexer) package, which would create an import cycle.

func makeWorkload(name, ns string) *kueue.Workload {
	return &kueue.Workload{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
}

func makeLimitRange(name, ns string, types ...corev1.LimitType) *corev1.LimitRange {
	items := make([]corev1.LimitRangeItem, len(types))
	for i, t := range types {
		items[i] = corev1.LimitRangeItem{Type: t}
	}
	return &corev1.LimitRange{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       corev1.LimitRangeSpec{Limits: items},
	}
}

func TestIndexLimitRangeHasContainerOrPodType(t *testing.T) {
	cases := map[string]struct {
		obj  client.Object
		want []string
	}{
		"non-LimitRange returns nil": {
			obj:  makeWorkload("wl", "ns"),
			want: nil,
		},
		"LimitRange with no limits returns nil": {
			obj:  makeLimitRange("lr", "ns"),
			want: nil,
		},
		"LimitRange with only Pod type returns nil": {
			obj:  makeLimitRange("lr", "ns", corev1.LimitTypePod),
			want: []string{"true"},
		},
		"LimitRange with Container type returns true": {
			obj:  makeLimitRange("lr", "ns", corev1.LimitTypeContainer),
			want: []string{"true"},
		},
		"LimitRange with both Pod and Container types returns true": {
			obj:  makeLimitRange("lr", "ns", corev1.LimitTypePod, corev1.LimitTypeContainer),
			want: []string{"true"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := IndexLimitRangeHasContainerOrPodType(tc.obj)
			if diff := cmp.Diff(tc.want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

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
