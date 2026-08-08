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
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("LabelsFromSelector", func() {
	ginkgo.DescribeTable("parses label selectors",
		func(selector string, want map[string]string, wantErr bool) {
			got, err := LabelsFromSelector(selector)
			if wantErr {
				gomega.Expect(err).To(gomega.HaveOccurred())
				return
			}
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(got).To(gomega.Equal(want))
		},
		ginkgo.Entry("single key", "app=foo", map[string]string{"app": "foo"}, false),
		ginkgo.Entry("multiple keys", "a=1,b=2", map[string]string{"a": "1", "b": "2"}, false),
		ginkgo.Entry("duplicate same value", "app=foo,app=foo", map[string]string{"app": "foo"}, false),
		ginkgo.Entry("empty selector", "", nil, true),
		ginkgo.Entry("conflicting duplicate key", "role=a,role=b", nil, true),
	)
})
