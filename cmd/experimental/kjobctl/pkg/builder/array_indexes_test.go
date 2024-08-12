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

package builder

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"k8s.io/utils/ptr"
)

func TestParseSlurmArrayIndexes(t *testing.T) {
	testCases := map[string]struct {
		array            string
		wantArrayIndexes arrayIndexes
		wantErr          string
	}{
		"shouldn't parse 0": {
			array:   "0",
			wantErr: invalidArrayFlagFormatErr.Error(),
		},
		"should parse 1": {
			array: "1",
			wantArrayIndexes: arrayIndexes{
				Indexes: []int32{1},
			},
		},
		"should parse 1,4,5": {
			array: "1,4,5",
			wantArrayIndexes: arrayIndexes{
				Indexes: []int32{1, 4, 5},
			},
		},
		"should parse 1,1,4,5, because duplicate indexes": {
			array:   "1,1,4,5",
			wantErr: invalidArrayFlagFormatErr.Error(),
		},
		"shouldn't parse 4,1,5, because invalid sequence": {
			array:   "4,1,5",
			wantErr: invalidArrayFlagFormatErr.Error(),
		},
		"should parse 1-5": {
			array: "1-5",
			wantArrayIndexes: arrayIndexes{
				Indexes: []int32{1, 2, 3, 4, 5},
			},
		},
		"shouldn't parse 5-1, because from > to": {
			array:   "5-1",
			wantErr: invalidArrayFlagFormatErr.Error(),
		},
		"should parse 3-9:3": {
			array: "3-9:3",
			wantArrayIndexes: arrayIndexes{
				Indexes: []int32{3, 6, 9},
				Step:    ptr.To[int32](3),
			},
		},
		"should parse 1-5%2": {
			array: "1-5%2",
			wantArrayIndexes: arrayIndexes{
				Indexes:     []int32{1, 2, 3, 4, 5},
				Parallelism: ptr.To[int32](2),
			},
		},
		"shouldn't parse 1-5?2": {
			array:   "1-5?2",
			wantErr: invalidArrayFlagFormatErr.Error(),
		},
		"shouldn't parse 1-5:2147483648, because value out of range": {
			array:   "1-5:2147483648",
			wantErr: invalidArrayFlagFormatErr.Error(),
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			gotArrayIndexes, gotErr := parseArrayIndexes(tc.array)

			var gotErrStr string
			if gotErr != nil {
				gotErrStr = gotErr.Error()
			}
			if diff := cmp.Diff(tc.wantErr, gotErrStr); diff != "" {
				t.Errorf("Unexpected error (-want/+got)\n%s", diff)
				return
			}

			if diff := cmp.Diff(tc.wantArrayIndexes, gotArrayIndexes); diff != "" {
				t.Errorf("Unexpected array indexes (-want/+got)\n%s", diff)
			}
		})
	}
}
