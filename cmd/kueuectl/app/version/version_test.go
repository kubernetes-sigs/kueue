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

package version

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	cmdtesting "sigs.k8s.io/kueue/cmd/kueuectl/app/testing"
)

func TestVersionCmd(t *testing.T) {
	testCases := map[string]struct {
		deployment *appsv1.Deployment
		args       []string
		wantOut    string
		wantOutErr string
		wantErr    error
	}{
		"should print client version": {
			args:    []string{},
			wantOut: "Client Version: v0.0.0-main\n",
		},
		"should print client and server versions": {
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      kueueControllerManagerName,
					Namespace: kueueNamespace,
				},
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "manager",
									Image: "registry.k8s.io/kueue/kueue:v0.0.0",
								},
							},
						},
					},
				},
			},
			args: []string{},
			wantOut: `Client Version: v0.0.0-main
Kueue Controller Manager Image: registry.k8s.io/kueue/kueue:v0.0.0
`,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			streams, _, out, outErr := genericiooptions.NewTestIOStreams()

			tcg := cmdtesting.NewTestClientGetter()
			if tc.deployment != nil {
				tcg.WithK8sClientset(k8sfake.NewSimpleClientset(tc.deployment))
			}

			cmd := NewVersionCmd(tcg, streams)
			cmd.SetArgs(tc.args)

			gotErr := cmd.Execute()
			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Unexpected error (-want/+got)\n%s", diff)
			}

			gotOut := out.String()
			if diff := cmp.Diff(tc.wantOut, gotOut); diff != "" {
				t.Errorf("Unexpected output (-want/+got)\n%s", diff)
			}

			gotOutErr := outErr.String()
			if diff := cmp.Diff(tc.wantOutErr, gotOutErr); diff != "" {
				t.Errorf("Unexpected output (-want/+got)\n%s", diff)
			}
		})
	}
}
