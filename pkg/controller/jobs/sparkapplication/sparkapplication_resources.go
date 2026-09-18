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
	"fmt"
	"regexp"
	"strconv"
	"strings"

	sparkv1beta2 "github.com/kubeflow/spark-operator/v2/api/v1beta2"
	sparkcommon "github.com/kubeflow/spark-operator/v2/pkg/common"
	sparkutil "github.com/kubeflow/spark-operator/v2/pkg/util"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// spark-operator does not compute Pod resources itself: it only translates the
// SparkApplication into spark-submit `--conf` properties, and Spark computes the
// driver and executor requests later, when the Pods are created. Kueue needs the
// same numbers before admission, so the functions below mirror Spark's
// BasicDriverFeatureStep, BasicExecutorFeatureStep and
// ResourceProfile.getResourcesForClusterManager (Spark 3.5).
//
// spark-submit keeps the last value of a property given multiple times, and
// spark-operator emits `spec.sparkConf` after `spec.memoryOverheadFactor` but
// before the typed driver/executor fields, so typed driver/executor fields win
// over `spec.sparkConf`, which in turn wins over `spec.memoryOverheadFactor`.

const (
	// Spark configuration keys not exported by spark-operator.
	sparkDriverMemoryOverheadFactor   = "spark.driver.memoryOverheadFactor"
	sparkExecutorMemoryOverheadFactor = "spark.executor.memoryOverheadFactor"
	sparkExecutorPysparkMemory        = "spark.executor.pyspark.memory"
	sparkMemoryOffHeapEnabled         = "spark.memory.offHeap.enabled"
	sparkMemoryOffHeapSize            = "spark.memory.offHeap.size"

	// Spark defaults for spark.{driver,executor}.cores and
	// spark.{driver,executor}.memory ("1g").
	defaultSparkCores     = 1
	defaultSparkMemoryMiB = 1024
	// ResourceProfile.MEMORY_OVERHEAD_MIN_MIB.
	minSparkMemoryOverheadMiB = 384
)

// sparkMemoryStringRegexp matches Spark's JavaUtils.byteStringAs format: an
// integer followed by an optional unit suffix.
var sparkMemoryStringRegexp = regexp.MustCompile(`^([0-9]+)([a-z]+)?$`)

var sparkMemoryUnitBytes = map[string]int64{
	"b":  1,
	"k":  1 << 10,
	"kb": 1 << 10,
	"m":  1 << 20,
	"mb": 1 << 20,
	"g":  1 << 30,
	"gb": 1 << 30,
	"t":  1 << 40,
	"tb": 1 << 40,
	"p":  1 << 50,
	"pb": 1 << 50,
}

// parseSparkMemoryBytes parses a Spark memory string such as "512m" or "2g".
// A bare number is interpreted in defaultUnitBytes, as Spark does for each
// property (MiB for spark.*.memory, bytes for spark.memory.offHeap.size).
func parseSparkMemoryBytes(s string, defaultUnitBytes int64) (int64, error) {
	m := sparkMemoryStringRegexp.FindStringSubmatch(strings.ToLower(strings.TrimSpace(s)))
	if m == nil {
		return 0, fmt.Errorf("invalid Spark memory string %q", s)
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid Spark memory string %q: %w", s, err)
	}
	unit := defaultUnitBytes
	if m[2] != "" {
		var ok bool
		if unit, ok = sparkMemoryUnitBytes[m[2]]; !ok {
			return 0, fmt.Errorf("invalid Spark memory string %q: unknown unit %q", s, m[2])
		}
	}
	return n * unit, nil
}

func parseSparkMemoryMiB(s string) (int64, error) {
	b, err := parseSparkMemoryBytes(s, 1<<20)
	return b >> 20, err
}

