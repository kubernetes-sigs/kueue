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

package tase2e

import (
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/kueue/test/util"
)

func expectJobWithSuspendedAndNodeSelectors(key types.NamespacedName, suspended bool, ns map[string]string) {
	job := &batchv1.Job{}
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, key, job)).To(gomega.Succeed())
		g.Expect(job.Spec.Suspend).Should(gomega.Equal(ptr.To(suspended)))
		g.Expect(job.Spec.Template.Spec.NodeSelector).Should(gomega.Equal(ns))
	}, util.Timeout, util.Interval).Should(gomega.Succeed())
}
