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

package baseline

import (
	"errors"
	"strings"
	"testing"

	"github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/types"

	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/util/behavioral/e2e"
)

func TestAssertMsgForMk(t *testing.T) {
	const message = "Workload not admitted in manager"
	workloadKey := types.NamespacedName{Name: "workload", Namespace: "default"}
	managerWorkload := utiltestingapi.MakeWorkload(workloadKey.Name, workloadKey.Namespace).Queue("manager").Obj()
	worker1Workload := utiltestingapi.MakeWorkload(workloadKey.Name, workloadKey.Namespace).Queue("worker1").Obj()
	worker2Workload := utiltestingapi.MakeWorkload(workloadKey.Name, workloadKey.Namespace).Queue("worker2").Obj()

	assertionMessage := e2e.AssertMsgForMk(
		t.Context(),
		message,
		workloadKey,
		utiltesting.NewClientBuilder().WithObjects(managerWorkload).Build(),
		utiltesting.NewClientBuilder().WithObjects(worker1Workload).Build(),
		utiltesting.NewClientBuilder().WithObjects(worker2Workload).Build(),
	)
	var failure string
	g := gomega.NewGomega(func(message string, _ ...int) {
		failure = message
	})
	g.Expect(errors.New("introduced error")).NotTo(gomega.HaveOccurred(), assertionMessage)
	if failure == "" {
		t.Fatal("Expected the introduced error to produce a Gomega failure")
	}

	wantSubstrings := []string{
		message,
		"Manager\n",
		"queueName: manager",
		"Worker1\n",
		"queueName: worker1",
		"Worker2\n",
		"queueName: worker2",
		"introduced error",
	}
	searchFrom := 0
	for _, want := range wantSubstrings {
		index := strings.Index(failure[searchFrom:], want)
		if index == -1 {
			t.Fatalf("AssertMsgForMk() output does not contain %q in the expected order:\n%s", want, failure)
		}
		searchFrom += index + len(want)
	}
}
