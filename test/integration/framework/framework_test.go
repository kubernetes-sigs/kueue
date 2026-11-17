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

package framework

import (
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestIsExpectedIntegrationErrorLog(t *testing.T) {
	const (
		multiKueueLogger = "multikueue-multikueuecluster-reconciler"
		invalidPathError = "failed to load client config, reason: BadKubeConfig, error: kubeconfig path is not under the allowed prefix"
	)

	testCases := map[string]struct {
		loggerName string
		message    string
		name       string
		errLog     string
		want       bool
	}{
		"expected secret cleanup error": {
			loggerName: multiKueueLogger,
			message:    "Reconciler error",
			name:       "testing-cluster-secret-config",
			errLog:     `failed to update MultiKueueCluster status: multikueueclusters.kueue.x-k8s.io "testing-cluster-secret-config" not found after failing to load client config: Secret "testing-secret" not found`,
			want:       true,
		},
		"secret cleanup error from another cluster": {
			loggerName: multiKueueLogger,
			message:    "Reconciler error",
			name:       "another-cluster",
			errLog:     `failed to update MultiKueueCluster status: multikueueclusters.kueue.x-k8s.io "testing-cluster-secret-config" not found after failing to load client config: Secret "testing-secret" not found`,
		},
		"expected invalid path error": {
			loggerName: multiKueueLogger,
			message:    "loading client config failed",
			name:       "testing-cluster-invalid-path",
			errLog:     invalidPathError,
			want:       true,
		},
		"invalid path error from another cluster": {
			loggerName: multiKueueLogger,
			message:    "loading client config failed",
			name:       "another-cluster",
			errLog:     invalidPathError,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			entry := observer.LoggedEntry{
				Entry: zapcore.Entry{LoggerName: tc.loggerName, Message: tc.message},
				Context: []zapcore.Field{
					zap.String("name", tc.name),
					zap.String("error", tc.errLog),
				},
			}

			if got := isExpectedIntegrationErrorLog(entry); got != tc.want {
				t.Errorf("isExpectedIntegrationErrorLog() = %t, want %t", got, tc.want)
			}
		})
	}
}
