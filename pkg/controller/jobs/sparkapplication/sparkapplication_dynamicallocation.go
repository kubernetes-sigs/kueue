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

package sparkapplication

import (
	"cmp"
	"strconv"
	"strings"

	sparkcommon "github.com/kubeflow/spark-operator/v2/pkg/common"
)

// The executor count and dynamic allocation are resolved as Spark sees them, as
// the Pod resources are (see sparkapplication_resources.go): spark-operator
// passes spec.sparkConf to spark-submit first, then spec.executor.instances and,
// only when spec.dynamicAllocation.enabled is true, the spec.dynamicAllocation
// fields that are set. spark-submit keeps the last value of a property, so a
// typed field wins when spark-operator passes it, and spec.sparkConf applies
// otherwise.

// dynamicAllocation is the dynamic allocation Spark runs the application with.
type dynamicAllocation struct {
	enabled                                      bool
	minExecutors, maxExecutors, initialExecutors *int32
}

func (j *SparkApplication) dynamicAllocation() dynamicAllocation {
	da := dynamicAllocation{
		enabled:          j.sparkConfBool(sparkcommon.SparkDynamicAllocationEnabled),
		minExecutors:     j.sparkConfInt32(sparkcommon.SparkDynamicAllocationMinExecutors),
		maxExecutors:     j.sparkConfInt32(sparkcommon.SparkDynamicAllocationMaxExecutors),
		initialExecutors: j.sparkConfInt32(sparkcommon.SparkDynamicAllocationInitialExecutors),
	}
	if typed := j.Spec.DynamicAllocation; typed != nil && typed.Enabled {
		da.enabled = true
		da.minExecutors = cmp.Or(typed.MinExecutors, da.minExecutors)
		da.maxExecutors = cmp.Or(typed.MaxExecutors, da.maxExecutors)
		da.initialExecutors = cmp.Or(typed.InitialExecutors, da.initialExecutors)
	}
	return da
}

// executorInstances returns spec.executor.instances, or spark.executor.instances
// in spec.sparkConf, or nil if neither is set.
func (j *SparkApplication) executorInstances() *int32 {
	return cmp.Or(j.Spec.Executor.Instances, j.sparkConfInt32(sparkcommon.SparkExecutorInstances))
}

// sparkConfBool reports whether a boolean property of spec.sparkConf is true,
// ignoring case and surrounding whitespace, as Spark does.
func (j *SparkApplication) sparkConfBool(key string) bool {
	return strings.EqualFold(strings.TrimSpace(j.Spec.SparkConf[key]), "true")
}

// sparkConfInt32 returns a non-negative integer property of spec.sparkConf, or
// nil if it is unset or malformed.
func (j *SparkApplication) sparkConfInt32(key string) *int32 {
	n, err := strconv.ParseInt(strings.TrimSpace(j.Spec.SparkConf[key]), 10, 32)
	if err != nil || n < 0 {
		return nil
	}
	return new(int32(n))
}
