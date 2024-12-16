---
title: "Troubleshooting Jobs"
date: 2024-03-21
weight: 1
description: >
  Troubleshooting the status of a Job
---

This doc is about troubleshooting pending [Kubernetes Jobs](https://kubernetes.io/docs/concepts/workloads/controllers/job/),
however, most of the ideas can be extrapolated to other [supported job types](/docs/tasks/run).

See the [Kueue overview](/docs/overview/#high-level-kueue-operation) to visualize the components that collaborate to run a Job.

In this document, assume that your Job is called `my-job` and it's in the `my-namespace` namespace.

## Identifying the Workload for your Job

For each Job, Kueue creates a [Workload](/docs/concepts/workload) object to hold the
details about the admission of the Job, whether it was admitted or not.

To find the Workload for a Job, you can use any of the following steps:

* You can obtain the Workload name from the Job events by running the following command:

  ```bash
  kubectl describe job -n my-namespace my-job
  ```

  The relevant event will look like the following:

  ```
    Normal  CreatedWorkload   24s   batch/job-kueue-controller  Created Workload: my-namespace/job-my-job-19797
  ```

* Kueue includes the UID of the source Job in the label `kueue.x-k8s.io/job-uid`.
  You can obtain the workload name with the following commands:

  ```bash
  JOB_UID=$(kubectl get job -n my-namespace my-job -o jsonpath='{.metadata.uid}')
  kubectl get workloads -n my-namespace -l "kueue.x-k8s.io/job-uid=$JOB_UID"
  ```

  The output looks like the following:

  ```
  NAME               QUEUE         RESERVED IN   ADMITTED   AGE
  job-my-job-19797   user-queue    cluster-queue True       9m45s
  ```

* You can list all of the workloads in the same namespace of your job and identify the one
  that matches the format `<api-name>-<job-name>-<hash>`.
  You can run a command like the following:

  ```bash
  kubectl get workloads -n my-namespace | grep job-my-job
  ```

  The output looks like the following:

  ```
  NAME               QUEUE         RESERVED IN   ADMITTED   AGE
  job-my-job-19797   user-queue    cluster-queue True       9m45s
  ```

Once you have identified the name of the Workload, you can obtain all details by
running the following command:

```bash
kubectl describe workload -n my-namespace job-my-job-19797
```


## What ResourceFlavors does my Job use?

Once you [identified the Workload for you Job](/docs/tasks/troubleshooting/troubleshooting_jobs/#identifying-the-workload-for-your-job) run the following command to get all the details of your Workload:

```sh
kubectl describe workload -n my-namespace job-my-job-19797
```

The output should be similar to the following:

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
  ...
```

Now you can clearly see what `ResourceFlavors` your Job uses.

## Is my Job suspended?

To know whether your Job is suspended, look for the value of the `.spec.suspend` field, by
running the following command:

```
kubectl get job -n my-namespace my-job -o jsonpath='{.spec.suspend}'
```

When a job is suspended, Kubernetes does not create any [Pods](https://kubernetes.io/docs/concepts/workloads/pods/)
for the Job. If the Job has Pods already running, Kubernetes terminates and deletes these Pods.

A Job is suspended when Kueue hasn't admitted it yet or when it was preempted to
accommodate another job. To understand why your Job is suspended,
check the corresponding Workload object.

## Is my Job admitted?

If your Job is not running, check whether Kueue has admitted the Workload.

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
  ...
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
  resourceRequests:
    name: main
    resources:
      cpu:    3
      Memory: 600Mi
```

{{< feature-state state="beta" for_version="v0.10" >}}
{{% alert title="Note" color="primary" %}}

Populating the `status.resourceRequests` field of pending workloads is a Beta feature that is enabled by default.

You can disable it by setting the `WorkloadResourceRequestsSummary` feature gate. Check the [Installation](/docs/installation/#change-the-feature-gates-configuration) guide for details on feature gate configuration.
{{% /alert %}}

### Does my ClusterQueue have the resource requests that the job requires?

When you submit a job that has a resource request, for example:

```bash
$ kubectl get jobs job-0-9-size-6 -o json | jq -r .spec.template.spec.containers[0].resources
```
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

If your ClusterQueue does not have a definition for the `requests`, Kueue cannot admit the job. For the job above, you should define `cpu` quotas under `resourceGroups`. A ClusterQueue defining `cpu` quota looks like the following:

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

See [resources groups](https://kueue.sigs.k8s.io/docs/concepts/cluster_queue/#resource-groups) for more information.

### Pending Admission Checks

When the ClusterQueue has [admission checks](/docs/concepts/admission_check) configured, such as
[ProvisioningRequest](/docs/admission-check-controllers/provisioning) or [MultiKueue](/docs/concepts/multikueue),
a Workload might stay with a status similar to the following, until the admission checks pass:

```yaml
status:
  admission:
    clusterQueue: dws-cluster-queue
    podSetAssignments:
      ...
  admissionChecks:
  - lastTransitionTime: "2024-05-03T20:01:59Z"
    message: ""
    name: dws-prov
    state: Pending
  conditions:
  - lastTransitionTime: "2024-05-03T20:01:59Z"
    message: Quota reserved in ClusterQueue dws-cluster-queue
    reason: QuotaReserved
    status: "True"
    type: QuotaReserved
```

### Unattempted Workload

When using a [ClusterQueue](/docs/concepts/cluster_queue) with the `StrictFIFO`
[`queueingStrategy`](/docs/concepts/cluster_queue/#queueing-strategy), Kueue only attempts
to admit the head of each ClusterQueue. As a result, if Kueue didn't attempt to admit
a Workload, the Workload status might not contain any condition.

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

See [Troubleshooting Queues](/docs/tasks/troubleshooting/troubleshooting_queues) to understand why a
ClusterQueue or a LocalQueue is inactive.

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

## Are the Pods of my Job running?

When a Job is not suspended, Kubernetes creates Pods for this Job.
To check how many of these Pods are scheduled and running, check the Job status.

Run the following command to obtain the full status of a Job:

```bash
kubectl get job -n my-namespace my-job -o yaml
```

The output will be similar to the following:
```yaml
...
status:
  active: 5
  ready: 4
  succeeded: 1
  ...
```

The `active` field shows how many Pods for the Job exist, and the `ready` field shows how many of them are running.

To list all the Pods of the Job, run the following command:

```bash
kubectl get pods -n my-namespace -l batch.kubernetes.io/job-name=my-job
```

The output will be similar to the following:

```
NAME             READY   STATUS    RESTARTS   AGE
my-job-0-nxmpx   1/1     Running   0          3m20s
my-job-1-vgms2   1/1     Running   0          3m20s
my-job-2-74gw7   1/1     Running   0          3m20s
my-job-3-d4559   1/1     Running   0          3m20s
my-job-4-pg75n   0/1     Pending   0          3m20s
```

If a Pod or Pods are stuck in `Pending` status, check the latest events issued for the Pod
by running the following command:

```
kubectl describe pod -n my-namespace my-job-0-pg75n
```

If the Pod is unschedulable, the output will be similar to the following:

```
Events:
  Type     Reason             Age                From                Message
  ----     ------             ----               ----                -------
  Warning  FailedScheduling   13s (x2 over 15s)  default-scheduler   0/9 nodes are available: 9 node(s) didn't match Pod's node affinity/selector. preemption: 0/9 nodes are available: 9 Preemption is not helpful for scheduling.
  Normal   NotTriggerScaleUp  13s                cluster-autoscaler  pod didn't trigger scale-up:
```
