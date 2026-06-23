---
title: "Setup garbage-collection of workload"
date: 2025-05-16
weight: 11
description: >
  Configure automatic garbage-collection of finished and deactivated Workloads
  by defining retention policies.
---

This guide shows you how to enable and configure optional object retention
policies in Kueue to automatically delete finished or deactivated Workloads
after a specified time. By default, Kueue leaves all Workload objects in cluster
indefinitely; with object retention you can free up etcd storage and reduce
Kueue’s memory footprint.

## Prerequisites

- A running Kueue installation at **v0.12** or newer.

## Set up a retention policy

Follow the instructions described
[here](/docs/installation#install-a-custom-configured-released-version) to
install a release version by extending the configuration with the following
fields:

```yaml
      objectRetentionPolicies:
        workloads:
          afterFinished: "1h"
          afterDeactivatedByKueue: "1h"
```

### Workload Retention Policy

The retention policy for Workloads is defined in the
`.objectRetentionPolicies.workloads` field.
It contains the following optional fields:
- `afterFinished`: Duration after which finished Workloads are deleted.
- `afterDeactivatedByKueue`: Duration after which any Kueue-deactivated Workloads (such as a Job, JobSet, or other custom workload types) are deleted.


## Example

### Kueue Configuration

**Configure** Kueue with a 1m retention policy and enable [waitForPodsReady](/docs/tasks/manage/setup_wait_for_pods_ready.md):

```yaml
  objectRetentionPolicies:
    workloads:
      afterFinished: "1m"
      afterDeactivatedByKueue: "1m"
  waitForPodsReady:
    timeout: 2m
    recoveryTimeout: 1m
    blockAdmission: false
    requeuingStrategy:
      backoffLimitCount: 0
```

---

### Scenario A: Successfully finished Workload

1. **Submit** a simple Workload that should finish normally:

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: successful-cq
spec:
  namespaceSelector: {}
  resourceGroups:
  - coveredResources: [cpu]
    flavors:
    - name: objectretention-rf
      resources:
      - name: cpu
        nominalQuota: "2"
---
apiVersion: kueue.x-k8s.io/v1beta2
kind: LocalQueue
metadata:
  namespace: default
  name: successful-lq
spec:
  clusterQueue: successful-cq
---
apiVersion: batch/v1
kind: Job
metadata:
  name: successful-job
  namespace: default
  labels:
    kueue.x-k8s.io/queue-name: successful-lq
spec:
  parallelism: 1
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: c
          image: registry.k8s.io/e2e-test-images/agnhost:2.53
          args: ["entrypoint-tester"]
          resources:
            requests:
              cpu: "1"
```

2. Watch the status transition to `Finished`. After ~1 minute, Kueue will automatically delete it:

```bash
# ~1m after Finished
kubectl get workloads.kueue.x-k8s.io -n default
# <successful-workload not found>
   
kubectl get jobs -n default
# NAME             STATUS     COMPLETIONS   DURATION   AGE
# successful-job   Complete   1/1                      1m
```

---

### Scenario B: Evicted Workload via `waitForPodsReady`

1. **Configure** Kueue [deployment](/docs/installation#install-a-custom-configured-released-version) to have more resources available than the node can provide:

```yaml
        resources:
          limits:
            cpu: "100" # or any value greater than the node's capacity
```

2. **Submit** a Workload that requests more than is available on the node:

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: limited-cq
spec:
  namespaceSelector: {}
  resourceGroups:
  - coveredResources: ["cpu"]
    flavors:
    - name: objectretention-rf
      resources:
      - name: cpu
        nominalQuota: "100" # more than is available on the node
---
apiVersion: kueue.x-k8s.io/v1beta2
kind: LocalQueue
metadata:
  namespace: default
  name: limited-lq
spec:
  clusterQueue: limited-cq
---
apiVersion: batch/v1
kind: Job
metadata:
  name: limited-job
  namespace: default
  labels:
    kueue.x-k8s.io/queue-name: limited-lq
spec:
  parallelism: 1
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: c
          image: registry.k8s.io/e2e-test-images/agnhost:2.53
          args: ["entrypoint-tester"]
          resources:
            requests:
              cpu: "100" # more than is available on the node
```

3. Without all Pods ready, Kueue evicts and deactivates the Workload:

```bash
# ~2m after submission
kubectl get workloads.kueue.x-k8s.io -n default
# NAME                    QUEUE    RESERVED IN   ADMITTED   FINISHED   AGE
# limited-workload                               False                 2m
```

4. ~1 minute after eviction, the deactivated Workload is garbage-collected:

```bash
# ~1m after eviction
kubectl get workloads.kueue.x-k8s.io -n default
# <limited-workload not found>
   
kubectl get jobs -n default
# <limited-job not found>
```

## Notes

- `afterDeactivatedByKueue` is the duration to wait after *any* Kueue-managed Workload
  (such as a Job, JobSet, or other custom workload types)
  has been marked as deactivated by Kueue before automatically deleting it.
  Deletion of deactivated workloads may cascade to objects not created by Kueue,
  since deleting the parent Workload owner (e.g. JobSet) can trigger garbage-collection of dependent resources.
  If you manually / automatically deactivate the Workload by specifying `.spec.active=false`, `afterDeactivatedByKueue` is _NOT_ effective.
- If a retention duration is mis-configured (invalid duration),
  the controller will fail to start.
- Deletion is handled synchronously in the reconciliation loop; in
  clusters with thousands of expired Workloads it may take time to
  remove them all on first startup.
