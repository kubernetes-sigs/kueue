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

package behavioral

import (
	"context"

	"github.com/onsi/ginkgo/v2"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func CreateNamespaceWithLog(ctx context.Context, k8sClient client.Client, nsName string) *corev1.Namespace {
	ginkgo.GinkgoHelper()
	return CreateNamespaceFromObjectWithLog(ctx, k8sClient, utiltesting.MakeNamespace(nsName))
}

func CreateNamespaceFromPrefixWithLog(ctx context.Context, k8sClient client.Client, nsPrefix string) *corev1.Namespace {
	ginkgo.GinkgoHelper()
	return CreateNamespaceFromObjectWithLog(ctx, k8sClient, utiltesting.MakeNamespaceWithGenerateName(nsPrefix))
}

func CreateNamespaceFromObjectWithLog(ctx context.Context, k8sClient client.Client, ns *corev1.Namespace) *corev1.Namespace {
	MustCreate(ctx, k8sClient, ns)
	ginkgo.GinkgoLogr.Info("Created namespace", "namespace", ns.Name)
	return ns
}
