# Large-Scale Webhook Conversion Failure Reproduction

## Overview
This script is designed to reliably trigger and observe the Kubernetes API server instability (the infinite relist / compaction storm loop) caused by the webhook conversion bug when the `ConcurrentWatchObjectDecode` feature gate is disabled.

During reproduction, it was discovered that the instability is not exclusively dependent on **workload volume** (the sheer number of workloads). The **workload weight** (the byte size and complexity of each workload object) also plays a role in triggering the failure.

To reliably reproduce the issue, this directory introduces a highly configurable `populator` tool to precisely manipulate both volume and weight, paired with an orchestrator script (`repro.sh`) to automate the test environment and validation steps.

## Components

### 1. The Populator (`main.go`)
A Go-based Kubernetes workload generator that generates dynamic `kueue.x-k8s.io/v1beta1` `Workload` objects to flood the cluster.

**Scaling Dimensions:**
- **Volume:** Controlled by the `--count` flag (total number of workloads created).
- **Weight:** Artificially inflates the object's parsing time to stress the controller.
  - `--containers`: Number of containers per workload.
  - `--envs`: Number of environment variables per container (each variable is artificially padded with 100 bytes).

**API Protection:**
Creating heavily bloated workloads places immense pressure on the cluster. To avoid triggering validation webhook failure thresholds, the populator provides explicit throughput controls:
- `--workers`: Number of concurrent API callers.
- `--qps`: Uses the native `client-go` rate limiter to cleanly throttle workload creation.

### 2. The Orchestrator (`repro.sh`)
An end-to-end automation bash script that builds the environment, deploys Kueue, runs the populator, reboots the API server to clear the cache, and extracts the etcd compaction logs to determine if the instability successfully triggered.

**Environment Constraints:**
To successfully force the failure locally in `kind`, the script introduces specific cluster constraints:
- `--etcd-interval`: Artificially lowers the etcd compaction time to reduce the time window the controller has to parse workloads before a compaction storm forces an infinite relist loop.
- `--kueue-cpu`: Artificially throttles Kueue's available CPU, forcing the controller to take longer to parse the generated heavy workloads. The actual default is empty (no CPU limits/requests are applied) because the script `repro.sh` uses the `KUEUE_CPU` environment variable and only sets CPU when `KUEUE_CPU` is non-empty. Note that the `default` shown in the echo (`echo "KUEUE_CPU: ${KUEUE_CPU:-default}"`) is only for display and not functional.
- `--cleanup`: Controls whether the test cluster is automatically deleted at the end of the run (defaults to `false`).

## Usage

### Local Execution (Fully Automated)
Run the script natively against a local `kind` cluster. The script will handle cluster creation, workload generation, API server reboots, and automatically parse the apiserver logs at the end to determine success or failure.

Example:
```bash
./repro.sh \
  --workloads 2000 \
  --qps 2 \
  --workers 2 \
  --containers 60 \
  --envs 20 \
  --etcd-interval 10s
```

By default the script will run two passes:
1. `ConcurrentWatchObjectDecode=false`: Demonstrates the infinite relist failure.
2. `ConcurrentWatchObjectDecode=true`: Demonstrates clean, successful recovery.
But the user can configure the script to run only one pass when starting the script using `--gate` set to either `true` or `false`.

### Cloud Execution (Interactive)
Run the script against a managed cloud cluster using `--env cloud`.

Example:
```bash
./repro.sh \
  --env cloud \
  --workloads 2000 \
  --qps 2 \
  --workers 2 \
  --containers 60 \
  --envs 20
```

**Note:** Cloud execution is interactive. Because the script cannot dynamically manipulate the feature gate of a managed API server, nor natively force a cold-cache reboot, the script will pause at critical junctures to allow the user to perform manual interventions and check the logs in the Cloud Console.

### Run Recommendations

With a baseline configuration such as:
```bash
./repro.sh \
  --workloads 2000 \
  --containers 60 \
  --envs 20 \
  --etcd-interval 10s
```
The initial cluster creation takes ~2 minutes, and the failure detection stage takes another 1-2 minutes. 

The main variable in testing time and stability is the workload populator. Because it generates heavily nested payloads, 
the populator's concurrent creation process is **highly RAM-dependent**, controlled largely by `--workers` and `--qps`. 
The spike in RAM happens when using the `ConcurrentWatchObjectDecode` feature gate, and there is no option to limit that 
concurrent decoding. Therefore, runs without `ConcurrentWatchObjectDecode` can use way higher workers and QPS.

- **High RAM Available (e.g., 40 GB):** Running `--qps 100 --workers 10` will successfully populate the cluster in about 1 minute.
- **Lower RAM Available (e.g., 24 GB):** Running `--qps 60 --workers 5` safely prevents OOM crashes while populating the cluster in about **~3 minutes**. (While higher worker limits can survive sequential decoding, they will instantly trigger an OOM crash when the `ConcurrentWatchObjectDecode` feature gate natively parallelizes decoding).

### Instability Thresholds

Based on manual runs using the recommended padded workload settings (`--containers 60`, `--envs 20`) and the tight etcd compaction window (`--etcd-interval 10s`), the boundary for triggering the compaction storm / infinite relist loop was identified as follows:

- **`ConcurrentWatchObjectDecode=false`**: Instability consistently triggers at **~1000 workloads**.
- **`ConcurrentWatchObjectDecode=true`**: Instability consistently triggers at **~2500 workloads**.

This demonstrates roughly a **~2.5x improvement** in watch cache decoding throughput when the feature gate is enabled, allowing the API server to successfully process and decode 2.5 times as many heavy payloads within the exact same 10-second compaction window before falling into the infinite relist loop.