// sparkRoleConf gathers the driver or executor settings that determine the
// Pod resources, from both the typed spec fields and spec.sparkConf.
type sparkRoleConf struct {
	app  *sparkv1beta2.SparkApplication
	spec *sparkv1beta2.SparkPodSpec
	// coreRequest lives outside SparkPodSpec in DriverSpec/ExecutorSpec.
	coreRequest *string
	// Property keys for this role.
	coresKey, requestCoresKey, memoryKey, memoryOverheadKey, memoryOverheadFactorKey string
	isExecutor                                                                       bool
}

func newSparkRoleConf(pod *corev1.Pod, app *sparkv1beta2.SparkApplication) (*sparkRoleConf, error) {
	switch {
	case sparkutil.IsDriverPod(pod):
		return &sparkRoleConf{
			app:                     app,
			spec:                    &app.Spec.Driver.SparkPodSpec,
			coreRequest:             app.Spec.Driver.CoreRequest,
			coresKey:                sparkcommon.SparkDriverCores,
			requestCoresKey:         sparkcommon.SparkKubernetesDriverRequestCores,
			memoryKey:               sparkcommon.SparkDriverMemory,
			memoryOverheadKey:       sparkcommon.SparkDriverMemoryOverhead,
			memoryOverheadFactorKey: sparkDriverMemoryOverheadFactor,
		}, nil
	case sparkutil.IsExecutorPod(pod):
		return &sparkRoleConf{
			app:                     app,
			spec:                    &app.Spec.Executor.SparkPodSpec,
			coreRequest:             app.Spec.Executor.CoreRequest,
			coresKey:                sparkcommon.SparkExecutorCores,
			requestCoresKey:         sparkcommon.SparkKubernetesExecutorRequestCores,
			memoryKey:               sparkcommon.SparkExecutorMemory,
			memoryOverheadKey:       sparkcommon.SparkExecutorMemoryOverhead,
			memoryOverheadFactorKey: sparkExecutorMemoryOverheadFactor,
			isExecutor:              true,
		}, nil
	default:
		return nil, fmt.Errorf("pod %s is neither a Spark driver nor an executor", pod.Name)
	}
}

func (c *sparkRoleConf) sparkConf(key string) (string, bool) {
	v, ok := c.app.Spec.SparkConf[key]
	return v, ok
}

// typedOrSparkConf returns the typed spec field if set, otherwise the value
// of the corresponding spark-submit property from spec.sparkConf.
func (c *sparkRoleConf) typedOrSparkConf(typed *string, key string) *string {
	if typed != nil {
		return typed
	}
	if v, ok := c.sparkConf(key); ok {
		return &v
	}
	return nil
}

// cpuRequest mirrors Spark: spark.kubernetes.<role>.request.cores if set,
// otherwise spark.<role>.cores, which defaults to 1.
func (c *sparkRoleConf) cpuRequest() (resource.Quantity, error) {
	if requestCores := c.typedOrSparkConf(c.coreRequest, c.requestCoresKey); requestCores != nil {
		q, err := resource.ParseQuantity(*requestCores)
		if err != nil {
			return resource.Quantity{}, fmt.Errorf("failed to parse CPU requests %s: %w", *requestCores, err)
		}
		return q, nil
	}

	cores := int64(defaultSparkCores)
	if c.spec.Cores != nil {
		cores = int64(*c.spec.Cores)
	} else if v, ok := c.sparkConf(c.coresKey); ok {
		var err error
		if cores, err = strconv.ParseInt(v, 10, 32); err != nil {
			return resource.Quantity{}, fmt.Errorf("failed to parse %s=%s: %w", c.coresKey, v, err)
		}
	}
	return *resource.NewQuantity(cores, resource.DecimalSI), nil
}

