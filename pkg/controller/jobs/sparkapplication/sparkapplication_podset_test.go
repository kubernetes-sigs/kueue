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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func driverPod(containerName string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				sparkcommon.LabelSparkRole: sparkcommon.SparkRoleDriver,
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: containerName},
			},
		},
	}
}

func TestAddVolumeMount(t *testing.T) {
	tests := map[string]struct {
		pod     *corev1.Pod
		wantErr bool
	}{
		"driver pod with matching container": {
			pod:     driverPod(sparkcommon.SparkDriverContainerName),
			wantErr: false,
		},
		"pod that is neither driver nor executor": {
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "not-spark"}},
				},
			},
			wantErr: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			err := addVolumeMount(tc.pod, corev1.VolumeMount{Name: "data", MountPath: "/data"})
			if (err != nil) != tc.wantErr {
				t.Fatalf("addVolumeMount() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestAddVolumes(t *testing.T) {
	tests := map[string]struct {
		pod             *corev1.Pod
		app             *sparkv1beta2.SparkApplication
		wantErr         bool
		wantVolumes     []string
		wantVolumeMount []string
	}{
		"adds a volume and mount that match by name": {
			pod: driverPod(sparkcommon.SparkDriverContainerName),
			app: &sparkv1beta2.SparkApplication{
				Spec: sparkv1beta2.SparkApplicationSpec{
					Volumes: []corev1.Volume{{Name: "data"}},
					Driver: sparkv1beta2.DriverSpec{
						SparkPodSpec: sparkv1beta2.SparkPodSpec{
							VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: "/data"}},
						},
					},
				},
			},
			wantVolumes:     []string{"data"},
			wantVolumeMount: []string{"data"},
		},
		"skips a mount with no matching volume declared": {
			pod: driverPod(sparkcommon.SparkDriverContainerName),
			app: &sparkv1beta2.SparkApplication{
				Spec: sparkv1beta2.SparkApplicationSpec{
					Driver: sparkv1beta2.DriverSpec{
						SparkPodSpec: sparkv1beta2.SparkPodSpec{
							VolumeMounts: []corev1.VolumeMount{{Name: "unknown", MountPath: "/data"}},
						},
					},
				},
			},
			wantVolumes:     nil,
			wantVolumeMount: nil,
		},
		"skips localDir volume mounts": {
			pod: driverPod(sparkcommon.SparkDriverContainerName),
			app: &sparkv1beta2.SparkApplication{
				Spec: sparkv1beta2.SparkApplicationSpec{
					Volumes: []corev1.Volume{{Name: sparkcommon.SparkLocalDirVolumePrefix + "0"}},
					Driver: sparkv1beta2.DriverSpec{
						SparkPodSpec: sparkv1beta2.SparkPodSpec{
							VolumeMounts: []corev1.VolumeMount{{Name: sparkcommon.SparkLocalDirVolumePrefix + "0", MountPath: "/tmp"}},
						},
					},
				},
			},
			wantVolumes:     nil,
			wantVolumeMount: nil,
		},
		"adds the volume once for two mounts referencing the same volume": {
			pod: driverPod(sparkcommon.SparkDriverContainerName),
			app: &sparkv1beta2.SparkApplication{
				Spec: sparkv1beta2.SparkApplicationSpec{
					Volumes: []corev1.Volume{{Name: "data"}},
					Driver: sparkv1beta2.DriverSpec{
						SparkPodSpec: sparkv1beta2.SparkPodSpec{
							VolumeMounts: []corev1.VolumeMount{
								{Name: "data", MountPath: "/data-a"},
								{Name: "data", MountPath: "/data-b"},
							},
						},
					},
				},
			},
			wantVolumes:     []string{"data"},
			wantVolumeMount: []string{"data", "data"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			err := addVolumes(tc.pod, tc.app)
			if (err != nil) != tc.wantErr {
				t.Fatalf("addVolumes() error = %v, wantErr %v", err, tc.wantErr)
			}

			var gotVolumes []string
			for _, v := range tc.pod.Spec.Volumes {
				gotVolumes = append(gotVolumes, v.Name)
			}
			if len(gotVolumes) != len(tc.wantVolumes) {
				t.Fatalf("pod.Spec.Volumes = %v, want %v", gotVolumes, tc.wantVolumes)
			}
			for i, name := range tc.wantVolumes {
				if gotVolumes[i] != name {
					t.Errorf("pod.Spec.Volumes[%d] = %v, want %v", i, gotVolumes[i], name)
				}
			}

			var gotMounts []string
			for _, m := range tc.pod.Spec.Containers[0].VolumeMounts {
				gotMounts = append(gotMounts, m.Name)
			}
			if len(gotMounts) != len(tc.wantVolumeMount) {
				t.Fatalf("container.VolumeMounts = %v, want %v", gotMounts, tc.wantVolumeMount)
			}
			for i, name := range tc.wantVolumeMount {
				if gotMounts[i] != name {
					t.Errorf("container.VolumeMounts[%d] = %v, want %v", i, gotMounts[i], name)
				}
			}
		})
	}
}
