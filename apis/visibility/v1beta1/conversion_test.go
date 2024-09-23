/*
Copyright 2023 The Kubernetes Authors.

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

package v1beta1

import (
	"net/url"
	"testing"

	"github.com/google/go-cmp/cmp"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/diff"
)

func TestPendingWorkloadsOptions(t *testing.T) {
	scheme := runtime.NewScheme()
	err := AddToScheme(scheme)
	if err != nil {
		t.Fatal(err)
	}
	codec := runtime.NewParameterCodec(scheme)

	cases := map[string]struct {
		inputQueryParams           url.Values
		wantQueryParams            url.Values
		wantPendingWorkloadOptions PendingWorkloadOptions
	}{
		"correct parameters": {
			inputQueryParams: url.Values{
				"limit":  {"1"},
				"offset": {"2"},
			},
			wantQueryParams: url.Values{
				"limit":  {"1"},
				"offset": {"2"},
			},
			wantPendingWorkloadOptions: PendingWorkloadOptions{
				Limit:  1,
				Offset: 2,
			},
		},
		"default values": {
			inputQueryParams: url.Values{
				"limit":  {"0"},
				"offset": {"0"},
			},
			wantQueryParams: url.Values{
				"limit":  {"1000"},
				"offset": {"0"},
			},
			wantPendingWorkloadOptions: PendingWorkloadOptions{
				Limit:  1000,
				Offset: 0,
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// versioned -> query params
			actualParameters, err := codec.EncodeParameters(&tc.wantPendingWorkloadOptions, SchemeGroupVersion)
			if err != nil {
				t.Fatal(err)
			}
			if d := cmp.Diff(actualParameters, tc.wantQueryParams); d != "" {
				t.Fatalf("Unexpected serialization:\n%s", diff.ObjectGoPrintSideBySide(tc.wantQueryParams, actualParameters))
			}

			// query params -> versioned
			convertedPendingWorkloadOptions := PendingWorkloadOptions{}
			err = codec.DecodeParameters(tc.inputQueryParams, SchemeGroupVersion, &convertedPendingWorkloadOptions)
			if err != nil {
				t.Fatal(err)
			}
			if d := cmp.Diff(convertedPendingWorkloadOptions, tc.wantPendingWorkloadOptions); d != "" {
				t.Fatalf("Unexpected deserialization:\n%s", diff.ObjectGoPrintSideBySide(tc.wantPendingWorkloadOptions, convertedPendingWorkloadOptions))
			}
		})
	}
}
