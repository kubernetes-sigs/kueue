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

package kubeversion

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"k8s.io/apimachinery/pkg/runtime"
	versionutil "k8s.io/apimachinery/pkg/util/version"
	"k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	fakeclientset "k8s.io/client-go/kubernetes/fake"
	kubetesting "k8s.io/client-go/testing"
)

// FetchServerVersion gets API server version
func TestFetchServerVersion(t *testing.T) {
	fakeClient := fakeclientset.NewSimpleClientset()
	fakeDiscovery, ok := fakeClient.Discovery().(*fakediscovery.FakeDiscovery)
	if !ok {
		t.Fatalf("couldn't convert Discovery() to *FakeDiscovery")
	}
	fakeDiscovery.FakedServerVersion = &version.Info{
		GitVersion: "v1.0.0",
	}

	fetcher := NewServerVersionFetcher(fakeDiscovery)
	_ = fetcher.FetchServerVersion()
	wanted := versionutil.MustParseGeneric("v1.0.0").String()
	if fetcher.serverVersion.String() != wanted {
		t.Errorf("Unexpected result, want %v", wanted)
	}
}

func TestFetchServerVersionWithError(t *testing.T) {
	expectedError := errors.New("an error occurred")

	fakeClient := fakeclientset.NewSimpleClientset()
	fakeDiscovery, ok := fakeClient.Discovery().(*fakediscovery.FakeDiscovery)
	if !ok {
		t.Fatalf("couldn't convert Discovery() to *FakeDiscovery")
	}
	fakeDiscovery.PrependReactor("*", "*", func(action kubetesting.Action) (handled bool, ret runtime.Object, err error) {
		return true, nil, expectedError
	})
	err := NewServerVersionFetcher(fakeDiscovery).FetchServerVersion()
	if diff := cmp.Diff(expectedError, err, cmpopts.EquateErrors()); diff != "" {
		t.Errorf("Unexpected result (-want,+got):\n%s", diff)
	}
}
