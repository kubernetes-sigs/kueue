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

package scheduler

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap/zapcore"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	utilbuilder "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestLogDrsValuesWhenVerboseJSON(t *testing.T) {
	var buf bytes.Buffer
	logger := zap.New(zap.WriteTo(&buf), zap.JSONEncoder(), zap.Level(zapcore.Level(-5)))

	cache := schdcache.New(utilbuilder.NewFakeClient())

	ctx, log := utilbuilder.ContextWithLog(t)
	cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("default").Obj())

	cq := utiltestingapi.MakeClusterQueue("cq").
		Cohort("test-cohort").
		FairWeight(resource.MustParse("0")).
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("default").
				ResourceQuotaWrapper("cpu").NominalQuota("2").Append().
				Obj(),
		).Obj()
	_ = cache.AddClusterQueue(ctx, cq)

	lendingCq := utiltestingapi.MakeClusterQueue("lending-cq").
		Cohort("test-cohort").
		FairWeight(resource.MustParse("1")).
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("default").
				ResourceQuotaWrapper("cpu").NominalQuota("8").Append().
				Obj(),
		).Obj()
	_ = cache.AddClusterQueue(ctx, lendingCq)

	snapshot, err := cache.Snapshot(ctx)
	if err != nil {
		t.Fatalf("unexpected error while building snapshot: %v", err)
	}

	admission := utiltestingapi.MakeAdmission("cq").
		PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
			Assignment("cpu", "default", "5000").
			Obj()).Obj()

	wl := utiltestingapi.MakeWorkload("workload-1", "default").
		ReserveQuotaAt(admission, time.Now()).Obj()
	cache.AddOrUpdateWorkload(log, wl)
	wlInfo := workload.NewInfo(wl)
	snapshot.AddWorkload(wlInfo)

	lendingAdmission := utiltestingapi.MakeAdmission("lending-cq").
		PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
			Assignment("cpu", "default", "10").
			Obj()).Obj()

	wl2 := utiltestingapi.MakeWorkload("workload-2", "default").
		ReserveQuotaAt(lendingAdmission, time.Now()).Obj()

	cache.AddOrUpdateWorkload(log, wl2)
	wl2Info := workload.NewInfo(wl2)
	snapshot.AddWorkload(wl2Info)

	entries := []entry{
		{
			clusterQueueSnapshot: snapshot.ClusterQueues()["cq"],
			Info:                 *wlInfo,
		},
		{
			clusterQueueSnapshot: snapshot.ClusterQueues()["lending-cq"],
			Info:                 *wl2Info,
		},
	}
	fsi := makeFairSharingIterator(ctx, entries, workload.Ordering{})

	cohorts := snapshot.Cohorts()
	fsi.entryComparer.computeDRS(cohorts["test-cohort"], fsi.cqToEntry)

	fsi.entryComparer.logDrsValuesWhenVerbose(logger)

	logOutput := buf.String()
	wantSubstr := `"drsValues":[{"parentCohort":"test-cohort","workload":"default/workload-1","drs":"+Inf"},{"parentCohort":"test-cohort","workload":"default/workload-2","drs":"200"}]`

	if !strings.Contains(logOutput, wantSubstr) {
		t.Errorf("Unexpected JSON log output:\nWant substring: %s\nGot: %s", wantSubstr, logOutput)
	}
}
