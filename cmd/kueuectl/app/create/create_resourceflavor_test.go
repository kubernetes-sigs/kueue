/*
Copyright 2024 The Kubernetes Authors.

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

package create

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/client-go/clientset/versioned/fake"
	cmdtesting "sigs.k8s.io/kueue/cmd/kueuectl/app/testing"
)

func TestResourceFlavorOptions_parseTaints(t *testing.T) {
	testCases := map[string]struct {
		spec       []string
		wantTaints []corev1.Taint
		wantErr    string
	}{
		"empty spec": {},
		"empty spec item": {
			spec:    []string{""},
			wantErr: "invalid taint spec",
		},
		"empty key and value": {
			spec:    []string{"NoSchedule"},
			wantErr: "invalid taint spec: NoSchedule",
		},
		"no effect": {
			spec:    []string{"key=value"},
			wantErr: "invalid taint spec: key=value",
		},
		"duplicate effect": {
			spec:    []string{"key=value:NoSchedule:NoSchedule"},
			wantErr: "invalid taint spec: key=value:NoSchedule:NoSchedule",
		},
		"duplicate values": {
			spec:    []string{"key=value=value:NoSchedule"},
			wantErr: "invalid taint spec: key=value=value:NoSchedule",
		},
		"duplicate key-effect pair": {
			spec:    []string{"key=value:NoSchedule", "key=value:NoSchedule"},
			wantErr: "duplicated taints with the same key and effect: {key value NoSchedule <nil>}",
		},
		"empty key": {
			spec:    []string{"=value:NoSchedule"},
			wantErr: "invalid taint spec: =value:NoSchedule, name part must be non-empty; name part must consist of alphanumeric characters, '-', '_' or '.', and must start and end with an alphanumeric character (e.g. 'MyName',  or 'my.name',  or '123-abc', regex used for validation is '([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9]')",
		},
		"invalid key": {
			spec:    []string{"@=value:NoSchedule"},
			wantErr: "invalid taint spec: @=value:NoSchedule, name part must consist of alphanumeric characters, '-', '_' or '.', and must start and end with an alphanumeric character (e.g. 'MyName',  or 'my.name',  or '123-abc', regex used for validation is '([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9]')",
		},
		"invalid value": {
			spec:    []string{"key=@:NoSchedule"},
			wantErr: "invalid taint spec: key=@:NoSchedule, a valid label must be an empty string or consist of alphanumeric characters, '-', '_' or '.', and must start and end with an alphanumeric character (e.g. 'MyValue',  or 'my_value',  or '12345', regex used for validation is '(([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9])?')",
		},
		"invalid effect": {
			spec:    []string{"key=value:Invalid"},
			wantErr: "invalid taint effect: Invalid, unsupported taint effect",
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			gotTaints, gotErr := parseTaints(tc.spec)

			var gotErrStr string
			if gotErr != nil {
				gotErrStr = gotErr.Error()
			}

			if diff := cmp.Diff(tc.wantErr, gotErrStr); diff != "" {
				t.Errorf("Unexpected error (-want/+got)\n%s", diff)
			}

			if gotErr != nil {
				return
			}

			if diff := cmp.Diff(tc.wantTaints, gotTaints); diff != "" {
				t.Errorf("Unexpected result (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestResourceFlavorOptions_parseTolerations(t *testing.T) {
	testCases := map[string]struct {
		spec            []string
		wantTolerations []corev1.Toleration
		wantErr         string
	}{
		"empty spec": {},
		"empty spec item": {
			spec:    []string{""},
			wantErr: "invalid toleration spec",
		},
		"duplicate effect": {
			spec:    []string{"key=value:NoSchedule:NoSchedule"},
			wantErr: "invalid toleration spec: key=value:NoSchedule:NoSchedule",
		},
		"duplicate values": {
			spec:    []string{"key=value=value:NoSchedule"},
			wantErr: "invalid toleration spec: key=value=value:NoSchedule",
		},
		"empty key": {
			spec:    []string{"=value:NoSchedule"},
			wantErr: "invalid toleration spec: =value:NoSchedule",
		},
		"invalid key": {
			spec:    []string{"@=value:NoSchedule"},
			wantErr: "invalid toleration spec: @=value:NoSchedule, name part must consist of alphanumeric characters, '-', '_' or '.', and must start and end with an alphanumeric character (e.g. 'MyName',  or 'my.name',  or '123-abc', regex used for validation is '([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9]')",
		},
		"invalid value": {
			spec:    []string{"key=@:NoSchedule"},
			wantErr: "invalid toleration spec: key=@:NoSchedule, a valid label must be an empty string or consist of alphanumeric characters, '-', '_' or '.', and must start and end with an alphanumeric character (e.g. 'MyValue',  or 'my_value',  or '12345', regex used for validation is '(([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9])?')",
		},
		"invalid effect": {
			spec:    []string{":Invalid"},
			wantErr: "invalid taint effect: Invalid, unsupported taint effect",
		},
		"invalid effect with key": {
			spec:    []string{"key:Invalid"},
			wantErr: "invalid taint effect: Invalid, unsupported taint effect",
		},
		"invalid effect with key=value": {
			spec:    []string{"key=value:Invalid"},
			wantErr: "invalid taint effect: Invalid, unsupported taint effect",
		},
		"key": {
			spec: []string{"key"},
			wantTolerations: []corev1.Toleration{
				{Key: "key", Operator: corev1.TolerationOpExists},
			},
		},
		"effect": {
			spec: []string{":NoSchedule"},
			wantTolerations: []corev1.Toleration{
				{Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule},
			},
		},
		"key and value": {
			spec: []string{"key=value"},
			wantTolerations: []corev1.Toleration{
				{Key: "key", Value: "value", Operator: corev1.TolerationOpEqual},
			},
		},
		"key and effect": {
			spec: []string{fmt.Sprintf("key:%s", corev1.TaintEffectNoSchedule)},
			wantTolerations: []corev1.Toleration{
				{Key: "key", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule},
			},
		},
		"key, value and effect": {
			spec: []string{fmt.Sprintf("key=value:%s", corev1.TaintEffectNoSchedule)},
			wantTolerations: []corev1.Toleration{
				{Key: "key", Value: "value", Effect: corev1.TaintEffectNoSchedule, Operator: corev1.TolerationOpEqual},
			},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			gotTolerations, gotErr := parseTolerations(tc.spec)

			var gotErrStr string
			if gotErr != nil {
				gotErrStr = gotErr.Error()
			}

			if diff := cmp.Diff(tc.wantErr, gotErrStr); diff != "" {
				t.Errorf("Unexpected error (-want/+got)\n%s", diff)
			}

			if gotErr != nil {
				return
			}

			if diff := cmp.Diff(tc.wantTolerations, gotTolerations); diff != "" {
				t.Errorf("Unexpected result (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestResourceFlavorOptions_createResourceFlavor(t *testing.T) {
	testCases := map[string]struct {
		options  *ResourceFlavorOptions
		expected *v1beta1.ResourceFlavor
	}{
		"success create": {
			options: &ResourceFlavorOptions{
				Name:       "rf",
				NodeLabels: map[string]string{"key1": "value"},
				NodeTaints: []corev1.Taint{{Key: "key1", Value: "value", Effect: corev1.TaintEffectNoSchedule}},
			},
			expected: &v1beta1.ResourceFlavor{
				TypeMeta:   metav1.TypeMeta{APIVersion: "kueue.x-k8s.io/v1beta1", Kind: "ResourceFlavor"},
				ObjectMeta: metav1.ObjectMeta{Name: "rf"},
				Spec: v1beta1.ResourceFlavorSpec{
					NodeLabels: map[string]string{"key1": "value"},
					NodeTaints: []corev1.Taint{{Key: "key1", Value: "value", Effect: corev1.TaintEffectNoSchedule}},
				},
			},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			rf := tc.options.createResourceFlavor()
			if diff := cmp.Diff(tc.expected, rf); diff != "" {
				t.Errorf("Unexpected result (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestResourceFlavorCmd(t *testing.T) {
	testCases := map[string]struct {
		rfName     string
		args       []string
		wantRf     *v1beta1.ResourceFlavor
		wantOut    string
		wantOutErr string
		wantErr    string
	}{
		"shouldn't create resource flavor with invalid dry-run client": {
			rfName:  "rf",
			args:    []string{"--dry-run", "invalid"},
			wantErr: "Invalid dry-run value (invalid). Must be \"none\", \"server\", or \"client\".",
		},
		"shouldn't create resource flavor with dry-run client": {
			rfName:  "rf",
			args:    []string{"--dry-run", "client"},
			wantRf:  &v1beta1.ResourceFlavor{},
			wantOut: "resourceflavor.kueue.x-k8s.io/rf created (client dry run)\n",
		},
		"should create resource flavor": {
			rfName: "rf",
			wantRf: &v1beta1.ResourceFlavor{
				TypeMeta:   metav1.TypeMeta{APIVersion: v1beta1.SchemeGroupVersion.String(), Kind: "ResourceFlavor"},
				ObjectMeta: metav1.ObjectMeta{Name: "rf"},
				Spec:       v1beta1.ResourceFlavorSpec{},
			},
			wantOut: "resourceflavor.kueue.x-k8s.io/rf created\n",
		},
		"should create resource flavor with node labels": {
			rfName: "rf",
			args:   []string{"--node-labels", "beta.kubernetes.io/arch=arm64,beta.kubernetes.io/os=linux"},
			wantRf: &v1beta1.ResourceFlavor{
				TypeMeta:   metav1.TypeMeta{APIVersion: v1beta1.SchemeGroupVersion.String(), Kind: "ResourceFlavor"},
				ObjectMeta: metav1.ObjectMeta{Name: "rf"},
				Spec: v1beta1.ResourceFlavorSpec{
					NodeLabels: map[string]string{
						"beta.kubernetes.io/arch": "arm64",
						"beta.kubernetes.io/os":   "linux",
					},
				},
			},
			wantOut: "resourceflavor.kueue.x-k8s.io/rf created\n",
		},
		"should create resource flavor with node taints": {
			rfName: "rf",
			args:   []string{"--node-taints", "key1=value:NoSchedule,key1=value:PreferNoSchedule,key2=value:NoSchedule,key3=value:NoSchedule"},
			wantRf: &v1beta1.ResourceFlavor{
				TypeMeta:   metav1.TypeMeta{APIVersion: v1beta1.SchemeGroupVersion.String(), Kind: "ResourceFlavor"},
				ObjectMeta: metav1.ObjectMeta{Name: "rf"},
				Spec: v1beta1.ResourceFlavorSpec{
					NodeTaints: []corev1.Taint{
						{Key: "key1", Value: "value", Effect: corev1.TaintEffectNoSchedule},
						{Key: "key1", Value: "value", Effect: corev1.TaintEffectPreferNoSchedule},
						{Key: "key2", Value: "value", Effect: corev1.TaintEffectNoSchedule},
						{Key: "key3", Value: "value", Effect: corev1.TaintEffectNoSchedule},
					},
				},
			},
			wantOut: "resourceflavor.kueue.x-k8s.io/rf created\n",
		},
		"shouldn't create resource flavor with invalid taint": {
			rfName:  "rf",
			args:    []string{"--node-taints", "key1=value:Invalid"},
			wantErr: "invalid taint effect: Invalid, unsupported taint effect",
		},
		"should create resource flavor with toleration": {
			rfName: "rf",
			args:   []string{"--tolerations", "key1=value:NoSchedule,key2:NoSchedule"},
			wantRf: &v1beta1.ResourceFlavor{
				TypeMeta:   metav1.TypeMeta{APIVersion: v1beta1.SchemeGroupVersion.String(), Kind: "ResourceFlavor"},
				ObjectMeta: metav1.ObjectMeta{Name: "rf"},
				Spec: v1beta1.ResourceFlavorSpec{
					Tolerations: []corev1.Toleration{
						{Key: "key1", Value: "value", Effect: corev1.TaintEffectNoSchedule, Operator: corev1.TolerationOpEqual},
						{Key: "key2", Effect: corev1.TaintEffectNoSchedule, Operator: corev1.TolerationOpExists},
					},
				},
			},
			wantOut: "resourceflavor.kueue.x-k8s.io/rf created\n",
		},
		"shouldn't create resource flavor with invalid toleration": {
			rfName:  "rf",
			args:    []string{"--tolerations", "key1=value:Invalid"},
			wantErr: "invalid taint effect: Invalid, unsupported taint effect",
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			streams, _, out, outErr := genericiooptions.NewTestIOStreams()

			clientset := fake.NewSimpleClientset()
			tcg := cmdtesting.NewTestClientGetter().WithKueueClientset(clientset)

			cmd := NewResourceFlavorCmd(tcg, streams)
			cmd.SetOut(out)
			cmd.SetErr(outErr)
			cmd.SetArgs(append([]string{tc.rfName}, tc.args...))
			cmd.Flags().String("dry-run", "none", "")

			gotErr := cmd.Execute()

			var gotErrStr string
			if gotErr != nil {
				gotErrStr = gotErr.Error()
			}

			if diff := cmp.Diff(tc.wantErr, gotErrStr); diff != "" {
				t.Errorf("Unexpected error (-want/+got)\n%s", diff)
			}

			if gotErr != nil {
				return
			}

			gotOut := out.String()
			if diff := cmp.Diff(tc.wantOut, gotOut); diff != "" {
				t.Errorf("Unexpected output (-want/+got)\n%s", diff)
			}

			gotOutErr := outErr.String()
			if diff := cmp.Diff(tc.wantOutErr, gotOutErr); diff != "" {
				t.Errorf("Unexpected output (-want/+got)\n%s", diff)
			}

			gotRf, err := clientset.KueueV1beta1().ResourceFlavors().Get(context.Background(), tc.rfName, metav1.GetOptions{})
			if client.IgnoreNotFound(err) != nil {
				t.Error(err)
				return
			}

			if diff := cmp.Diff(tc.wantRf, gotRf); diff != "" {
				t.Errorf("Unexpected resource flavor (-want/+got)\n%s", diff)
			}
		})
	}
}
