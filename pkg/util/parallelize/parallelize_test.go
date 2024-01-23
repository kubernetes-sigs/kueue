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

package parallelize

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
)

var errEven = errors.New("even")

func TestUntil(t *testing.T) {
	var result []int
	cases := map[string]struct {
		op         func(int) error
		wantOutput []int
		wantErr    error
	}{
		"double the index": {
			op: func(i int) error {
				result[i] = 2 * i
				return nil
			},
			wantOutput: []int{0, 2, 4, 6, 8},
		},
		"error in even numbers": {
			op: func(i int) error {
				if i%2 == 0 {
					return errEven
				}
				return nil
			},
			wantOutput: make([]int, 5),
			wantErr:    errEven,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			result = make([]int, 5)
			err := Until(context.Background(), len(result), tc.op)
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("Got error %q, want %q", err, tc.wantErr)
			}
			if diff := cmp.Diff(tc.wantOutput, result); diff != "" {
				t.Errorf("Processed result (-want,+got):\n%s", diff)
			}
		})
	}
}
