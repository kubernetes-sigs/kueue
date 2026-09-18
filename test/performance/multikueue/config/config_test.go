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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	"sigs.k8s.io/kueue/test/performance/multikueue/report"
)

func TestConfigValidate(t *testing.T) {
	testCases := map[string]struct {
		config  Config
		wantErr string
	}{
		"valid": {
			config: makeConfig().Obj(),
		},
		"maximum workloads": {
			config: makeConfig().WorkloadCount(maxWorkloadCount).Obj(),
		},
		"too many workloads": {
			config:  makeConfig().WorkloadCount(maxWorkloadCount + 1).Obj(),
			wantErr: "workloadCount must not exceed 10000",
		},
		"invalid CPU": {
			config:  makeConfig().CPURequest("not-a-quantity").Obj(),
			wantErr: "cpuRequest must be a valid resource quantity",
		},
		"zero CPU": {
			config:  makeConfig().CPURequest("0").Obj(),
			wantErr: "cpuRequest must be positive",
		},
		"unsupported dispatcher": {
			config:  makeConfig().Dispatcher(configapi.MultiKueueDispatcherModeIncremental).Obj(),
			wantErr: "dispatcher must be",
		},
		"missing dispatcher": {
			config:  makeConfig().Dispatcher("").Obj(),
			wantErr: "dispatcher must be",
		},
		"missing workloads": {
			config:  makeConfig().WorkloadCount(0).Obj(),
			wantErr: "workloadCount must be positive",
		},
		"negative workloads": {
			config:  makeConfig().WorkloadCount(-1).Obj(),
			wantErr: "workloadCount must be positive",
		},
		"missing worker clusters": {
			config:  makeConfig().WorkerClusters(0).Obj(),
			wantErr: "workerClusters must be positive",
		},
		"negative worker clusters": {
			config:  makeConfig().WorkerClusters(-1).Obj(),
			wantErr: "workerClusters must be positive",
		},
		"missing creation workers": {
			config:  makeConfig().CreationWorkers(0).Obj(),
			wantErr: "creationWorkers must be positive",
		},
		"negative creation workers": {
			config:  makeConfig().CreationWorkers(-1).Obj(),
			wantErr: "creationWorkers must be positive",
		},
		"missing local QPS": {
			config:  makeConfig().LocalClientQPS(0).Obj(),
			wantErr: "localClientQPS must be positive",
		},
		"negative local QPS": {
			config:  makeConfig().LocalClientQPS(-1).Obj(),
			wantErr: "localClientQPS must be positive",
		},
		"missing local burst": {
			config:  makeConfig().LocalClientBurst(0).Obj(),
			wantErr: "localClientBurst must be positive",
		},
		"negative local burst": {
			config:  makeConfig().LocalClientBurst(-1).Obj(),
			wantErr: "localClientBurst must be positive",
		},
		"missing remote QPS": {
			config:  makeConfig().RemoteClientQPS(0).Obj(),
			wantErr: "remoteClientQPS must be positive",
		},
		"negative remote QPS": {
			config:  makeConfig().RemoteClientQPS(-1).Obj(),
			wantErr: "remoteClientQPS must be positive",
		},
		"missing remote burst": {
			config:  makeConfig().RemoteClientBurst(0).Obj(),
			wantErr: "remoteClientBurst must be positive",
		},
		"negative remote burst": {
			config:  makeConfig().RemoteClientBurst(-1).Obj(),
			wantErr: "remoteClientBurst must be positive",
		},
		"missing workload concurrency": {
			config:  makeConfig().WorkloadConcurrency(0).Obj(),
			wantErr: "workloadConcurrency must be positive",
		},
		"negative workload concurrency": {
			config:  makeConfig().WorkloadConcurrency(-1).Obj(),
			wantErr: "workloadConcurrency must be positive",
		},
		"missing GC interval": {
			config:  makeConfig().GCInterval(0).Obj(),
			wantErr: "gcInterval must be positive",
		},
		"negative GC interval": {
			config:  makeConfig().GCInterval(-1).Obj(),
			wantErr: "gcInterval must be positive",
		},
		"missing worker lost timeout": {
			config:  makeConfig().WorkerLostTimeout(0).Obj(),
			wantErr: "workerLostTimeout must be positive",
		},
		"negative worker lost timeout": {
			config:  makeConfig().WorkerLostTimeout(-1).Obj(),
			wantErr: "workerLostTimeout must be positive",
		},
		"missing events batch period": {
			config:  makeConfig().EventsBatchPeriod(0).Obj(),
			wantErr: "eventsBatchPeriod must be positive",
		},
		"negative events batch period": {
			config:  makeConfig().EventsBatchPeriod(-1).Obj(),
			wantErr: "eventsBatchPeriod must be positive",
		},
		"missing timeout": {
			config:  makeConfig().Timeout(0).Obj(),
			wantErr: "timeout must be positive",
		},
		"negative timeout": {
			config:  makeConfig().Timeout(-1).Obj(),
			wantErr: "timeout must be positive",
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			err := tc.config.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() unexpected error: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Validate() error = %v, want one containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestLoad(t *testing.T) {
	const configurationYAML = `workloadCount: 200
workerClusters: 4
creationWorkers: 2
cpuRequest: 2m
localClientQPS: 431.5
localClientBurst: 701
remoteClientQPS: 731.5
remoteClientBurst: 997
workloadConcurrency: 6
dispatcher: kueue.x-k8s.io/multikueue-dispatcher-all-at-once
gcInterval: 2m
workerLostTimeout: 7m
eventsBatchPeriod: 3s
timeout: 20m
`
	testCases := map[string]struct {
		data    string
		want    Config
		wantErr string
	}{
		"explicit configuration": {
			data: configurationYAML,
			want: Config{
				WorkloadCount:       200,
				WorkerClusters:      4,
				CreationWorkers:     2,
				CPURequest:          "2m",
				LocalClientQPS:      431.5,
				LocalClientBurst:    701,
				RemoteClientQPS:     731.5,
				RemoteClientBurst:   997,
				WorkloadConcurrency: 6,
				Dispatcher:          configapi.MultiKueueDispatcherModeAllAtOnce,
				GCInterval:          metav1.Duration{Duration: 2 * time.Minute},
				WorkerLostTimeout:   metav1.Duration{Duration: 7 * time.Minute},
				EventsBatchPeriod:   metav1.Duration{Duration: 3 * time.Second},
				Timeout:             metav1.Duration{Duration: 20 * time.Minute},
			},
		},
		"expectations are not configuration": {
			data:    configurationYAML + "minThroughputPerSecond: 75\n",
			wantErr: `unknown field "minThroughputPerSecond"`,
		},
		"scope is a fixed contract": {
			data:    configurationYAML + "remoteClientRateLimitScope: worker-cluster\n",
			wantErr: `unknown field "remoteClientRateLimitScope"`,
		},
		"missing controller configuration": {
			data:    strings.Replace(configurationYAML, "gcInterval: 2m\n", "", 1),
			wantErr: "gcInterval must be positive",
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "configuration.yaml")
			if err := os.WriteFile(path, []byte(tc.data), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := Load(path)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Load() error = %v, want one containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() unexpected error: %v", err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Load() mismatch (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestConfigScenario(t *testing.T) {
	config := makeConfig().WorkloadCount(200).WorkerClusters(4).CreationWorkers(2).
		CPURequest("2m").LocalClientQPS(431.5).LocalClientBurst(701).
		RemoteClientQPS(731.5).RemoteClientBurst(997).WorkloadConcurrency(6).
		GCInterval(2 * time.Minute).WorkerLostTimeout(7 * time.Minute).EventsBatchPeriod(3 * time.Second).
		Timeout(20 * time.Minute).Obj()
	want := report.Scenario{
		RemoteClientRateLimitScope: "worker-cluster",
		WorkloadCount:              200,
		WorkerClusters:             4,
		CreationWorkers:            2,
		CPURequest:                 "2m",
		Dispatcher:                 "kueue.x-k8s.io/multikueue-dispatcher-all-at-once",
		WorkloadConcurrency:        6,
		GCInterval:                 "2m0s",
		WorkerLostTimeout:          "7m0s",
		EventsBatchPeriod:          "3s",
		LocalClientQPS:             431.5,
		LocalClientBurst:           701,
		RemoteClientQPS:            731.5,
		RemoteClientBurst:          997,
	}
	if diff := cmp.Diff(want, config.Scenario()); diff != "" {
		t.Errorf("Scenario() mismatch (-want,+got):\n%s", diff)
	}
}

func TestCommittedConfiguration(t *testing.T) {
	if _, err := Load(filepath.Join("..", "configs", "baseline", "configuration.yaml")); err != nil {
		t.Fatalf("Load committed configuration: %v", err)
	}
}

type configWrapper struct{ config Config }

func makeConfig() configWrapper {
	return configWrapper{config: Config{
		WorkloadCount:       1000,
		WorkerClusters:      3,
		CreationWorkers:     1,
		CPURequest:          "1m",
		LocalClientQPS:      1000,
		LocalClientBurst:    1000,
		RemoteClientQPS:     1000,
		RemoteClientBurst:   1000,
		WorkloadConcurrency: 10,
		Dispatcher:          configapi.MultiKueueDispatcherModeAllAtOnce,
		GCInterval:          metav1.Duration{Duration: time.Minute},
		WorkerLostTimeout:   metav1.Duration{Duration: 15 * time.Minute},
		EventsBatchPeriod:   metav1.Duration{Duration: time.Second},
		Timeout:             metav1.Duration{Duration: 10 * time.Minute},
	}}
}

func (w configWrapper) Obj() Config { return w.config }

func (w configWrapper) WorkloadCount(value int) configWrapper {
	w.config.WorkloadCount = value
	return w
}

func (w configWrapper) WorkerClusters(value int) configWrapper {
	w.config.WorkerClusters = value
	return w
}

func (w configWrapper) CreationWorkers(value int) configWrapper {
	w.config.CreationWorkers = value
	return w
}

func (w configWrapper) LocalClientQPS(value float32) configWrapper {
	w.config.LocalClientQPS = value
	return w
}

func (w configWrapper) LocalClientBurst(value int32) configWrapper {
	w.config.LocalClientBurst = value
	return w
}

func (w configWrapper) RemoteClientQPS(value float32) configWrapper {
	w.config.RemoteClientQPS = value
	return w
}

func (w configWrapper) RemoteClientBurst(value int32) configWrapper {
	w.config.RemoteClientBurst = value
	return w
}

func (w configWrapper) WorkloadConcurrency(value int) configWrapper {
	w.config.WorkloadConcurrency = value
	return w
}

func (w configWrapper) GCInterval(value time.Duration) configWrapper {
	w.config.GCInterval.Duration = value
	return w
}

func (w configWrapper) WorkerLostTimeout(value time.Duration) configWrapper {
	w.config.WorkerLostTimeout.Duration = value
	return w
}

func (w configWrapper) EventsBatchPeriod(value time.Duration) configWrapper {
	w.config.EventsBatchPeriod.Duration = value
	return w
}

func (w configWrapper) Dispatcher(value string) configWrapper {
	w.config.Dispatcher = value
	return w
}

func (w configWrapper) CPURequest(value string) configWrapper {
	w.config.CPURequest = value
	return w
}

func (w configWrapper) Timeout(value time.Duration) configWrapper {
	w.config.Timeout.Duration = value
	return w
}
