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

package pod

import (
	"testing"

	"github.com/go-logr/zapr"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

type podListLogEntry struct {
	Message   string
	Namespace string
}

func TestListPodsLogsEndForEachNamespace(t *testing.T) {
	cases := map[string]struct {
		namespaces []string
		wantError  error
		wantLogs   []podListLogEntry
	}{
		"lists all namespaces": {
			namespaces: []string{"ns-a", "ns-b"},
			wantLogs: []podListLogEntry{
				{Message: "Begin pods list", Namespace: "ns-a"},
				{Message: "End pods list", Namespace: "ns-a"},
				{Message: "Begin pods list", Namespace: "ns-b"},
				{Message: "End pods list", Namespace: "ns-b"},
			},
		},
		"logs the end when listing fails": {
			namespaces: []string{"ns-a"},
			wantError:  errPodList,
			wantLogs: []podListLogEntry{
				{Message: "Begin pods list", Namespace: "ns-a"},
				{Message: "End pods list", Namespace: "ns-a"},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			core, observed := observer.New(zapcore.Level(-3))
			ctx := ctrl.LoggerInto(t.Context(), zapr.NewLogger(zap.New(core)))
			builder := utiltesting.NewClientBuilder()
			if tc.wantError != nil {
				builder = builder.WithInterceptorFuncs(failPagedPodList(tc.wantError))
			}

			err := ListPods(ctx, builder.Build(), tc.namespaces, make(chan corev1.Pod))
			if diff := cmp.Diff(tc.wantError, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("ListPods() error mismatch (-want/+got):\n%s", diff)
			}

			var got []podListLogEntry
			for _, entry := range observed.All() {
				if entry.Message == "Begin pods list" || entry.Message == "End pods list" {
					got = append(got, podListLogEntry{
						Message:   entry.Message,
						Namespace: entry.ContextMap()["namespace"].(string),
					})
				}
			}
			if diff := cmp.Diff(tc.wantLogs, got); diff != "" {
				t.Errorf("Log entries mismatch (-want/+got):\n%s", diff)
			}
		})
	}
}
