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

package config

import (
	"testing"
)

func TestConfigurationValidate(t *testing.T) {
	cases := map[string]struct {
		cfg     Configuration
		wantErr bool
	}{
		"default config is valid": {
			cfg:     Default(),
			wantErr: false,
		},
		"static mode with localQueueName is valid": {
			cfg: Configuration{
				LocalQueueName:     "my-queue",
				LocalQueueNameMode: LocalQueueNameModeStatic,
			},
			wantErr: false,
		},
		"empty mode with localQueueName is valid": {
			cfg: Configuration{
				LocalQueueName: "my-queue",
			},
			wantErr: false,
		},
		"AsClusterQueue mode without localQueueName is valid": {
			cfg: Configuration{
				LocalQueueNameMode: LocalQueueNameModeAsClusterQueue,
			},
			wantErr: false,
		},
		"AsClusterQueue mode with localQueueName is invalid": {
			cfg: Configuration{
				LocalQueueName:     "my-queue",
				LocalQueueNameMode: LocalQueueNameModeAsClusterQueue,
			},
			wantErr: true,
		},
		"unsupported mode is invalid": {
			cfg: Configuration{
				LocalQueueNameMode: "SomethingElse",
			},
			wantErr: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			errs := tc.cfg.Validate()
			if tc.wantErr && len(errs) == 0 {
				t.Errorf("expected validation errors but got none")
			}
			if !tc.wantErr && len(errs) > 0 {
				t.Errorf("expected no validation errors but got: %v", errs.ToAggregate())
			}
		})
	}
}
