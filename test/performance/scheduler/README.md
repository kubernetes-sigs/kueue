# Scalability test

Is a test meant to detect regressions in the Kueue's overall scheduling capabilities. 

# Components
In order to achieve this the following components are used:

## Runner

An application able to:
- generate a set of Kueue specific objects based on a config following the schema of [generator.yaml](`./configs/baseline/generator.yaml`)
- mimic the execution of the workloads
- monitor the created object and generate execution statistics based on the received events

Optionally it's able to run an instance of [minimalkueue](#MinimalKueue) in a dedicated [envtest](https://book.kubebuilder.io/reference/envtest.html) environment.

## MinimalKueue

A light version of the Kueue's controller manager consisting only of the core controllers and the scheduler.

It is designed to offer the Kueue scheduling capabilities without any additional components which may flood the optional cpu profiles taken during it's execution.


## Checker

Checks the results of a performance-scheduler against a set of expected value defined as [rangespec.yaml](configs/baseline/rangespec.yaml).

# Usage

## Run in an existing cluster

```bash
make run-performance-scheduler-in-cluster
```

Will run a performance-scheduler scenario against an existing cluster (connectable by the host's default kubeconfig), and store the resulting artifacts are stored in `$(PROJECT_DIR)/bin/run-performance-scheduler-in-cluster`.

The generation config to be used can be set in `SCALABILITY_GENERATOR_CONFIG` by default using `$(PROJECT_DIR)/test/performance/scheduler/configs/baseline/generator.yaml`

Setting `SCALABILITY_SCRAPE_INTERVAL` to an interval value and `SCALABILITY_SCRAPE_URL` to an URL exposing kueue's metrics will cause the scalability runner to scrape that URL every interval and store the results in `$(PROJECT_DIR)/bin/run-performance-scheduler-in-cluster/metricsDump.tgz`.

Check [installation guide](https://kueue.sigs.k8s.io/docs/installation) for cluster and [observability](https://kueue.sigs.k8s.io/docs/installation/#add-metrics-scraping-for-prometheus-operator).

## Run with minimalkueue

```bash
make run-performance-scheduler
```

Will run a performance-scheduler scenario against an [envtest](https://book.kubebuilder.io/reference/envtest.html) environment
and an instance of minimalkueue.
The resulting artifacts are stored in `$(PROJECT_DIR)/bin/run-performance-scheduler`.

The generation config to be used can be set in `SCALABILITY_GENERATOR_CONFIG` by default using `$(PROJECT_DIR)/test/performance/scheduler/configs/baseline/generator.yaml`

Setting `SCALABILITY_CPU_PROFILE=1` will generate a cpuprofile of minimalkueue in `$(PROJECT_DIR)/bin/run-performance-scheduler/minimalkueue.cpu.prof`

Setting `SCALABILITY_KUEUE_LOGS=1` will save the logs of minimalkueue in  `$(PROJECT_DIR)/bin/run-performance-scheduler/minimalkueue.out.log` and  `$(PROJECT_DIR)/bin/run-performance-scheduler/minimalkueue.err.log`

Setting `SCALABILITY_SCRAPE_INTERVAL` to an interval value (e.g. `1s`) will expose the metrics of `minimalkueue` and have them collected by the scalability runner in `$(PROJECT_DIR)/bin/run-performance-scheduler/metricsDump.tgz` every interval. 

## Run performance-scheduler test

```bash
make test-performance-scheduler
```

Runs the performance-scheduler with minimalkueue and checks the results against `$(PROJECT_DIR)/test/performance/scheduler/configs/baseline/rangespec.yaml`

## Scrape result

The scrape result `metricsDump.tgz` contains a set of `<ts>.prometheus` files, where `ts` is the millisecond representation of the epoch time at the moment each scrape was stared and can be used during the import in a visualization tool.

If an instance of [VictoriaMetrics](https://docs.victoriametrics.com/) listening at `http://localhost:8428` is used, a metrics dump can be imported like:

```bash
 TMPDIR=$(mktemp -d)
 tar -xf ./bin/run-performance-scheduler/metricsDump.tgz -C $TMPDIR
 for file in ${TMPDIR}/*.prometheus; do timestamp=$(basename "$file" .prometheus);  curl -vX POST -T "$file" http://localhost:8428/api/v1/import/prometheus?timestamp="$timestamp"; done
 rm -r $TMPDIR

```

## TAS (Topology Aware Scheduling) Tests

The performance test framework supports TAS features using the same infrastructure with the `--enableTAS` flag. TAS tests measure performance with:
- Hierarchical topology (blocks → racks → nodes)
- Nodes with topology labels
- Workloads with topology constraints (required, preferred, balanced)

### Run TAS tests

```bash
# Run with minimalkueue in envtest
make run-tas-performance-scheduler

# Run in existing cluster
make run-tas-performance-scheduler-in-cluster

# Run and validate against thresholds
make test-tas-performance-scheduler
```

Same environment variables as standard tests apply (`SCALABILITY_CPU_PROFILE`, `SCALABILITY_KUEUE_LOGS`, etc.).
Results are stored in `$(PROJECT_DIR)/bin/run-tas-performance-scheduler/`.

### TAS configuration

TAS config (`configs/tas/generator.yaml`) extends the standard format with:

```yaml
topology:
  name: "default-topology"
  levels:
    - name: rack
      count: 10
      nodeLabel: "cloud.provider.com/topology-rack"
    - name: node
      count: 64
      nodeLabel: "kubernetes.io/hostname"
      capacity:
        cpu: "96"
        memory: "256Gi"

resourceFlavor:
  name: "tas-flavor"
  nodeLabel: "tas-node-group"

cohorts:
  # TAS workloads add:
  # - podCount: number of pods (default 1)
  # - tasConstraint: "required", "preferred", or "balanced"
  # - tasLevel: topology level label
  # - sliceSize: pods per slice for balanced placement
```

Performance thresholds are defined in `configs/tas/rangespec.yaml`.
