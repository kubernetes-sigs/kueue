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

package logging

import (
	"errors"
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestErrorWithValues(t *testing.T) {
	errLookup := errors.New("lookup failed")
	errNotFound := apierrors.NewNotFound(schema.GroupResource{Group: "kueue.x-k8s.io", Resource: "workloads"}, "wl")

	testCases := map[string]struct {
		err           error
		keysAndValues []any
		// wrap wraps the result of ErrorWithValues after message and identity checks.
		wrap         func(error) error
		wantErr      error
		wantNotFound bool
		// wantSame expects the error itself, without a wrapper.
		wantSame bool
	}{
		"nil error stays nil": {
			keysAndValues: []any{"job", "a"},
		},
		"error without values is returned as is": {
			err:      errLookup,
			wantErr:  errLookup,
			wantSame: true,
		},
		"error with values preserves message and identity": {
			err:           errLookup,
			keysAndValues: []any{"job", "a", "queue", "b"},
			wantErr:       errLookup,
		},
		"wrapped API error is still found": {
			err:           fmt.Errorf("getting Workload: %w", errNotFound),
			keysAndValues: []any{"job", "a"},
			wantErr:       errNotFound,
			wantNotFound:  true,
		},
		"errors.Is still matches after a later wrap": {
			err:           errLookup,
			keysAndValues: []any{"job", "a"},
			wrap: func(err error) error {
				return fmt.Errorf("reconciling: %w", err)
			},
			wantErr: errLookup,
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			got := ErrorWithValues(tc.err, tc.keysAndValues...)
			if got != nil && got.Error() != tc.err.Error() {
				t.Errorf("ErrorWithValues changed the message to %q, want %q", got.Error(), tc.err.Error())
			}
			if gotSame := got == tc.err; tc.err != nil && gotSame != tc.wantSame {
				t.Errorf("ErrorWithValues returned the error itself: %v, want %v", gotSame, tc.wantSame)
			}
			if tc.wrap != nil {
				got = tc.wrap(got)
			}
			if diff := cmp.Diff(tc.wantErr, got, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Unexpected error (-want +got):\n%s", diff)
			}
			// IsNotFound uses errors.As to find the API status in the chain.
			if gotNotFound := apierrors.IsNotFound(got); gotNotFound != tc.wantNotFound {
				t.Errorf("IsNotFound = %v, want %v", gotNotFound, tc.wantNotFound)
			}
		})
	}
}