// memoryRequest mirrors Spark: spark.<role>.memory (default 1g) plus
// spark.<role>.memoryOverhead, which defaults to
// max(memoryOverheadFactor * memory, 384MiB). Executors additionally get
// spark.executor.pyspark.memory for Python applications and
// spark.memory.offHeap.size when off-heap memory is enabled.
func (c *sparkRoleConf) memoryRequest() (resource.Quantity, error) {
	memoryMiB := int64(defaultSparkMemoryMiB)
	if memory := c.typedOrSparkConf(c.spec.Memory, c.memoryKey); memory != nil {
		var err error
		if memoryMiB, err = parseSparkMemoryMiB(*memory); err != nil {
			return resource.Quantity{}, fmt.Errorf("failed to parse memory requests: %w", err)
		}
	}

	overheadMiB, err := c.memoryOverheadMiB(memoryMiB)
	if err != nil {
		return resource.Quantity{}, err
	}
	totalMiB := memoryMiB + overheadMiB

	if c.isExecutor {
		extraMiB, err := c.executorExtraMemoryMiB()
		if err != nil {
			return resource.Quantity{}, err
		}
		totalMiB += extraMiB
	}
	return *resource.NewQuantity(totalMiB<<20, resource.BinarySI), nil
}

func (c *sparkRoleConf) memoryOverheadMiB(memoryMiB int64) (int64, error) {
	if overhead := c.typedOrSparkConf(c.spec.MemoryOverhead, c.memoryOverheadKey); overhead != nil {
		overheadMiB, err := parseSparkMemoryMiB(*overhead)
		if err != nil {
			return 0, fmt.Errorf("failed to parse memory overhead: %w", err)
		}
		return overheadMiB, nil
	}
	factor, err := c.memoryOverheadFactor()
	if err != nil {
		return 0, err
	}
	// Spark truncates the product to an int before applying the minimum.
	return max(int64(factor*float64(memoryMiB)), minSparkMemoryOverheadMiB), nil
}

// memoryOverheadFactor mirrors Spark: the role-specific
// spark.<role>.memoryOverheadFactor if set, otherwise
// spark.kubernetes.memoryOverheadFactor, which defaults to 0.1 for JVM
// applications and 0.4 for Python and R applications. The driver propagates
// its resolved default to the executors, so the non-JVM default applies to
// both roles.
func (c *sparkRoleConf) memoryOverheadFactor() (float64, error) {
	factor, ok := c.sparkConf(c.memoryOverheadFactorKey)
	if !ok {
		factor, ok = c.sparkConf(sparkcommon.SparkKubernetesMemoryOverheadFactor)
	}
	if !ok && c.app.Spec.MemoryOverheadFactor != nil && *c.app.Spec.MemoryOverheadFactor != "" {
		factor, ok = *c.app.Spec.MemoryOverheadFactor, true
	}
	if !ok {
		if c.app.Spec.Type == sparkv1beta2.SparkApplicationTypePython || c.app.Spec.Type == sparkv1beta2.SparkApplicationTypeR {
			return sparkcommon.DefaultNonJVMMemoryOverheadFactor, nil
		}
		return sparkcommon.DefaultJVMMemoryOverheadFactor, nil
	}
	f, err := strconv.ParseFloat(factor, 64)
	if err != nil || f < 0 {
		return 0, fmt.Errorf("invalid memory overhead factor %q: must be a non-negative number", factor)
	}
	return f, nil
}

func (c *sparkRoleConf) executorExtraMemoryMiB() (int64, error) {
	var extraMiB int64
	if v, ok := c.sparkConf(sparkExecutorPysparkMemory); ok && c.app.Spec.Type == sparkv1beta2.SparkApplicationTypePython {
		pysparkMiB, err := parseSparkMemoryMiB(v)
		if err != nil {
			return 0, fmt.Errorf("failed to parse %s: %w", sparkExecutorPysparkMemory, err)
		}
		extraMiB += pysparkMiB
	}
	if enabled, ok := c.sparkConf(sparkMemoryOffHeapEnabled); ok && strings.EqualFold(enabled, "true") {
		if v, ok := c.sparkConf(sparkMemoryOffHeapSize); ok {
			offHeapBytes, err := parseSparkMemoryBytes(v, 1)
			if err != nil {
				return 0, fmt.Errorf("failed to parse %s: %w", sparkMemoryOffHeapSize, err)
			}
			extraMiB += offHeapBytes >> 20
		}
	}
	return extraMiB, nil
}
