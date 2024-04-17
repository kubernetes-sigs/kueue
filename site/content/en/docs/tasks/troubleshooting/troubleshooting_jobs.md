---
title: "Troubleshooting Jobs"
date: 2024-03-21
weight: 1
description: >
  Troubleshooting the status of a Job
---

This doc is about troubleshooting pending kubernetes Jobs, however, most of the ideas can be extrapolated
to other supported CRDs.

## Identifying the Workload for your Job

For each Job (a Kubernetes Job or a CRD), Kueue creates a [Workload](/docs/concepts/workload) object to hold the
information about the admission of the Job. The Workload object allows Kueue to make admission decisions without
knowing the specifics of each CRD.

There are multiple ways to find the Workload for a Job. In the following examples, let's assume your
Job is called `my-job` in the `my-namespace` namespace.

1. You can obtain the Workload name from the Job events, running the following command:

   ```bash
   kubectl describe job -n my-namespace my-job
   ```

   The relevant event will look like the following:

   ```
     Normal  CreatedWorkload   24s   batch/job-kueue-controller  Created Workload: my-namespace/job-my-job-19797
   ```

2. Kueue includes the UID of the source Job in the label `kueue.x-k8s.io/job-uid`.
   You can obtain the workload name with the following commands:

   ```bash
   JOB_UID=$(kubectl get job -n my-namespace my-job -o jsonpath='{.metadata.uid}')
   kubectl get workloads -n my-namespace -l "kueue.x-k8s.io/job-uid=$JOB_UID"
   ```

   The output looks like the following:

   ```
   NAME               QUEUE        ADMITTED BY     AGE
   job-my-job-19797   user-queue   cluster-queue   9m45s
   ```

3. You can list all of the workloads in the same namespace of your job and identify the one
   that matches the format `<api-name>-<job-name>-<hash>`.
   The command may look like the following:

   ```bash
   kubectl get workloads -n my-namespace | grep job-my-job
   ```

   The output looks like the following:

   ```
   NAME               QUEUE        ADMITTED BY     AGE
   job-my-job-19797   user-queue   cluster-queue   9m45s
   ```

## Does my cluster-queue match the job resource requests?

When you submit a job that has a resource request, for example:

```bash
$ kubectl get jobs job-0-9-size-6 -o json | jq -r .spec.template.spec.containers[0].resources
```console
{
  "limits": {
    "cpu": "2"
  },
  "requests": {
    "cpu": "2"
  }
}
```

If your cluster queue does not have a definition for the _requests_ the job cannot be admitted. For the job above you would want to ensure that you've defined "cpu" under `resoujrceGroups`, as shown below:


```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: ClusterQueue
metadata:
  name: "cluster-queue"
spec:
  namespaceSelector: {}
  resourceGroups:
  - coveredResources: ["cpu"]
    flavors:
    - name: "default-flavor"
      resources:
      - name: "cpu"
        nominalQuota: 40
```

Generally speaking, if you find that workloads are not admitted, a good debugging strategy is to check that you've covered resources that are requested, and then carefully describe each of the job and workload, along with looking at logs for kueue. You can typically find a log or condition message that gives a hint to why the queue is not moving.

## Is my Job running?

To know whether your Job is running, look for the value of the `.spec.suspend` field, by
running the following command:

```
kubectl get job -n my-namespace my-job -o jsonpath='{.spec.suspend}'
```

If your Job is running, the output will be `false`.

## Is my Job admitted?

If your Job is not running, you should first check whether Kueue has admitted the Workload.

The starting point to know whether a Job was admitted, it's pending or was not yet attempted
for admission is to look at the Workload status.

Run the following command to obtain the full status of a Workload:

```
kubectl get workload -n my-namespace my-workload -o yaml
```

### Admitted Workload

If your Job is admitted, the Workload should have a status similar to the following:

```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: Workload
...
status:
  admission:
    clusterQueue: cluster-queue
    podSetAssignments:
    - count: 3
      flavors:
        cpu: default-flavor
        memory: default-flavor
      name: main
      resourceUsage:
        cpu: "3"
        memory: 600Mi
  conditions:
  - lastTransitionTime: "2024-03-19T20:49:17Z"
    message: Quota reserved in ClusterQueue cluster-queue
    reason: QuotaReserved
    status: "True"
    type: QuotaReserved
  - lastTransitionTime: "2024-03-19T20:49:17Z"
    message: The workload is admitted
    reason: Admitted
    status: "True"
    type: Admitted
```

### Pending Workload

If Kueue has attempted to admit the Workload, but failed to so due to lack of quota,
the Workload should have a status similar to the following:

```yaml
status:
  conditions:
  - lastTransitionTime: "2024-03-21T13:43:00Z"
    message: 'couldn''t assign flavors to pod set main: insufficient quota for cpu
      in flavor default-flavor in ClusterQueue'
    reason: Pending
    status: "False"
    type: QuotaReserved
```

### Unattempted Workload

When using a [ClusterQueue](/docs/concepts/cluster_queue) with the `StrictFIFO`
[`queueingStrategy`](/docs/concepts/cluster_queue/#queueing-strategy), Kueue only attempts
to admit the head of each ClusterQueue. As a result, if Kueue didn't attempt to admit
a Workload, the Workload status would not contain any condition.

### Misconfigured LocalQueues or ClusterQueues

If your Job references a LocalQueue that doesn't exist or the LocalQueue or ClusterQueue
that it references is misconfigured, the Workload status would look like the following:

```yaml
status:
  conditions:
  - lastTransitionTime: "2024-03-21T13:55:21Z"
    message: LocalQueue user-queue doesn't exist
    reason: Inadmissible
    status: "False"
    type: QuotaReserved
```

## Is my Job preempted?

If your Job is not running, and your ClusterQueues have [preemption](/docs/concepts/cluster_queue/#preemption) enabled,
you should check whether Kueue preempted the Workload.


```yaml
status:
  conditions:
  - lastTransitionTime: "2024-03-21T15:49:56Z"
    message: 'couldn''t assign flavors to pod set main: insufficient unused quota
      for cpu in flavor default-flavor, 9 more needed'
    reason: Pending
    status: "False"
    type: QuotaReserved
  - lastTransitionTime: "2024-03-21T15:49:55Z"
    message: Preempted to accommodate a higher priority Workload
    reason: Preempted
    status: "True"
    type: Evicted
  - lastTransitionTime: "2024-03-21T15:49:56Z"
    message: The workload has no reservation
    reason: NoReservation
    status: "False"
    type: Admitted
```

The `Evicted` condition shows that the Workload was preempted and the `QuotaReserved` condition with `status: "True"`
shows that Kueue already attempted to admit it again, unsuccessfully in this case.
