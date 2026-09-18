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
		ctx  context.Context
		want configapi.QuotaReleaseStrategy
	}{
		"empty context returns default OnTerminating": {
			ctx:  context.Background(),
			want: configapi.QuotaReleaseOnTerminating,
		},
		"context with OnTerminal": {
			ctx:  ContextWithQuotaReleaseStrategy(context.Background(), configapi.QuotaReleaseOnTerminal),
			want: configapi.QuotaReleaseOnTerminal,
		},
		"context with OnTerminating": {
			ctx:  ContextWithQuotaReleaseStrategy(context.Background(), configapi.QuotaReleaseOnTerminating),
			want: configapi.QuotaReleaseOnTerminating,
		},
		"context with empty strategy returns default OnTerminating": {
			ctx:  ContextWithQuotaReleaseStrategy(context.Background(), configapi.QuotaReleaseStrategy("")),
			want: configapi.QuotaReleaseOnTerminating,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := GetQuotaReleaseStrategy(tc.ctx)
			if got != tc.want {
				t.Errorf("GetQuotaReleaseStrategy() = %v, want %v", got, tc.want)
			}
		})
	}
}
