---
title: "Setup Quota Release Strategy"
date: 2026-07-18
weight: 5
description: >
  Configure how Kueue releases quota for terminating jobs
---

By default, Kueue holds onto a workload's quota reservation until all of its pods have fully terminated (reached the `Succeeded` or `Failed` phase). While this ensures safety, it can delay the admission of queued workloads by tens of seconds while the active pods gracefully shut down.

To improve cluster throughput and reduce scheduling latency, administrators can configure Kueue to release quota earlier—as soon as pods *begin* terminating.

## Enabling Quota Release Strategy

This feature is configured via the `scheduling.quotaReleaseStrategy` field in the Kueue Manager configuration. It applies globally to all workloads managed by Kueue.

```yaml
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
# ... other configuration fields
scheduling:
  quotaReleaseStrategy: OnTerminalBestEffort
```

### Available Strategies

#### OnTermination (Default)
When this strategy is used, Kueue releases the quota as soon as pods enter the terminating state (i.e. they receive a deletion timestamp). This allows the next queued workload to be admitted and scheduled immediately, maximizing your cluster's resource utilization and throughput.

#### OnTerminalBestEffort
When this strategy is used, Kueue waits for the pods to fully terminate and completely release their physical resources before releasing quota. This is required if you are using Topology Aware Scheduling (TAS) to ensure physical nodes are completely empty before scheduling the next job.
