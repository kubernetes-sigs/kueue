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

package sparkapplication

import (
	"testing"

	sparkv1beta2 "github.com/kubeflow/spark-operator/v2/api/v1beta2"
	sparkcommon "github.com/kubeflow/spark-operator/v2/pkg/common"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func executorPod(containerName string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				sparkcommon.LabelSparkRole: sparkcommon.SparkRoleExecutor,
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: containerName},
			},
		},
	}
}

func TestSparkPodResources(t *testing.T) {
	scalaApp := func(driver sparkv1beta2.DriverSpec, executor sparkv1beta2.ExecutorSpec) *sparkv1beta2.SparkApplication {
		return &sparkv1beta2.SparkApplication{
			Spec: sparkv1beta2.SparkApplicationSpec{
				Type:     sparkv1beta2.SparkApplicationTypeScala,
				Driver:   driver,
				Executor: executor,
			},
		}
	}

	tests := map[string]struct {
		pod        *corev1.Pod
		app        *sparkv1beta2.SparkApplication
		wantCPU    string
		wantMemory string
		wantErr    bool
	}{
		"driver defaults to 1 core and 1g plus the minimum overhead": {
			pod:        driverPod(sparkcommon.SparkDriverContainerName),
			app:        scalaApp(sparkv1beta2.DriverSpec{}, sparkv1beta2.ExecutorSpec{}),
			wantCPU:    "1",
			wantMemory: "1408Mi",
		},
		"executor defaults to 1 core and 1g plus the minimum overhead": {
			pod:        executorPod(sparkcommon.Spark3DefaultExecutorContainerName),
			app:        scalaApp(sparkv1beta2.DriverSpec{}, sparkv1beta2.ExecutorSpec{}),
			wantCPU:    "1",
			wantMemory: "1408Mi",
		},
		"cores is used as the CPU request when coreRequest is unset": {
			pod: driverPod(sparkcommon.SparkDriverContainerName),
			app: scalaApp(sparkv1beta2.DriverSpec{
				SparkPodSpec: sparkv1beta2.SparkPodSpec{Cores: new(int32(4))},
			}, sparkv1beta2.ExecutorSpec{}),
			wantCPU:    "4",
			wantMemory: "1408Mi",
		},
		"coreRequest takes precedence over cores": {
			pod: driverPod(sparkcommon.SparkDriverContainerName),
			app: scalaApp(sparkv1beta2.DriverSpec{
				SparkPodSpec: sparkv1beta2.SparkPodSpec{Cores: new(int32(4))},
				CoreRequest:  new("500m"),
			}, sparkv1beta2.ExecutorSpec{}),
			wantCPU:    "500m",
			wantMemory: "1408Mi",
		},
		"request.cores from sparkConf takes precedence over cores": {
			pod: executorPod(sparkcommon.Spark3DefaultExecutorContainerName),
			app: func() *sparkv1beta2.SparkApplication {
				app := scalaApp(sparkv1beta2.DriverSpec{}, sparkv1beta2.ExecutorSpec{
					SparkPodSpec: sparkv1beta2.SparkPodSpec{Cores: new(int32(4))},
				})
				app.Spec.SparkConf = map[string]string{
					sparkcommon.SparkKubernetesExecutorRequestCores: "2500m",
				}
				return app
			}(),
			wantCPU:    "2500m",
			wantMemory: "1408Mi",
		},
		"coreRequest takes precedence over request.cores from sparkConf": {
			pod: executorPod(sparkcommon.Spark3DefaultExecutorContainerName),
			app: func() *sparkv1beta2.SparkApplication {
				app := scalaApp(sparkv1beta2.DriverSpec{}, sparkv1beta2.ExecutorSpec{
					CoreRequest: new("500m"),
				})
				app.Spec.SparkConf = map[string]string{
					sparkcommon.SparkKubernetesExecutorRequestCores: "2500m",
				}
				return app
			}(),
			wantCPU:    "500m",
			wantMemory: "1408Mi",
		},
		"cores from sparkConf is used when the typed field is unset": {
			pod: driverPod(sparkcommon.SparkDriverContainerName),
			app: func() *sparkv1beta2.SparkApplication {
				app := scalaApp(sparkv1beta2.DriverSpec{}, sparkv1beta2.ExecutorSpec{})
				app.Spec.SparkConf = map[string]string{sparkcommon.SparkDriverCores: "3"}
				return app
			}(),
			wantCPU:    "3",
			wantMemory: "1408Mi",
		},
		"typed cores takes precedence over cores from sparkConf": {
			pod: driverPod(sparkcommon.SparkDriverContainerName),
			app: func() *sparkv1beta2.SparkApplication {
				app := scalaApp(sparkv1beta2.DriverSpec{
					SparkPodSpec: sparkv1beta2.SparkPodSpec{Cores: new(int32(4))},
				}, sparkv1beta2.ExecutorSpec{})
				app.Spec.SparkConf = map[string]string{sparkcommon.SparkDriverCores: "3"}
				return app
			}(),
			wantCPU:    "4",
			wantMemory: "1408Mi",
		},
		"memory from sparkConf is used when the typed field is unset": {
			pod: executorPod(sparkcommon.Spark3DefaultExecutorContainerName),
			app: func() *sparkv1beta2.SparkApplication {
				app := scalaApp(sparkv1beta2.DriverSpec{}, sparkv1beta2.ExecutorSpec{})
				app.Spec.SparkConf = map[string]string{sparkcommon.SparkExecutorMemory: "8g"}
				return app
			}(),
			wantCPU:    "1",
			wantMemory: "9011Mi", // 8192 + int(8192 * 0.1)
		},
		"typed memory takes precedence over memory from sparkConf": {
			pod: executorPod(sparkcommon.Spark3DefaultExecutorContainerName),
			app: func() *sparkv1beta2.SparkApplication {
				app := scalaApp(sparkv1beta2.DriverSpec{}, sparkv1beta2.ExecutorSpec{
					SparkPodSpec: sparkv1beta2.SparkPodSpec{Memory: new("1g")},
				})
				app.Spec.SparkConf = map[string]string{sparkcommon.SparkExecutorMemory: "8g"}
				return app
			}(),
			wantCPU:    "1",
			wantMemory: "1408Mi",
		},
		"memoryOverhead from sparkConf is used when the typed field is unset": {
			pod: driverPod(sparkcommon.SparkDriverContainerName),
			app: func() *sparkv1beta2.SparkApplication {
				app := scalaApp(sparkv1beta2.DriverSpec{
					SparkPodSpec: sparkv1beta2.SparkPodSpec{Memory: new("4g")},
				}, sparkv1beta2.ExecutorSpec{})
				app.Spec.SparkConf = map[string]string{sparkcommon.SparkDriverMemoryOverhead: "1g"}
				return app
			}(),
			wantCPU:    "1",
			wantMemory: "5Gi",
		},
		"typed memoryOverhead takes precedence over memoryOverhead from sparkConf": {
			pod: driverPod(sparkcommon.SparkDriverContainerName),
			app: func() *sparkv1beta2.SparkApplication {
				app := scalaApp(sparkv1beta2.DriverSpec{
					SparkPodSpec: sparkv1beta2.SparkPodSpec{
						Memory:         new("4g"),
						MemoryOverhead: new("512m"),
					},
				}, sparkv1beta2.ExecutorSpec{})
				app.Spec.SparkConf = map[string]string{sparkcommon.SparkDriverMemoryOverhead: "1g"}
				return app
			}(),
			wantCPU:    "1",
			wantMemory: "4608Mi",
		},
		"explicit memoryOverhead is added to memory": {
			pod: driverPod(sparkcommon.SparkDriverContainerName),
			app: scalaApp(sparkv1beta2.DriverSpec{
				SparkPodSpec: sparkv1beta2.SparkPodSpec{
					Memory:         new("4g"),
					MemoryOverhead: new("1g"),
				},
			}, sparkv1beta2.ExecutorSpec{}),
			wantCPU:    "1",
			wantMemory: "5Gi",
		},
		"memoryOverhead without a unit is in MiB": {
			pod: driverPod(sparkcommon.SparkDriverContainerName),
			app: scalaApp(sparkv1beta2.DriverSpec{
				SparkPodSpec: sparkv1beta2.SparkPodSpec{
					Memory:         new("4g"),
					MemoryOverhead: new("512"),
				},
			}, sparkv1beta2.ExecutorSpec{}),
			wantCPU:    "1",
			wantMemory: "4608Mi",
		},
		"default overhead is 10% of memory when above the minimum": {
			pod: executorPod(sparkcommon.Spark3DefaultExecutorContainerName),
			app: scalaApp(sparkv1beta2.DriverSpec{}, sparkv1beta2.ExecutorSpec{
				SparkPodSpec: sparkv1beta2.SparkPodSpec{Memory: new("8g")},
			}),
			wantCPU:    "1",
			wantMemory: "9011Mi", // 8192 + int(8192 * 0.1)
		},
		"memoryOverheadFactor of 0 still applies the minimum overhead": {
			pod: driverPod(sparkcommon.SparkDriverContainerName),
			app: func() *sparkv1beta2.SparkApplication {
				app := scalaApp(sparkv1beta2.DriverSpec{
					SparkPodSpec: sparkv1beta2.SparkPodSpec{Memory: new("1g")},
				}, sparkv1beta2.ExecutorSpec{})
				app.Spec.MemoryOverheadFactor = new("0")
				return app
			}(),
			wantCPU:    "1",
			wantMemory: "1408Mi",
		},
		"memoryOverheadFactor from spec scales the overhead": {
			pod: driverPod(sparkcommon.SparkDriverContainerName),
			app: func() *sparkv1beta2.SparkApplication {
				app := scalaApp(sparkv1beta2.DriverSpec{
					SparkPodSpec: sparkv1beta2.SparkPodSpec{Memory: new("2g")},
				}, sparkv1beta2.ExecutorSpec{})
				app.Spec.MemoryOverheadFactor = new("0.5")
				return app
			}(),
			wantCPU:    "1",
			wantMemory: "3Gi",
		},
		"memoryOverheadFactor from sparkConf takes precedence over spec": {
			pod: driverPod(sparkcommon.SparkDriverContainerName),
			app: func() *sparkv1beta2.SparkApplication {
				app := scalaApp(sparkv1beta2.DriverSpec{
					SparkPodSpec: sparkv1beta2.SparkPodSpec{Memory: new("2g")},
				}, sparkv1beta2.ExecutorSpec{})
				app.Spec.MemoryOverheadFactor = new("0.5")
				app.Spec.SparkConf = map[string]string{sparkcommon.SparkKubernetesMemoryOverheadFactor: "0.25"}
				return app
			}(),
			wantCPU:    "1",
			wantMemory: "2560Mi",
		},
		"role-specific memoryOverheadFactor takes precedence over the global one": {
			pod: executorPod(sparkcommon.Spark3DefaultExecutorContainerName),
			app: func() *sparkv1beta2.SparkApplication {
				app := scalaApp(sparkv1beta2.DriverSpec{}, sparkv1beta2.ExecutorSpec{
					SparkPodSpec: sparkv1beta2.SparkPodSpec{Memory: new("2g")},
				})
				app.Spec.SparkConf = map[string]string{
					sparkcommon.SparkKubernetesMemoryOverheadFactor: "0.25",
					sparkExecutorMemoryOverheadFactor:               "0.5",
				}
				return app
			}(),
			wantCPU:    "1",
			wantMemory: "3Gi",
		},
		"explicit memoryOverhead takes precedence over the factor": {
			pod: driverPod(sparkcommon.SparkDriverContainerName),
			app: func() *sparkv1beta2.SparkApplication {
				app := scalaApp(sparkv1beta2.DriverSpec{
					SparkPodSpec: sparkv1beta2.SparkPodSpec{
						Memory:         new("2g"),
						MemoryOverhead: new("100m"),
					},
				}, sparkv1beta2.ExecutorSpec{})
				app.Spec.MemoryOverheadFactor = new("0.5")
				return app
			}(),
			wantCPU:    "1",
			wantMemory: "2148Mi",
		},
		"Python applications default to a 40% overhead": {
			pod: driverPod(sparkcommon.SparkDriverContainerName),
			app: &sparkv1beta2.SparkApplication{
				Spec: sparkv1beta2.SparkApplicationSpec{
					Type: sparkv1beta2.SparkApplicationTypePython,
					Driver: sparkv1beta2.DriverSpec{
						SparkPodSpec: sparkv1beta2.SparkPodSpec{Memory: new("2g")},
					},
				},
			},
			wantCPU:    "1",
			wantMemory: "2867Mi", // 2048 + int(2048 * 0.4)
		},
		"R applications default to a 40% overhead for executors too": {
			pod: executorPod(sparkcommon.Spark3DefaultExecutorContainerName),
			app: &sparkv1beta2.SparkApplication{
				Spec: sparkv1beta2.SparkApplicationSpec{
					Type: sparkv1beta2.SparkApplicationTypeR,
					Executor: sparkv1beta2.ExecutorSpec{
						SparkPodSpec: sparkv1beta2.SparkPodSpec{Memory: new("2g")},
					},
				},
			},
			wantCPU:    "1",
			wantMemory: "2867Mi",
		},
		"memoryOverheadFactor from spec overrides the Python default": {
			pod: driverPod(sparkcommon.SparkDriverContainerName),
			app: &sparkv1beta2.SparkApplication{
				Spec: sparkv1beta2.SparkApplicationSpec{
					Type:                 sparkv1beta2.SparkApplicationTypePython,
					MemoryOverheadFactor: new("0.1"),
					Driver: sparkv1beta2.DriverSpec{
						SparkPodSpec: sparkv1beta2.SparkPodSpec{Memory: new("2g")},
					},
				},
			},
			wantCPU:    "1",
			wantMemory: "2432Mi", // 2048 + max(204, 384)
		},
		"pyspark memory is added to Python executors": {
			pod: executorPod(sparkcommon.Spark3DefaultExecutorContainerName),
			app: &sparkv1beta2.SparkApplication{
				Spec: sparkv1beta2.SparkApplicationSpec{
					Type:      sparkv1beta2.SparkApplicationTypePython,
					SparkConf: map[string]string{sparkExecutorPysparkMemory: "512m"},
					Executor: sparkv1beta2.ExecutorSpec{
						SparkPodSpec: sparkv1beta2.SparkPodSpec{Memory: new("2g")},
					},
				},
			},
			wantCPU:    "1",
			wantMemory: "3379Mi", // 2048 + 819 + 512
		},
		"pyspark memory is ignored for JVM applications": {
			pod: executorPod(sparkcommon.Spark3DefaultExecutorContainerName),
			app: func() *sparkv1beta2.SparkApplication {
				app := scalaApp(sparkv1beta2.DriverSpec{}, sparkv1beta2.ExecutorSpec{
					SparkPodSpec: sparkv1beta2.SparkPodSpec{Memory: new("2g")},
				})
				app.Spec.SparkConf = map[string]string{sparkExecutorPysparkMemory: "512m"}
				return app
			}(),
			wantCPU:    "1",
			wantMemory: "2432Mi",
		},
		"pyspark memory is ignored for the driver": {
			pod: driverPod(sparkcommon.SparkDriverContainerName),
			app: &sparkv1beta2.SparkApplication{
				Spec: sparkv1beta2.SparkApplicationSpec{
					Type:      sparkv1beta2.SparkApplicationTypePython,
					SparkConf: map[string]string{sparkExecutorPysparkMemory: "512m"},
					Driver: sparkv1beta2.DriverSpec{
						SparkPodSpec: sparkv1beta2.SparkPodSpec{Memory: new("2g")},
					},
				},
			},
			wantCPU:    "1",
			wantMemory: "2867Mi",
		},
		"off-heap memory is added to executors when enabled": {
			pod: executorPod(sparkcommon.Spark3DefaultExecutorContainerName),
			app: func() *sparkv1beta2.SparkApplication {
				app := scalaApp(sparkv1beta2.DriverSpec{}, sparkv1beta2.ExecutorSpec{
					SparkPodSpec: sparkv1beta2.SparkPodSpec{Memory: new("2g")},
				})
				app.Spec.SparkConf = map[string]string{
					sparkMemoryOffHeapEnabled: "true",
					sparkMemoryOffHeapSize:    "1g",
				}
				return app
			}(),
			wantCPU:    "1",
			wantMemory: "3456Mi", // 2048 + 384 + 1024
		},
		"off-heap size without a unit is in bytes": {
			pod: executorPod(sparkcommon.Spark3DefaultExecutorContainerName),
			app: func() *sparkv1beta2.SparkApplication {
				app := scalaApp(sparkv1beta2.DriverSpec{}, sparkv1beta2.ExecutorSpec{
					SparkPodSpec: sparkv1beta2.SparkPodSpec{Memory: new("2g")},
				})
				app.Spec.SparkConf = map[string]string{
					sparkMemoryOffHeapEnabled: "true",
					sparkMemoryOffHeapSize:    "1073741824",
				}
				return app
			}(),
			wantCPU:    "1",
			wantMemory: "3456Mi",
		},
		"off-heap memory is ignored when disabled": {
			pod: executorPod(sparkcommon.Spark3DefaultExecutorContainerName),
			app: func() *sparkv1beta2.SparkApplication {
				app := scalaApp(sparkv1beta2.DriverSpec{}, sparkv1beta2.ExecutorSpec{
					SparkPodSpec: sparkv1beta2.SparkPodSpec{Memory: new("2g")},
				})
				app.Spec.SparkConf = map[string]string{sparkMemoryOffHeapSize: "1g"}
				return app
			}(),
			wantCPU:    "1",
			wantMemory: "2432Mi",
		},
		"invalid memory string": {
			pod: driverPod(sparkcommon.SparkDriverContainerName),
			app: scalaApp(sparkv1beta2.DriverSpec{
				SparkPodSpec: sparkv1beta2.SparkPodSpec{Memory: new("512Mi")},
			}, sparkv1beta2.ExecutorSpec{}),
			wantErr: true,
		},
		"invalid memoryOverheadFactor": {
			pod: driverPod(sparkcommon.SparkDriverContainerName),
			app: func() *sparkv1beta2.SparkApplication {
				app := scalaApp(sparkv1beta2.DriverSpec{}, sparkv1beta2.ExecutorSpec{})
				app.Spec.MemoryOverheadFactor = new("lots")
				return app
			}(),
			wantErr: true,
		},
		"invalid cores from sparkConf": {
			pod: driverPod(sparkcommon.SparkDriverContainerName),
			app: func() *sparkv1beta2.SparkApplication {
				app := scalaApp(sparkv1beta2.DriverSpec{}, sparkv1beta2.ExecutorSpec{})
				app.Spec.SparkConf = map[string]string{sparkcommon.SparkDriverCores: "four"}
				return app
			}(),
			wantErr: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			err := addCPURequests(tc.pod, tc.app)
			if err == nil {
				err = addMemoryRequests(tc.pod, tc.app)
			}
			if (err != nil) != tc.wantErr {
				t.Fatalf("unexpected error: %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			res := tc.pod.Spec.Containers[0].Resources
			if got := res.Requests[corev1.ResourceCPU]; got.Cmp(resource.MustParse(tc.wantCPU)) != 0 {
				t.Errorf("unexpected CPU request: got %s, want %s", got.String(), tc.wantCPU)
			}
			wantMemory := resource.MustParse(tc.wantMemory)
			if got := res.Requests[corev1.ResourceMemory]; got.Cmp(wantMemory) != 0 {
				t.Errorf("unexpected memory request: got %s, want %s", got.String(), tc.wantMemory)
			}
			if got := res.Limits[corev1.ResourceMemory]; got.Cmp(wantMemory) != 0 {
				t.Errorf("unexpected memory limit: got %s, want %s", got.String(), tc.wantMemory)
			}
		})
	}
}

func TestParseSparkMemoryBytes(t *testing.T) {
	tests := map[string]struct {
		input       string
		defaultUnit int64
		want        int64
		wantErr     bool
	}{
		"bare number uses the default unit": {input: "512", defaultUnit: 1 << 20, want: 512 << 20},
		"m":                                 {input: "512m", defaultUnit: 1, want: 512 << 20},
		"mb":                                {input: "512mb", defaultUnit: 1, want: 512 << 20},
		"g":                                 {input: "2g", defaultUnit: 1, want: 2 << 30},
		"uppercase":                         {input: "2G", defaultUnit: 1, want: 2 << 30},
		"k":                                 {input: "1k", defaultUnit: 1, want: 1 << 10},
		"t":                                 {input: "1t", defaultUnit: 1, want: 1 << 40},
		"b":                                 {input: "100b", defaultUnit: 1 << 20, want: 100},
		"kubernetes style is rejected":      {input: "512Mi", defaultUnit: 1, wantErr: true},
		"fraction is rejected":              {input: "1.5g", defaultUnit: 1, wantErr: true},
		"empty is rejected":                 {input: "", defaultUnit: 1, wantErr: true},
		"unknown unit is rejected":          {input: "1x", defaultUnit: 1, wantErr: true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := parseSparkMemoryBytes(tc.input, tc.defaultUnit)
			if (err != nil) != tc.wantErr {
				t.Fatalf("unexpected error: %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("parseSparkMemoryBytes(%q) = %d, want %d", tc.input, got, tc.want)
			}
		})
	}
}
