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

package jobframework

import (
	"context"
	"testing"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
)

func TestGetQuotaReleaseStrategy(t *testing.T) {
	cases := map[string]struct {
		setContext func(ctx context.Context) context.Context
		want       configapi.QuotaReleaseStrategy
	}{
		"empty context returns default OnTerminating": {
			want: configapi.QuotaReleaseOnTerminating,
		},
		"context with OnTerminal": {
			setContext: func(ctx context.Context) context.Context {
				return ContextWithQuotaReleaseStrategy(ctx, configapi.QuotaReleaseOnTerminal)
			},
			want: configapi.QuotaReleaseOnTerminal,
		},
		"context with OnTerminating": {
			setContext: func(ctx context.Context) context.Context {
				return ContextWithQuotaReleaseStrategy(ctx, configapi.QuotaReleaseOnTerminating)
			},
			want: configapi.QuotaReleaseOnTerminating,
		},
		"context with empty strategy returns default OnTerminating": {
			setContext: func(ctx context.Context) context.Context {
				return ContextWithQuotaReleaseStrategy(ctx, configapi.QuotaReleaseStrategy(""))
			},
			want: configapi.QuotaReleaseOnTerminating,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			if tc.setContext != nil {
				ctx = tc.setContext(ctx)
			}
			got := GetQuotaReleaseStrategy(ctx)
			if got != tc.want {
				t.Errorf("GetQuotaReleaseStrategy() = %v, want %v", got, tc.want)
			}
		})
	}
}
