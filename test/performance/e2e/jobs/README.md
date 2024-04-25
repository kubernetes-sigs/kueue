# Kueue e2e Performance Testing

## Measurements

### Job startup latency

How fast do jobs transition from `created` to `started` state?
Time spent between the transition from `job.CreationTimestamp.Time` to `job.Status.StartTime.Time` state.

High Job startup latency in Kueue is expected when the total quota is not enough to schedule all jobs immediately, because the jobs need to queue.

### Job startup throughput

The best workload admission rate per second within 1 minute intervals.
The rate is measured every 5 seconds (see more details in [PromQL examples](https://prometheus.io/docs/prometheus/latest/querying/examples/#subquery)):

`max_over_time(sum(rate(kueue_admitted_workloads_total{cluster_queue="{{$clusterQueue}}"}[1m]))[{{$testTimeout}}:5s])`

This measurement is not accurate if the cluster quota is big enough to schedule all workloads of the test immediately, because Kueue immediately admits all the workloads and the `kueue_admitted_workloads_total` never increases. In this case, the PromQL query returns 0.

## How to run the test?

### Prerequisites

1. Deploy [Kueue](https://github.com/kubernetes-sigs/kueue/blob/main/docs/setup/install.md)
2. Make sure you have `kubectl`, [jq](https://stedolan.github.io/jq/download/), [golang version](https://github.com/mikefarah/yq) of `yq` and `go`
3. Checkout `Clusterloader2` framework: https://github.com/kubernetes/perf-tests and build `clusterloader` binary:

    * change to `clusterloader2` directory
    * run `go build -o clusterloader './cmd/'`

### Run the test

1. Copy an environment file example to `.env` file:

    * `cp .env.example .env`

2. Edit the environment variables

| Variable          | Description |
| -----------       | ----------- |
| CL2_HOME_DIR      | Clusterloader home directory (checkout https://github.com/kubernetes/perf-tests)       |
| USE_KUEUE         | Run the performance test with Kueue (this requires Kueue to be pre-deployed to the cluster) or without Kueue    |
| EXPERIMENTS       | Configuration of iterations iterations (see configuration example in the file) |
| KUBECONFIG        | Kubeconfig file location |
| PROVIDER          | Kubernetes kind (tested on `gke` only)

3. Run the `run-test.sh` file

### Test results

Every test execution creates a `report_<timestamp>` directory inside `TEST_CONFIG_DIR` with `summary.csv` file, where the following metrics are available:

* P50 Job Create to start latency (ms)
* P90 Job Create to start latency (ms)
* P50 Job Start to complete latency (ms)
* P90 Job Start to complete latency (ms)
* Max Job Throughput (max jobs/s)
* Total Jobs
* Total Pods
* Duration (s)

Additionally, the following metrics are added to the results only for reference. Kueue doesn't influence them directly.

* Avg Pod Waiting time (s)
* P90 Pod Waiting time (s)
* Avg Pod Completion time (s)
* P90 Pod Completion time (s)
