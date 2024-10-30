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

package parser

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"k8s.io/utils/ptr"
)

func TestParseTimeLimit(t *testing.T) {
	testCases := map[string]struct {
		val     string
		want    *int32
		wantErr error
	}{
		"empty value": {},
		"-1": {
			val: "-1",
		},
		"INFINITE": {
			val: "INFINITE",
		},
		"UNLIMITED": {
			val: "UNLIMITED",
		},
		"infinite": {
			val: "infinite",
		},
		"unlimited": {
			val: "unlimited",
		},
		"incomplete formats 12-": {
			val:     "12-",
			wantErr: invalidTimeFormatErr,
		},
		"incomplete formats 12:": {
			val:     "12:",
			wantErr: invalidTimeFormatErr,
		},
		"incomplete formats 12-05:": {
			val:     "12-05:",
			wantErr: invalidTimeFormatErr,
		},
		"single digit minimal values 0": {
			val: "0",
		},
		"single digit minimal values 0:0:0": {
			val: "0:0:0",
		},
		"single digit minimal values 0-0:0:0": {
			val: "0-0:0:0",
		},
		"single digit minimal values 1-0:0:0": {
			val:  "1-0:0:0",
			want: ptr.To[int32](24 * 60 * 60),
		},
		"not supported chars": {
			val:     "12-0m-23",
			wantErr: invalidTimeFormatErr,
		},
		"more than one dashes": {
			val:     "12-05-23",
			wantErr: invalidTimeFormatErr,
		},
		"more than two colons": {
			val:     "2:12:05:23",
			wantErr: invalidTimeFormatErr,
		},
		"minutes": {
			val:  "05",
			want: ptr.To[int32](5 * 60),
		},
		"minutes:seconds": {
			val:  "05:23",
			want: ptr.To[int32](5*60 + 23),
		},
		"hours:minutes:seconds": {
			val:  "12:05:23",
			want: ptr.To[int32](12*60*60 + 5*60 + 23),
		},
		"days-hours:minutes:seconds": {
			val:  "2-12:05:23",
			want: ptr.To[int32](2*24*60*60 + 12*60*60 + 5*60 + 23),
		},
		"days-hours": {
			val:  "02-12",
			want: ptr.To[int32](2*24*60*60 + 12*60*60),
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			got, gotErr := TimeLimitToSeconds(tc.val)

			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Unexpected error (-want/+got)\n%s", diff)
				return
			}

			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Unexpected resutl (-want/+got)\n%s", diff)
			}
		})
	}
}
