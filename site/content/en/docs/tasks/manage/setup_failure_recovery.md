---
title: "Setup failure recovery"
date: 2025-12-04
weight: 12
description: >
  Configure failure recovery mechanisms to unblock workloads after a node outage.
---

This describes how to configure the failure recovery mechanism that automatically transitions pods into the Failed phase when they are assigned to unreachable nodes and stuck terminating.
This is especially relevant for `Job`-based workloads that
specify [`podReplacementPolicy: Failed`](https://kubernetes.io/docs/concepts/workloads/controllers/job/#pod-replacement-policy),
preventing the `Job` from making progress if such scenario occurrs.

Without the failure recovery controller, a node failure (e.g. network partition or a malfunction of the `kubelet`) results in the pods running on that node becoming stuck.
The failure recovery mechanism unblocks workloads relying on pods being in a terminated state by forcefully moving them to the `Failed` phase after
a **grace period of 60 seconds** since they became stuck.

{{% alert title="Important" color="warning" %}}

The default Kubernetes behaviour of keeping the pods `Terminating` is a deliberate safety measure, as in that scenario the control plane has no way to confirm that the pods actually stopped
and released all their resources.
Before opting into the feature, make sure the affected workloads will not cause any data corruption if re-scheduled without cleanup.

{{% /alert %}}

# Prerequisites

- A running Kueue installation at **v0.15** or newer.
- The `FailureRecoveryPolicy` feature gate enabled in the Kueue controller manager. Check the [Installation](/docs/installation/#change-the-feature-gates-configuration) guide for details on feature gate configuration.

# Pod-level opt-in

Only pods annotated with `kueue.x-k8s.io/safe-to-forcefully-terminate` are managed by the failure recovery controller.

To opt-in, the annotation's value should be set to `true` in the pod template of the relevant workload. For example:
```yaml
apiVersion: batch/v1
kind: Job
# ...
spec:
  # ...
  template:
    metadata:
      annotations:
        kueue.x-k8s.io/safe-to-forcefully-terminate: true
    spec:
      # ...
```

{{% alert title="Note" color="primary" %}}

The controller is **not** limited to pods managed by Kueue (i.e. having the `kueue.x-k8s.io/managed` or `kueue.x-k8s.io/podset label`).
All pods in the cluster that have the new annotation will be affected.

{{% /alert %}}

# Example

The behavior of `FailureRecoveryPolicy` can be demonstrated by simulating a node issue by disabling the `kubelet` system service on the given node.

## 0. (Optional) Provision a cluster and configure Kueue

Although the approach described in below is applicable to all other cluster setups as well, for demonstration purposes the guide assumes a `kind` cluster running with the [`kind-cluster.yaml`](https://github.com/kubernetes-sigs/kueue/blob/81cae0608cac3d457b8ecf8a0480e7994c2151c1/hack/kind-cluster.yaml)
configuration available on GitHub.

```sh
wget https://raw.githubusercontent.com/kubernetes-sigs/kueue/81cae0608cac3d457b8ecf8a0480e7994c2151c1/hack/kind-cluster.yaml
kind create cluster --config hack/kind-cluster.yaml
```

To install Kueue into the cluster and enable the `FailureRecoveryPolicy` feature gate, follow the Kueue [Installation](/docs/installation) guide.
The guide assumes a single `ClusterQueue` and single `ResourceFlavor` setup, as described in the [Administer Cluster Quotas](/docs/tasks/manage/administer_cluster_quotas/#single-clusterqueue-and-single-resourceflavor-setup) guide.

## 1. Create a Job with `podReplacementPolicy: Failed` and `kueue.x-k8s.io/safe-to-forcefully-terminate: true`

As an example, a `Job` with `podReplacementPolicy: Failed` will be used to clearly demonstrate how the mechanism benefits certain workload types.

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  generateName: sample-job-
  namespace: default
  labels:
    kueue.x-k8s.io/queue-name: user-queue
spec:
  parallelism: 1
  completions: 1
  suspend: true
  podReplacementPolicy: Failed # Replace pods only if they are terminated
  template:
    metadata:
      annotations:
        kueue.x-k8s.io/safe-to-forcefully-terminate: "true" # Opt-in to failure recovery
    spec:
      affinity:
        podAntiAffinity:
          # Schedule on a different node than kueue-controller-manager
          preferredDuringSchedulingIgnoredDuringExecution:
          - weight: 1
            podAffinityTerm:
              labelSelector:
                matchExpressions:
                - key: app.kubernetes.io/name
                  operator: In
                  values:
                  - kueue
              topologyKey: kubernetes.io/hostname
              namespaceSelector: {}
      containers:
      - name: dummy-job
        image: registry.k8s.io/e2e-test-images/agnhost:2.53
        command: [ "/bin/sh" ]
        args: [ "-c", "sleep 300" ]
        resources:
          requests:
            cpu: "1"
            memory: "200Mi"
      restartPolicy: Never
```

Create the job:

```sh
kubectl create -f sample-job.yaml
```

And find the node its pod is scheduled on:

```sh
kubectl get pods -o wide
```

```
NAME                     READY   STATUS    RESTARTS   AGE   IP            NODE           NOMINATED NODE   READINESS GATES
sample-job-l8pts-qcwbc   1/1     Running   0          4s    10.244.2.29   kind-worker2   <none>           <none>
```

In this particular case, the pod is scheduled on a node called `kind-worker2`.

## 2. Disable the `kubelet` service in the node

The easiest way to simulate a node outage is to open a shell with `ssh` and disable the `kubelet` service.

In the case of a `kind` cluster, a shell can be opened with:
```sh
docker exec -it kind-worker2 bash
```

To stop the `kubelet` service, run:
```sh
systemctl stop kubelet
```

This will cause the node to stop sending health pings to the control plane.

## 3. Observe the node being down

The `kubelet` on worker nodes periodically sends heartbeat pings to the control plane to report their health.
If this this does not happen for `node-monitor-grace-period` (by default 50 seconds), the node's readiness status will be
deemed `Unknown` and a `node.kubernetes.io/unreachable` taint will be added to the node.

After waiting for the `node-monitor-grace-period` to elapse and running:
```sh
kubectl get nodes
```
the output should report the node's unreachability.
```
NAME                 STATUS     ROLES           AGE   VERSION
kind-control-plane   Ready      control-plane   31h   v1.34.0
kind-worker          Ready      <none>          31h   v1.34.0
kind-worker2         NotReady   <none>          31h   v1.34.0
```

## 4. Observe the pod being terminated

By default, pods have a 5 minute toleration for the `node.kubernetes.io/unreachable` taint.
As such, for the first 5 minutes after the node status was reported as `NotReady` (and the taint was added), the
pod should continue running.

After the 5 minute grace period elapses, the pod will be marked for termination:
```sh
kubectl get pods
```
Showing:
```
NAME                     READY   STATUS        RESTARTS   AGE
sample-job-l8pts-qcwbc   1/1     Terminating   0          6m16s
```

Without the failure recovery policy, the pod will be unable to progress past this point and will be stuck in the
`Terminating` status until the node goes back online or the administrator intervenes manually.

## 5. Observe the pod transition to `Failed` and a replacement being scheduled

By default, pods have a 30 second grace period to terminate gracefully.
Additionally, the failure recovery controller also waits 60 seconds before forcefully marking the stuck pod as `Failed`.
In total, the pod should transition to `Failed` after 90 seconds since the `deletionTimestamp` was added to the pod.

Fetching all the pods after it elapses:
```sh
kubectl get pods -o wide
```
shows that the "stuck" pod now reports the `Failed` status and a replacement was scheduled per the `podReplacementPolicy`.
```
NAME                     READY   STATUS    RESTARTS   AGE    IP            NODE           NOMINATED NODE   READINESS GATES
sample-job-l8pts-kxpng   1/1     Running   0          17s    10.244.1.9    kind-worker    <none>           <none>
sample-job-l8pts-qcwbc   1/1     Failed    0          8m4s   10.244.2.29   kind-worker2   <none>           <none>
```

The `Job` can now make progress on the remaining functional nodes.