# TAS Performance Test

Performance test framework for TAS (Topology Aware Scheduling) in Kueue.

## Overview

This test framework measures the performance of Kueue's TAS feature by:
- Generating a hierarchical topology (blocks → racks → nodes)
- Creating nodes with topology labels
- Submitting workloads with topology constraints
- Measuring time-to-admission and resource utilization
- Validating performance against expected thresholds

## Components

### Runner
Main application that orchestrates the performance test:
- Generates topology nodes with hierarchical labels
- Creates Topology and ResourceFlavor resources
- Generates cohorts, queues, and workloads
- Monitors workload admission and completion
- Collects performance statistics

### MinimalKueue
Lightweight Kueue controller manager with:
- Core controllers (Workload, LocalQueue, ClusterQueue, ResourceFlavor)
- TAS controllers (Topology, Resource Flavor, Topology Ungater)
- Scheduler with TAS support
- Optional CPU profiling and metrics

### Generator
Creates test resources:
- **Topology Generator**: Creates nodes with hierarchical labels (block/rack/node)
- **Workload Generator**: Creates workloads with TAS topology constraints

### Recorder
Tracks events and generates statistics:
- Workload state transitions (pending → admitted → finished)
- ClusterQueue resource usage over time
- Time-to-admission metrics
- Summary statistics

### Checker
Validates performance results against expected ranges:
- Command execution time
- ClusterQueue utilization
- Workload admission times

## Usage

### Build MinimalKueue

```bash
make minimalkueue-tas
```

This creates `bin/minimalkueue-tas` with TAS controller support.

### Run Performance Test

```bash
make run-performance-tas
```

Runs the performance test with minimalkueue in an envtest environment.

**Optional environment variables**:
- `SCALABILITY_CPU_PROFILE=1` - Generate CPU profile
- `SCALABILITY_KUEUE_LOGS=1` - Capture minimalkueue logs
- `SCALABILITY_KUEUE_LOGS_LEVEL=2` - Set log level (default: 2)
- `SCALABILITY_SCRAPE_INTERVAL=1s` - Scrape Prometheus metrics every second
- `SCALABILITY_TAS_GENERATOR_CONFIG=path/to/config.yaml` - Custom config

**Output**: `bin/run-performance-tas/`
- `summary.yaml` - Overall statistics
- `wlStates.csv` - Workload state timeline
- `cqStates.csv` - ClusterQueue state timeline
- `minimalkueue-tas.stats.yaml` - Process statistics
- `minimalkueue-tas.cpu.prof` - CPU profile (if enabled)
- `metricsDump.tgz` - Prometheus metrics (if scraping enabled)

### Run in Existing Cluster

```bash
make run-performance-tas-in-cluster
```

Runs against your current kubeconfig cluster. Requires:
- Kueue installed and running
- Proper RBAC permissions

**Optional environment variables**:
- `SCALABILITY_SCRAPE_INTERVAL=1s` - Scrape metrics
- `SCALABILITY_SCRAPE_URL=http://...` - Kueue metrics endpoint

**Output**: `bin/run-performance-tas-in-cluster/`

### Validate Performance

```bash
make test-performance-tas
```

Runs the performance test and validates results against `default_rangespec.yaml`.

- ✅ Passes if metrics are within expected ranges
- ❌ Fails if performance degrades beyond thresholds

## Configuration

### Generator Config (`default_generator_config.yaml`)

Defines the test topology and workload patterns:

```yaml
topology:
  name: "default-topology"
  levels:
    - name: block
      count: 1
      nodeLabel: "cloud.provider.com/topology-block"
    - name: rack
      count: 5
      nodeLabel: "cloud.provider.com/topology-rack"
    - name: node
      count: 32
      nodeLabel: "kubernetes.io/hostname"
      capacity:
        cpu: "96"
        memory: "256Gi"

resourceFlavor:
  name: "tas-flavor"
  nodeLabel: "tas-node-group"

# Cohorts and workload definitions...
```

### Range Spec (`default_rangespec.yaml`)

Defines expected performance thresholds:

```yaml
cmd:
  maxWallMs: 600_000  # 10 minutes max

clusterQueueClassesMinUsage:
  cq: 40  # Minimum 40% utilization

wlClassesMaxAvgTimeToAdmissionMs:
  small-required-rack: 10_000   # 10 seconds
  medium-required-rack: 20_000  # 20 seconds
  large-required-rack: 30_000   # 30 seconds
```

## Analyzing Results

### Summary File (`summary.yaml`)

```yaml
workloadClasses:
  small-required-rack:
    total: 300
    admitted: 300
    finished: 300
    averageTimeToAdmissionMs: 3500
    # ...
clusterQueueClasses:
  cq:
    count: 6
    nominalQuota: 1200
    cpuUsed: 850000
    # ...
```

### Workload States CSV (`wlStates.csv`)

Timeline of workload state transitions:
```
timestamp,class,state,count
2024-01-20T10:00:00Z,small-required-rack,pending,50
2024-01-20T10:00:03Z,small-required-rack,admitted,30
# ...
```

### ClusterQueue States CSV (`cqStates.csv`)

Timeline of queue resource usage:
```
timestamp,class,pending,reserving,admitted,nominalQuota,borrowingLimit
2024-01-20T10:00:00Z,cq,0,0,0,200,400
# ...
```

## Related Documentation

- [TAS User Guide](https://kueue.sigs.k8s.io/docs/tasks/manage/setup_topology/)
- [Performance Test Design Issue #4634](https://github.com/kubernetes-sigs/kueue/issues/4634)
- [Scheduler Performance Test](../scheduler/README.md)
