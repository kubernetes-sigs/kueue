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

package util

import (
	"context"

	kfmpi "github.com/kubeflow/mpi-operator/pkg/apis/kubeflow/v2beta1"
	kftrainerapi "github.com/kubeflow/trainer/v2/pkg/apis/trainer/v1alpha1"
	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	awv1beta2 "github.com/project-codeflare/appwrapper/api/v1beta2"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	appsv1 "k8s.io/api/apps/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	jobset "sigs.k8s.io/jobset/api/jobset/v1alpha2"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func PolicyRule(group, resource string, verbs ...string) rbacv1.PolicyRule {
	return rbacv1.PolicyRule{
		APIGroups: []string{group},
		Resources: []string{resource},
		Verbs:     verbs,
	}
}

var resourceVerbs = []string{"create", "update", "patch", "delete", "get", "list", "watch"}

var frameworkRules = map[string][]rbacv1.PolicyRule{
	"batch/job": {
		PolicyRule(batchv1.SchemeGroupVersion.Group, "jobs", resourceVerbs...),
		PolicyRule(batchv1.SchemeGroupVersion.Group, "jobs/status", "get"),
	},
	"jobset.x-k8s.io/jobset": {
		PolicyRule(jobset.SchemeGroupVersion.Group, "jobsets", resourceVerbs...),
		PolicyRule(jobset.SchemeGroupVersion.Group, "jobsets/status", "get"),
	},
	"pod": {
		PolicyRule(corev1.SchemeGroupVersion.Group, "pods", resourceVerbs...),
		PolicyRule(corev1.SchemeGroupVersion.Group, "pods/status", "get"),
	},
	"statefulset": {
		PolicyRule(appsv1.SchemeGroupVersion.Group, "statefulsets", resourceVerbs...),
		PolicyRule(appsv1.SchemeGroupVersion.Group, "statefulsets/status", "get"),
	},
	"kubeflow.org/tfjob": {
		PolicyRule(kftraining.SchemeGroupVersion.Group, "tfjobs", resourceVerbs...),
		PolicyRule(kftraining.SchemeGroupVersion.Group, "tfjobs/status", "get"),
	},
	"kubeflow.org/paddlejob": {
		PolicyRule(kftraining.SchemeGroupVersion.Group, "paddlejobs", resourceVerbs...),
		PolicyRule(kftraining.SchemeGroupVersion.Group, "paddlejobs/status", "get"),
	},
	"kubeflow.org/pytorchjob": {
		PolicyRule(kftraining.SchemeGroupVersion.Group, "pytorchjobs", resourceVerbs...),
		PolicyRule(kftraining.SchemeGroupVersion.Group, "pytorchjobs/status", "get"),
	},
	"kubeflow.org/xgboostjob": {
		PolicyRule(kftraining.SchemeGroupVersion.Group, "xgboostjobs", resourceVerbs...),
		PolicyRule(kftraining.SchemeGroupVersion.Group, "xgboostjobs/status", "get"),
	},
	"kubeflow.org/jaxjob": {
		PolicyRule(kftraining.SchemeGroupVersion.Group, "jaxjobs", resourceVerbs...),
		PolicyRule(kftraining.SchemeGroupVersion.Group, "jaxjobs/status", "get"),
	},
	"kubeflow.org/mpijob": {
		PolicyRule(kfmpi.SchemeGroupVersion.Group, "mpijobs", resourceVerbs...),
		PolicyRule(kfmpi.SchemeGroupVersion.Group, "mpijobs/status", "get"),
	},
	"workload.codeflare.dev/appwrapper": {
		PolicyRule(awv1beta2.GroupVersion.Group, "appwrappers", resourceVerbs...),
		PolicyRule(awv1beta2.GroupVersion.Group, "appwrappers/status", "get"),
	},
	"ray.io/rayjob": {
		PolicyRule(rayv1.SchemeGroupVersion.Group, "rayjobs", resourceVerbs...),
		PolicyRule(rayv1.SchemeGroupVersion.Group, "rayjobs/status", "get"),
	},
	"ray.io/raycluster": {
		PolicyRule(rayv1.SchemeGroupVersion.Group, "rayclusters", resourceVerbs...),
		PolicyRule(rayv1.SchemeGroupVersion.Group, "rayclusters/status", "get"),
	},
	"ray.io/rayservice": {
		PolicyRule(rayv1.SchemeGroupVersion.Group, "rayservices", resourceVerbs...),
		PolicyRule(rayv1.SchemeGroupVersion.Group, "rayservices/status", "get"),
	},
	"leaderworkerset.x-k8s.io/leaderworkerset": {
		PolicyRule(leaderworkersetv1.SchemeGroupVersion.Group, "leaderworkersets", resourceVerbs...),
		PolicyRule(leaderworkersetv1.SchemeGroupVersion.Group, "leaderworkersets/status", "get"),
	},
	"trainer.kubeflow.org/trainjob": {
		PolicyRule(kftrainerapi.SchemeGroupVersion.Group, "trainjobs", resourceVerbs...),
		PolicyRule(kftrainerapi.SchemeGroupVersion.Group, "trainjobs/status", "get"),
	},
}

