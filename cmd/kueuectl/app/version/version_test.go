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

package version

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/spf13/cobra"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	kubetesting "k8s.io/client-go/testing"

	cmdtesting "sigs.k8s.io/kueue/cmd/kueuectl/app/testing"
)

func TestVersionCmd(t *testing.T) {
	controllerManagerLabels := map[string]string{
		"app.kubernetes.io/name": "kueue",
		"control-plane":          "controller-manager",
	}
	errForbidden := apierrors.NewForbidden(appsv1.Resource("deployments"), "", errors.New("access denied"))

	testCases := map[string]struct {
		deployments []*appsv1.Deployment
		listErr     error
		args        []string
		wantOut     string
		wantOutErr  string
		wantErr     error
	}{
		"should print client version": {
			args:    []string{},
			wantOut: "Client Version: v0.0.0-main\n",
		},
		"should print client and server versions": {
			deployments: []*appsv1.Deployment{{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "kueue-controller-manager",
					Namespace: kueueNamespace,
					Labels:    controllerManagerLabels,
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
			}},
			args: []string{},
			wantOut: `Client Version: v0.0.0-main
Kueue Controller Manager Image: registry.k8s.io/kueue/kueue:v0.0.0
`,
		},
		"should look up the controller manager in --namespace": {
			deployments: []*appsv1.Deployment{{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "kueue-controller-manager",
					Namespace: "custom-kueue",
					Labels:    controllerManagerLabels,
				},
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "manager",
									Image: "registry.k8s.io/kueue/kueue:v0.0.0-custom",
								},
							},
						},
					},
				},
			}},
			args: []string{"--namespace", "custom-kueue"},
			wantOut: `Client Version: v0.0.0-main
Kueue Controller Manager Image: registry.k8s.io/kueue/kueue:v0.0.0-custom
`,
		},
		"should ignore a controller manager outside --namespace": {
			deployments: []*appsv1.Deployment{{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "kueue-controller-manager",
					Namespace: kueueNamespace,
					Labels:    controllerManagerLabels,
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
			}},
			args:    []string{"--namespace", "custom-kueue"},
			wantOut: "Client Version: v0.0.0-main\n",
		},
		"should find the controller manager of a Helm release with a custom name": {
			deployments: []*appsv1.Deployment{{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "foo-kueue-controller-manager",
					Namespace: kueueNamespace,
					Labels:    controllerManagerLabels,
				},
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "manager",
									Image: "registry.k8s.io/kueue/kueue:v0.0.0-helm",
								},
							},
						},
					},
				},
			}},
			args: []string{},
			wantOut: `Client Version: v0.0.0-main
Kueue Controller Manager Image: registry.k8s.io/kueue/kueue:v0.0.0-helm
`,
		},
		"should ignore a Deployment without the controller manager labels": {
			deployments: []*appsv1.Deployment{{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "foo-kueue-kueueviz-backend",
					Namespace: kueueNamespace,
					Labels: map[string]string{
						"app.kubernetes.io/name":      "kueue",
						"app.kubernetes.io/component": "dashboard",
					},
				},
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "manager",
									Image: "registry.k8s.io/kueue/kueueviz-backend:v0.0.0",
								},
							},
						},
					},
				},
			}},
			args:    []string{},
			wantOut: "Client Version: v0.0.0-main\n",
		},
		"should fail when multiple Deployments have the controller manager labels": {
			deployments: []*appsv1.Deployment{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "foo-kueue-controller-manager",
						Namespace: kueueNamespace,
						Labels:    controllerManagerLabels,
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "bar-kueue-controller-manager",
						Namespace: kueueNamespace,
						Labels:    controllerManagerLabels,
					},
				},
			},
			args:    []string{},
			wantOut: "Client Version: v0.0.0-main\n",
			wantErr: errMultipleControllerManagers,
		},
		"should return a List error": {
			listErr: errForbidden,
			args:    []string{},
			wantOut: "Client Version: v0.0.0-main\n",
			wantErr: errForbidden,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			streams, _, out, outErr := genericiooptions.NewTestIOStreams()

			objs := make([]runtime.Object, 0, len(tc.deployments))
			for _, d := range tc.deployments {
				objs = append(objs, d)
			}
			clientset := k8sfake.NewClientset(objs...)
			if tc.listErr != nil {
				clientset.PrependReactor("list", "deployments", func(kubetesting.Action) (bool, runtime.Object, error) {
					return true, nil, tc.listErr
				})
			}
			tcg := cmdtesting.NewTestClientGetter().WithK8sClientset(clientset)

			cmd := NewVersionCmd(tcg, streams)
			// Simulate the inherited persistent --namespace flag from the root command.
			cmd.Flags().StringP("namespace", "n", "", "If present, the namespace scope for this CLI request")
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

func TestExplicitNamespace(t *testing.T) {
	cmd := &cobra.Command{Use: "version"}
	cmd.Flags().StringP("namespace", "n", "", "")

	if got := explicitNamespace(cmd); got != "" {
		t.Fatalf("explicitNamespace() = %q, want empty when flag is unset", got)
	}

	if err := cmd.Flags().Set("namespace", "custom-kueue"); err != nil {
		t.Fatalf("Set namespace: %v", err)
	}
	if got := explicitNamespace(cmd); got != "custom-kueue" {
		t.Fatalf("explicitNamespace() = %q, want custom-kueue", got)
	}
}
