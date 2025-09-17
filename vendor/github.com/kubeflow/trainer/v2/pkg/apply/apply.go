/*
Copyright 2025 The Kubeflow Authors.

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

package apply

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	errorRequestedFieldPathNotFound = errors.New("requested field path not found")
)

func UpsertEnvVar(envVars *[]corev1ac.EnvVarApplyConfiguration, envVar ...corev1ac.EnvVarApplyConfiguration) {
	for _, e := range envVar {
		upsert(envVars, e, byEnvVarName)
	}
}

func UpsertEnvVars(envVars *[]corev1ac.EnvVarApplyConfiguration, upEnvVars ...corev1ac.EnvVarApplyConfiguration) {
	for _, e := range upEnvVars {
		upsert(envVars, e, byEnvVarName)
	}
}

func UpsertPort(ports *[]corev1ac.ContainerPortApplyConfiguration, port ...corev1ac.ContainerPortApplyConfiguration) {
	for _, p := range port {
		upsert(ports, p, byContainerPortOrName)
	}
}

func UpsertVolumes(volumes *[]corev1ac.VolumeApplyConfiguration, upVolumes ...corev1ac.VolumeApplyConfiguration) {
	for _, v := range upVolumes {
		upsert(volumes, v, byVolumeName)
	}
}

func UpsertVolumeMounts(mounts *[]corev1ac.VolumeMountApplyConfiguration, upMounts ...corev1ac.VolumeMountApplyConfiguration) {
	for _, m := range upMounts {
		upsert(mounts, m, byVolumeMountPath)
	}
}

func byEnvVarName(a, b corev1ac.EnvVarApplyConfiguration) bool {
	return ptr.Equal(a.Name, b.Name)
}

func byContainerPortOrName(a, b corev1ac.ContainerPortApplyConfiguration) bool {
	return ptr.Equal(a.ContainerPort, b.ContainerPort) || ptr.Equal(a.Name, b.Name)
}

func byVolumeName(a, b corev1ac.VolumeApplyConfiguration) bool {
	return ptr.Equal(a.Name, b.Name)
}

func byVolumeMountPath(a, b corev1ac.VolumeMountApplyConfiguration) bool {
	return ptr.Equal(a.MountPath, b.MountPath)
}

type compare[T any] func(T, T) bool

func upsert[T any](items *[]T, item T, predicate compare[T]) {
	for i, t := range *items {
		if predicate(t, item) {
			(*items)[i] = item
			return
		}
	}
	*items = append(*items, item)
}

func EnvVar(e corev1.EnvVar) *corev1ac.EnvVarApplyConfiguration {
	envVar := corev1ac.EnvVar().WithName(e.Name)
	if from := e.ValueFrom; from != nil {
		source := corev1ac.EnvVarSource()
		if ref := from.FieldRef; ref != nil {
			source.WithFieldRef(corev1ac.ObjectFieldSelector().WithFieldPath(ref.FieldPath))
		}
		if ref := from.ResourceFieldRef; ref != nil {
			source.WithResourceFieldRef(corev1ac.ResourceFieldSelector().
				WithContainerName(ref.ContainerName).
				WithResource(ref.Resource).
				WithDivisor(ref.Divisor))
		}
		if ref := from.ConfigMapKeyRef; ref != nil {
			key := corev1ac.ConfigMapKeySelector().WithKey(ref.Key).WithName(ref.Name)
			if optional := ref.Optional; optional != nil {
				key.WithOptional(*optional)
			}
			source.WithConfigMapKeyRef(key)
		}
		if ref := from.SecretKeyRef; ref != nil {
			key := corev1ac.SecretKeySelector().WithKey(ref.Key).WithName(ref.Name)
			if optional := ref.Optional; optional != nil {
				key.WithOptional(*optional)
			}
			source.WithSecretKeyRef(key)
		}
		envVar.WithValueFrom(source)
	} else {
		envVar.WithValue(e.Value)
	}
	return envVar
}

func EnvVars(e ...corev1.EnvVar) []corev1ac.EnvVarApplyConfiguration {
	var envs []corev1ac.EnvVarApplyConfiguration
	for _, env := range e {
		envs = append(envs, *EnvVar(env))
	}
	return envs
}

func FromTypedObjWithFields[A any](typed client.Object, fields ...string) (*A, error) {
	u, err := runtime.DefaultUnstructuredConverter.ToUnstructured(typed)
	if err != nil {
		return nil, err
	}
	uObj, ok, err := unstructured.NestedFieldCopy(u, fields...)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("%w: '.%s'", errorRequestedFieldPathNotFound, strings.Join(fields, "."))
	}
	raw, err := json.Marshal(uObj)
	if err != nil {
		return nil, err
	}
	var objApply *A
	if err = json.Unmarshal(raw, &objApply); err != nil {
		return nil, err
	}
	return objApply, nil
}