// MultiKueueRulesForManager returns RBAC rules matching the integrations
// enabled in the Kueue manager configuration. Workload rules are always included.
func MultiKueueRulesForManager(ctx context.Context, k8sClient client.Client) []rbacv1.PolicyRule {
	cfg := GetKueueConfiguration(ctx, k8sClient)

	rules := []rbacv1.PolicyRule{
		PolicyRule(kueue.SchemeGroupVersion.Group, "workloads", resourceVerbs...),
		PolicyRule(kueue.SchemeGroupVersion.Group, "workloads/status", "get", "patch", "update"),
	}

	for _, framework := range cfg.Integrations.Frameworks {
		if r, ok := frameworkRules[framework]; ok {
			rules = append(rules, r...)
		}
	}
	return rules
}

// KubeconfigForMultiKueueSA creates RBAC resources and returns a kubeconfig for MultiKueue.
func KubeconfigForMultiKueueSA(ctx context.Context, c client.Client, restConfig *rest.Config, ns string, prefix string, clusterName string, rules []rbacv1.PolicyRule) ([]byte, error) {
	roleName := prefix + "-role"
	cr := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: roleName},
		Rules:      rules,
	}
	err := c.Create(ctx, cr)
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return nil, err
	}

	saName := prefix + "-sa"
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ns,
			Name:      saName,
		},
	}
	err = c.Create(ctx, sa)
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return nil, err
	}

	crb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: prefix + "-crb"},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.SchemeGroupVersion.Group,
			Kind:     "ClusterRole",
			Name:     roleName,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      saName,
				Namespace: ns,
			},
		},
	}
	err = c.Create(ctx, crb)
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return nil, err
	}

	// get the token
	token := &authenticationv1.TokenRequest{
		Spec: authenticationv1.TokenRequestSpec{
			// The 7d expiration duration matches the max expiration time value for this token
			// and is configured in [hack/testing/multikueue/worker-cluster.kind.yaml].
			ExpirationSeconds: ptr.To[int64](7 * 24 * 3600),
		},
	}
	err = c.SubResource("token").Create(ctx, sa, token)
	if err != nil {
		return nil, err
	}

	cfg := clientcmdapi.Config{
		Kind:       "config",
		APIVersion: "v1",
		Clusters: map[string]*clientcmdapi.Cluster{
			"default-cluster": {
				Server:                   GetClusterServerAddress(clusterName),
				CertificateAuthorityData: restConfig.CAData,
			},
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			"default-user": {
				Token: token.Status.Token,
			},
		},
		Contexts: map[string]*clientcmdapi.Context{
			"default-context": {
				Cluster:  "default-cluster",
				AuthInfo: "default-user",
			},
		},
		CurrentContext: "default-context",
	}
	return clientcmd.Write(cfg)
}

// CleanKubeconfigForMultiKueueSA removes the RBAC resources.
func CleanKubeconfigForMultiKueueSA(ctx context.Context, c client.Client, ns string, prefix string) error {
	roleName := prefix + "-role"

	err := c.Delete(ctx, &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: roleName}})
	if client.IgnoreNotFound(err) != nil {
		return err
	}

	err = c.Delete(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: prefix + "-sa"}})
	if client.IgnoreNotFound(err) != nil {
		return err
	}

	err = c.Delete(ctx, &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: prefix + "-crb"}})
	if client.IgnoreNotFound(err) != nil {
		return err
	}

	return nil
}

func MakeMultiKueueSecret(ctx context.Context, c client.Client, namespace string, name string, kubeconfig []byte) error {
	secret := utiltesting.MakeSecret(name, namespace).Data("kubeconfig", kubeconfig).Obj()
	err := c.Create(ctx, secret)
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

func CleanMultiKueueSecret(ctx context.Context, c client.Client, namespace string, name string) error {
	secret := utiltesting.MakeSecret(name, namespace).Obj()
	return client.IgnoreNotFound(c.Delete(ctx, secret))
}
