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

package core

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

var _ = ginkgo.Describe("CapacityBufferConfig Validation", func() {
	ginkgo.BeforeEach(func() {
		fwk.StartManagerForSpec(ctx, cfg, managerSetup)
	})

	ginkgo.DescribeTable("Validate retryStrategy on creation",
		func(name string, retryStrategy *kueuealpha.CapacityBufferRetryStrategy, wantErr string) {
			config := &kueuealpha.CapacityBufferConfig{
				Name: name,
				Spec: kueuealpha.CapacityBufferConfigSpec{
					ManagedResources: []corev1.ResourceName{corev1.ResourceCPU},
					RetryStrategy:    retryStrategy,
				},
			}

			err := k8sClient.Create(ctx, config)
			if wantErr == "" {
				gomega.Expect(err).To(gomega.Succeed())
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, config, true)
			} else {
				gomega.Expect(err).To(utiltesting.BeInvalidError())
				gomega.Expect(err.Error()).To(gomega.ContainSubstring(wantErr))
			}
		},
		ginkgo.Entry("Allow the defaults",
			"cbc-defaults", nil, ""),
		ginkgo.Entry("Allow a base equal to the max",
			"cbc-base-equals-max", &kueuealpha.CapacityBufferRetryStrategy{BackoffBaseSeconds: new(int32(1)), BackoffMaxSeconds: new(int32(1))}, ""),
		ginkgo.Entry("Disallow a zero base",
			"cbc-zero-base", &kueuealpha.CapacityBufferRetryStrategy{BackoffBaseSeconds: new(int32(0))},
			"spec.retryStrategy.backoffBaseSeconds"),
		ginkgo.Entry("Disallow a zero max",
			"cbc-zero-max", &kueuealpha.CapacityBufferRetryStrategy{BackoffBaseSeconds: new(int32(1)), BackoffMaxSeconds: new(int32(0))},
			"spec.retryStrategy.backoffMaxSeconds"),
		ginkgo.Entry("Disallow a base greater than the max",
			"cbc-base-above-max", &kueuealpha.CapacityBufferRetryStrategy{BackoffBaseSeconds: new(int32(120)), BackoffMaxSeconds: new(int32(60))},
			"backoffBaseSeconds must be less than or equal to backoffMaxSeconds"),
		ginkgo.Entry("Disallow a base greater than the default max",
			"cbc-base-above-default-max", &kueuealpha.CapacityBufferRetryStrategy{BackoffBaseSeconds: new(int32(3600))},
			"backoffBaseSeconds must be less than or equal to backoffMaxSeconds"),
	)
})
