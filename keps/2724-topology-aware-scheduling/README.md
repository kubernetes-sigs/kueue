# KEP-2724: Topology Aware Scheduling

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1](#story-1)
    - [Story 2](#story-2)
    - [Story 3](#story-3)
    - [Story 4](#story-4)
    - [Story 5](#story-5)
    - [Story 6](#story-6)
    - [Story 7](#story-7)
    - [Story 8](#story-8)
    - [Story 9](#story-9)
  - [Notes/Constraints/Caveats (Optional)](#notesconstraintscaveats-optional)
    - [Integration support](#integration-support)
      - [Job](#job)
      - [Job with multi-level scheduling](#job-with-multi-level-scheduling)
      - [JobSet](#jobset)
      - [LeaderWorkerSet](#leaderworkerset)
      - [LeaderWorkerSet](#leaderworkerset-1)
      - [MPIJob with runLauncherAsWorker](#mpijob-with-runlauncherasworker)
    - [Support for the &quot;auto&quot; mode](#support-for-the-auto-mode)
    - [PodSetAssignment is per lowest-topology level](#podsetassignment-is-per-lowest-topology-level)
    - [Provisioning request and required mode](#provisioning-request-and-required-mode)
    - [kueue.x-k8s.io/tas Pod label](#kueuex-k8siotas-pod-label)
  - [Risks and Mitigations](#risks-and-mitigations)
    - [Non-exclusive use of nodes](#non-exclusive-use-of-nodes)
    - [Node topology changes](#node-topology-changes)
    - [Race condition when accounting for DaemonSet pods](#race-condition-when-accounting-for-daemonset-pods)
- [Design Details](#design-details)
  - [Hierarchy representation](#hierarchy-representation)
  - [Admin-facing API](#admin-facing-api)
  - [User-facing API](#user-facing-api)
  - [Validation](#validation)
    - [PodSet Slice size validation](#podset-slice-size-validation)
  - [Internal APIs](#internal-apis)
    - [Topology assignment representation](#topology-assignment-representation)
      - [Until v1beta1](#until-v1beta1)
      - [Since v1beta2](#since-v1beta2)
    - [Node failures](#node-failures)
      - [Until v0.13](#until-v013)
      - [Since v0.14](#since-v014)
    - [Tainted nodes treatment](#tainted-nodes-treatment)
      - [User stories](#user-stories-1)
  - [Implicit defaulting of TAS annotations](#implicit-defaulting-of-tas-annotations)
  - [Computing the assignment](#computing-the-assignment)
    - [Example](#example)
    - [Selecting the algorithm](#selecting-the-algorithm)
      - [Until v0.14](#until-v014)
      - [Since v0.15](#since-v015)
  - [Two-level Topology Aware scheduling](#two-level-topology-aware-scheduling)
    - [Example](#example-1)
  - [Multi-level Topology Aware scheduling](#multi-level-topology-aware-scheduling)
    - [Example](#example-2)
  - [Cross-PodSet Topology Aware scheduling](#cross-podset-topology-aware-scheduling)
    - [Ensure leader and workers end up on the same flavor](#ensure-leader-and-workers-end-up-on-the-same-flavor)
  - [Enforcing the assignment](#enforcing-the-assignment)
  - [Support for Elastic Workloads](#support-for-elastic-workloads)
  - [Balanced placement](#balanced-placement)
    - [Example](#example-3)
  - [Support for ProvisioningRequests](#support-for-provisioningrequests)
    - [Determining the need for second pass](#determining-the-need-for-second-pass)
    - [Targeting the newly provisioned nodes](#targeting-the-newly-provisioned-nodes)
    - [Error handling](#error-handling)
  - [Test Plan](#test-plan)
      - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
  - [Graduation Criteria](#graduation-criteria)
    - [Alpha:](#alpha)
    - [Beta](#beta)
    - [Stable](#stable)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [Account usage by watching DaemonSet pods](#account-usage-by-watching-daemonset-pods)
  - [Use label for workload](#use-label-for-workload)
  - [Implement it in ClusterAutoscaler or kube-scheduler](#implement-it-in-clusterautoscaler-or-kube-scheduler)
  - [Workload API alternatives](#workload-api-alternatives)
    - [Drop the topologyAssignment.levels field](#drop-the-topologyassignmentlevels-field)
    - [Rename the topologyAssignment.domains.values field as levelValues](#rename-the-topologyassignmentdomainsvalues-field-as-levelvalues)
  - [Drop dedicated TAS label](#drop-dedicated-tas-label)
  - [MostFreeCapacity algorithm](#mostfreecapacity-algorithm)
    - [Example](#example-4)
  - [TopologyAssignmentSlices as separate CRD instances](#topologyassignmentslices-as-separate-crd-instances)
<!-- /toc -->

## Summary

This KEP introduces a mechanism, called Topology Aware Scheduling (TAS), to
facilitate Job scheduling in Kueue by leveraging the information about the
hierarchical organization of a data center.

First, we start by observing that data centers have organizational units (like
racks and blocks). VMs running within the same organizational unit have better
network bandwidth than VMs on different units. Second, the units form
a hierarchical structure - there are multiple nodes within a rack, and there are
multiple racks within a block. We say that nods placed in different racks are
more distant than nodes placed within the same rack. Similarly, nodes placed in
different blocks are more distant than two nodes within the same block.

Based on these observations we propose a convention to expose the information
about the node placement in a data center hierarchy using node labels.

We also propose a set of APIs for Kueue administrators and users to utilize this
information in order to optimize the network throughput between the pods.

## Motivation

It is common that AI / ML workloads require a significant amount of pod-to-pod
communication to make progress. Thus, it is important, for runtime and overall
cost, to make sure there is a high throughput network connection between the
running pods.

For example, some workloads may run twice slower if there is a pod placed on
a node which belongs to a different block. However, currently the end user of
Kueue has no way to "required" that all pods of its workload run within the same
block.

### Goals

- allow a user to express that a workload can schedule only if all of its pods
  can be placed on nodes which are close together (within the same level of
  hierarchy)
- allow a user to express that a workload prefers to schedule all of its pods
  within the same level of hierarchy, but automatically relax the constraint if
  not possible.
- support Cluster Autoscaler via ProvisioningRequest

### Non-Goals

- support MultiKueue at the management cluster
- support Cluster Autoscaler without ProvisioningRequest
- support `preferred` topology for PodSet Slices

The above features might be pursued in the future in follow up KEPs.

## Proposal

* Introduce convention to represent the topological hierarchy as node labels
* Provide API to configure the list of node labels representing levels in the
  hierarchy per Resource Flavor
* Introduce a set of Job annotations which allow to describe requirements for
  TAS

### User Stories

#### Story 1

As an ML researcher I run AI training workloads which require exchanging huge
amounts of data between the worker Pods. I use k8s Jobs.

A) The workloads take twice longer and are twice more expensive when there are
pods running in different data center racks. I don't want to start my workload
if the pods cannot be placed within the same rack.

B) I would like to optimize the runtime of the workloads, by placing the pods as
close to another as possible, ideally within a single rack, but I'm ok running
the workload on nodes scattered across data center if placing all pods within
the same hierarchy rack is not possible at the given time.

#### Story 2

Similar to [Story 1](#story-1), but I would like to run my Workloads using
JobSet with multiple ReplicatedJob instances per worker
([`replicas>0`](https://github.com/kubernetes-sigs/jobset/blob/d7c1dadd55906dec1d1275bcb7b73f08d1fa509f/api/jobset/v1alpha2/jobset_types.go#L227)).

To achieve optimal performance I need to ensure that every ReplicatedJob
instance is contained within a "rack".

#### Story 3

Similar to [Story 1](#story-1), but I use multi-template Jobs (JobSet, MPIJob,
TFJob) and Pods belonging to different templates also need to exchange sizable
amount of data, impacting the execution time. I would like to be able to
indicate that (1) all worker Pods are contained within a "rack", but also all
Pods in the workload are contained within a "block".

Note: not planned for [Alpha](#alpha), to be evaluated for [Beta](#beta).

#### Story 4

Extension to [Story 1](#story-1), as a AI/ML researcher I use ML frameworks
were the majority of communication happens between Pods with consecutive indexes
(ranks). The order of Pods correspond to their indexes for the Job and JobSet
CRDs that I use. So, for optimal performance I would like the Pods with
consecutive ranks to be placed within the same topology domain (if possible).

Extension: support for indicating the order of pods for external Jobs.

#### Story 5

Similar to [Story 2](#story-2), but I want all the Jobs in ReplicatedJob to run
within a "block", but each Job should also run within a "host" within that "block".

#### Story 6

Similar to [Story 1](#story-1), but I want Leader and its Workers within a single replica
in LeaderWorkerSet to run within a "rack".

#### Story 7

Similar to [Story 1](#story-1), but I want Leader and its Workers across multiple PodSets within a single Workload
for MPIJob with runLauncherAsWorker (`.spec.runLauncherAsWorker`) which should be scheduled considering
Pod index order.

#### Story 8

Similar to [Story 7](#story-7), but I want Leader and its Workers across multiple PodSets within a single Workload
for LeaderWorkerSet with multiple PodTemplates (`.spec.leaderWorkerTemplate.leaderTemplate` and `.spec.leaderWorkerTemplate.workerTemplate`) 
which should be scheduled considering Pod index order even if [Cross-PodSet Topology Aware scheduling](#cross-podset-topology-aware-scheduling)
is __NOT__ enabled.

#### Story 9

Similar to [Story 1](#story-1), but I want a Job's Pods to be placed across a
multi-layer topology based on user-specified constraints. For example, I want
to ensure that a Job is scheduled onto the same data center, in multiples
of 64 on the same "block", and in multiples of 16 on the same "rack".

### Notes/Constraints/Caveats (Optional)

#### Integration support

In the alpha iteration we aim to support Job, JobSet, and LeaderWorkerSet. If other integrations
follow naturally we may as well support them in alpha.

##### Job

**Example**:

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  namespace: tas-example-job
  labels:
    kueue.x-k8s.io/queue-name: user-queue
spec:
  parallelism: 10
  completions: 10
  template:
    metadata:
      annotations:
        kueue.x-k8s.io/podset-required-topology: cloud.provider.com/topology-rack
    spec:
      containers:
      - name: worker
        image: registry.k8s.io/e2e-test-images/agnhost:2.53
        args: ["pause"]
        resources:
          requests:
            cpu: "4"
      restartPolicy: Never
```

In this example we indicate that all Pods created by the Job should be contained
within the same "rack".

##### Job with multi-level scheduling

According to [Story 8](#story-8), some users would like to place Pods of a Job
across multi-layer topologies.

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  namespace: tas-example-job
  labels:
    kueue.x-k8s.io/queue-name: user-queue
spec:
  parallelism: 128
  completions: 128
  template:
    metadata:
      annotations:
        kueue.x-k8s.io/podset-required-topology: cloud.provider.com/datacenter
        kueue.x-k8s.io/podset-slice-required-topology-constraints: |
          [
            {"topology": "cloud.provider.com/aizone", "size": "64"},
            {"topology": "cloud.provider.com/block", "size": "32"},
            {"topology": "cloud.provider.com/rack", "size": "16"},
          ]
    spec:
      containers:
      - name: worker
        image: registry.k8s.io/e2e-test-images/agnhost:2.53
```

This example ensures the Job is placed within a data center symmetrically,
while leaving room for the user to tune the "multiple-of" knob on each layer.
This setup achieves "gang-of-gang-of-gang" semantics in complex NVIDIA
GB200/GB300 topology architectures.

##### JobSet

One complication we noticed for JobSet is that the proposed design assumes
injecting the dedicated scheduling gate, called `kueue.x-k8s.io/topology` into
the PodTemplate. This required a JobSet fix which is released in 0.6
(see [PR](https://github.com/kubernetes-sigs/jobset/pull/623)).

**Example**:

```yaml
apiVersion: jobset.x-k8s.io/v1alpha2
kind: JobSet
metadata:
  name: tas-example-jobset
  labels:
    kueue.x-k8s.io/queue-name: user-queue
spec:
  replicatedJobs:
  - name: leader
    replicas: 1
    template:
      spec:
        completions: 1
        parallelism: 1
        template:
          metadata:
            annotations:
              kueue.x-k8s.io/podset-required-topology: cloud.provider.com/topology-rack
          spec:
            containers:
            - name: leader
              image: registry.k8s.io/e2e-test-images/agnhost:2.53
              args: ["pause"]
  - name: workers
    replicas: 2
    template:
      spec:
        completions: 2
        parallelism: 2
        template:
          metadata:
            annotations:
              kueue.x-k8s.io/podset-preferred-topology: cloud.provider.com/topology-rack
          spec:
            containers:
            - name: worker
              image: registry.k8s.io/e2e-test-images/agnhost:2.53
              args: ["pause"]
```

In this example we say that the PodSet corresponding to the leader requires
the "rack" topology domain (there is only one such Pod, so the requirement
is satisfied trivially). The PodSet corresponding to the worker only
prefers the "rack" topology domain, so it may fallback to "block".

###### JobSet with ReplicatedJob instance topology

To address [Story 2](#story-2) we are introducing PodSet Slices, which are
subsets of the whole PodSet that are target to their own topology requirements.

To keep solution generic (not only for JobSet) and allow to split PodSet into
groups for other workload types, we decided to go with low-level API to allow
user to directly specify PodSet Slice size.

To achieve a result of each ReplicatedJob instance having its own topology
domain user should align the slice size to "completions".

**Example**:

```yaml
apiVersion: jobset.x-k8s.io/v1alpha2
kind: JobSet
metadata:
  name: tas-example-jobset
  labels:
    kueue.x-k8s.io/queue-name: user-queue
spec:
  replicatedJobs:
  - name: leader
    replicas: 1
    template:
      spec:
        completions: 1
        parallelism: 1
        template:
          metadata:
            annotations:
              kueue.x-k8s.io/podset-required-topology: cloud.provider.com/topology-block
          spec:
            containers:
            - name: leader
              image: registry.k8s.io/e2e-test-images/agnhost:2.53
              args: ["pause"]
  - name: workers
    replicas: 2
    template:
      spec:
        completions: 4
        parallelism: 4
        template:
          metadata:
            annotations:
              kueue.x-k8s.io/podset-slice-required-topology: cloud.provider.com/topology-host
              kueue.x-k8s.io/podset-slice-size: 4
          spec:
            containers:
            - name: worker
              image: registry.k8s.io/e2e-test-images/agnhost:2.53
              args: ["pause"]
```

In this example there will be 8 (2 ReplicatedJob instances with 4
"parallelism" each) worker pods and we say that we want to split
PodSet into slices with 4 pods each (so 2 slices in total) and
each slice requires "host" topology domain.

Since slice size is equal to `parallelism` for the Job, the end result
is that each Job will be placed within a "host". It is also possible
to omit the annotation `kueue.x-k8s.io/podset-slice-size`, as the default
value for it in case of JobSet is `parallelism`.

###### JobSet with two-level scheduling

According to [Story 5](#story-5) we noticed that some users would like to
co-locate all Jobs resulting from ReplicatedJob in JobSet in some
higher-level domain (like "block"), but also  co-locate each Job within
lower-level domain like "host".

To achieve that we are allowing to define both main and slice topologies.

**Example**:

```yaml
apiVersion: jobset.x-k8s.io/v1alpha2
kind: JobSet
metadata:
  name: tas-example-jobset
  labels:
    kueue.x-k8s.io/queue-name: user-queue
spec:
  replicatedJobs:
  - name: leader
    replicas: 1
    template:
      spec:
        completions: 1
        parallelism: 1
        template:
          metadata:
            annotations:
              kueue.x-k8s.io/podset-required-topology: cloud.provider.com/topology-block
          spec:
            containers:
            - name: leader
              image: registry.k8s.io/e2e-test-images/agnhost:2.53
              args: ["pause"]
  - name: workers
    replicas: 2
    template:
      spec:
        completions: 4
        parallelism: 4
        template:
          metadata:
            annotations:
              kueue.x-k8s.io/podset-preferred-topology: cloud.provider.com/topology-block
              kueue.x-k8s.io/podset-slice-required-topology: cloud.provider.com/topology-host
              kueue.x-k8s.io/podset-slice-size: 4
          spec:
            containers:
            - name: worker
              image: registry.k8s.io/e2e-test-images/agnhost:2.53
              args: ["pause"]
```

In this example there will be 8 worker pods and we say that the PodSet
corresponding to the worker requires the "block" topology domain for all
those pods.

However, we also specify that we want to split PodSet into slices with 4
pods each (so 2 slices in total) and each slice requires "host" topology
domain.

Since slice size is equal to `parallelism` for the Job, the end result
is that each Job will be placed within a "host". It is also possible
to omit the annotation `kueue.x-k8s.io/podset-slice-size`, as the default
value for it in case of JobSet is `parallelism`.

##### LeaderWorkerSet
###### Co-locate Leader with Workers

According to [Story 6](#story-6) we noticed that some users would like to
co-locate Leader with Workers within the same replica in LeaderWorkerSet.

To allow user to request topology for the group of pods consisting of leader
and workers we are adding a new `kueue.x-k8s.io/podset-group-name` annotation.
It specifies the name of the group for each PodSet, so if user specifies the
same group in two or more PodSets, Kueue will:
- find a common flavor for them
- find a common topology domain

Finding topology domain for PodSet Group comes with extra constraints that are
described in [Cross-PodSet Topology Aware scheduling](#cross-podset-topology-aware-scheduling)
section.

User has to also request one of `kueue.x-k8s.io/podset-required-topology` or
`kueue.x-k8s.io/podset-preferred-topology` to specify a requested topology for
the PodSet Group.

**Example**:

```yaml
apiVersion: leaderworkerset.x-k8s.io/v1
kind: LeaderWorkerSet
metadata:
  name: tas-example-lws
  labels:
    kueue.x-k8s.io/queue-name: user-queue
spec:
  replicas: 2
  leaderWorkerTemplate:
    leaderTemplate:
      metadata:
        annotations:
          kueue.x-k8s.io/podset-group-name: lws-group
          kueue.x-k8s.io/podset-required-topology: cloud.provider.com/topology-host
      spec:
        containers:
          - name: leader
            image: registry.k8s.io/e2e-test-images/agnhost:2.53
            args: ["pause"]
    size: 3
    workerTemplate:
      metadata:
        annotations:
          kueue.x-k8s.io/podset-group-name: lws-group
          kueue.x-k8s.io/podset-required-topology: cloud.provider.com/topology-host
      spec:
        containers:
          - name: leader
            image: registry.k8s.io/e2e-test-images/agnhost:2.53
            args: ["pause"]
```

In this example there are 2 replicas with 2 workers and 1 leader each.
Each group of leader and 2 workers should be placed in a rack.

##### LeaderWorkerSet
###### Respect Pod Index order within Workers
According to [Story 8](#story-8), even if the [Cross-PodSet Topology Aware scheduling](#cross-podset-topology-aware-scheduling) is __NOT__ enabled,
Worker Pods should be scheduled with Pod Index order consideration.

**Example**:

```yaml
apiVersion: leaderworkerset.x-k8s.io/v1
kind: LeaderWorkerSet
metadata:
  name: tas-example-lws
  labels:
    kueue.x-k8s.io/queue-name: user-queue
spec:
  replicas: 2
  leaderWorkerTemplate:
    leaderTemplate:
      spec:
        containers:
          - name: leader
            image: registry.k8s.io/e2e-test-images/agnhost:2.53
            args: ["pause"]
    size: 3
    workerTemplate:
      spec:
        containers:
          - name: leader
            image: registry.k8s.io/e2e-test-images/agnhost:2.53
            args: ["pause"]
```

When the above LeaderWorkerSet is submitted, Kueue LeaderWorkerSet integration webhook adds
`kueue.x-k8s.io/pod-index-offset: "1"` annotation to `.spec.leaderWorkerTemplate.workerTemplate.metadata.annotations`
because Worker pods will get 1-2 index annotation values in `leaderworkerset.sigs.k8s.io/worker-index`.

On the other hands, `kueue.x-k8s.io/pod-index-offset` annotation is not added and offset management is delegated to the PodSet Group mechanism
when `kueue.x-k8s.io/podset-group-name` is specified.

Additionally, if LeaderWorkerSet doesn't have a separate Leader template, the offset management is not added.

##### MPIJob with runLauncherAsWorker

According to [Story 7](#story-7) we noticed that Kueue should properly handle MPIJob Pods
indexes (`training.kubeflow.org/replica-index`) in case of MPIJob with runLauncherAsWorker mode.  
Because Worker replica indexes occasionally start from `1` when runLauncherAsWorker MPIJob has 
a separate replica spec for both roles (`Launcher` and `Worker`).

To allow all Pods to be scheduled considering Pod indexes,
we are adding a new `kueue.x-k8s.io/pod-index-offset` annotation.
It specifies the starting index for the replica.

**Example**:

```yaml
apiVersion: kubeflow.org/v2beta1
kind: MPIJob
metadata:
  name: pi
  labels:
    kueue.x-k8s.io/queue-name: user-queue
spec:
  slotsPerWorker: 1
  runLauncherAsWorker: true  
  mpiReplicaSpecs:
    Launcher:
      replicas: 1
      template:
        spec:
          containers:
          - image: mpioperator/mpi-pi:openmpi
            name: mpi-launcher
            securityContext:
              runAsUser: 1000
            command:
            - mpirun
            args:
            - -n
            - "2"
            - /home/mpiuser/pi
    Worker:
      replicas: 2
      template:
        spec:
          containers:
          - image: mpioperator/mpi-pi:openmpi
            name: mpi-worker
            securityContext:
              runAsUser: 1000
            command:
            - /usr/sbin/sshd
            args:
            - -De
            - -f
            - /home/mpiuser/.sshd_config
```

When the above MPIJob is submitted, Kueue MPIJob integration webhook adds
`kueue.x-k8s.io/pod-index-offset: "1"` annotation to `.spec.mpiReplicas["Worker"].metadata.annotations` 
because Worker pods will get 1-2 index annotation values in `training.kubeflow.org/replica-index`.

On the other hands, `kueue.x-k8s.io/pod-index-offset` annotation is not added and offset management is delegated to the PodSet Group mechanism
when `kueue.x-k8s.io/podset-group-name` is specified.

#### Support for the "auto" mode

We are considering introducing the "auto" mode where the user does not need to
specify the level by node label value, but the lowest level is selected
automatically.

One approach is to accept "auto" as a specific value for the "preferred" and
"required" annotations. However, we are going to defer this for Beta or GA based
on the users' feedback.

#### PodSetAssignment is per lowest-topology level

Assigning the lowest-topology level is to prevent the following race conditions.

For example, there is a topology with one block: "block1" and with three racks:
"rack1", "rack2", "rack3". Now, a user sends "workload1" which requiring "block"
topology, and it fits within two racks. Let's assume Kueue only assigned
nodeSelector to match for "block1" on admission. Then, kube-scheduler binds the
Pods to "rack1" and "rack2". In the meanwhile, there is "workload2" admitted,
requiring "rack". It gets assigned to "rack1", but cannot be scheduled since the
nodes of "rack1" are already in use.

#### Provisioning request and required mode

When using [ProvisioningRequest with TAS](#support-for-provisioningrequests)
the newly provisioned nodes may or may not respect the topology requested in the
TAS "required" annotations.

For example, a user may request Kueue to schedule Pods on a single rack with the
"kueue.x-k8s.io/podset-required-topology: rack" annotation, but the cloud
provider may or may not have support for such "compact placement" provisioning
of nodes. If the newly created nodes are scattered across racks, then TAS will
fail to schedule the workload.

However, the workload will not get stuck forever. After a while (10min by default)
the BookingExpired condition is added by ClusterAutoscaler, which in turn will
result in releasing quota for the workload and retrying. After a couple of
retries the workload will get deactivated.

#### kueue.x-k8s.io/tas Pod label
We initially introduced `kueue.x-k8s.io/tas` label to Pod level label 
to identify if created Pod is scheduled via TopologyAwareScheduling.

But, we find better way (informer cache pattern <a.k.a. indexer>) to do the same thing.
So, we stop adding `kueue.x-k8s.io/tas` label to Pod Template in Kueue v0.14.0, and then
we remove the label evaluation mechanism in Kueue v0.17.0.

### Risks and Mitigations

#### Non-exclusive use of nodes

The TAS feature assumes exclusive use of the nodes, that is all pods running on
the nodes come from workloads assigned to the same TAS Resource Flavor. This can
be achieved by admins by using a distinct label corresponding to a node group
meant for TAS.

In order to reduce the risk of such misconfiguration we require every TAS
Resource Flavor to have at least one label.

#### Node topology changes

A node can be removed during runtime of a workload, and the replacement for the
pod running on this node may not be able to find a replacement matching the
TopologyAssignment. In that case the workload may not be able to progress.

First, note that the issue also exists for regular workloads, but for TAS
workloads, given the additional constraints, it might be harder to find a
replacement node.

In order to mitigate this risk we propose to extend the waitForPodsReady
mechanism with a new timeout, called replacement timeout, which defines the
timeout for all pods to be ready again. The time is computed since the last
transition to the `PodsReady=false`, more details in the
[KEP PR](https://github.com/kubernetes-sigs/kueue/pull/2737). This mechanism
will also be helpful for regular workloads.

We also propose to recompute the TopologyAdmission upon node removal and/or
failure, to find matching replacements for missing nodes. If no such
replacement exists, the workload has to be evicted and rescheduled again.
This mechanism requires kueue to keep track of any failed or missing nodes
affecting the scheduled TAS workloads. We propose to initially handle only
a single node failure and extend it to multiple depending on the feedback
from the users.
#### Race condition when accounting for DaemonSet pods

There is a risk that workloads are scheduled before the DaemonSet pods are
accounted by TAS.

We consider this risk as not that relevant for alpha as the TAS workloads are
expected to mostly consume accelerators (like TPUs and GPUs), so they are not
expected to compete for resources with DaemonSet pods.

One way to mitigate the risk is to implement tracking for DaemonSets
(see [Account usage by watching DaemonSet pods](#account-usage-by-watching-daemonset-pods)).
We will re-evaluate the need for Beta or GA based on the users' feedback.

## Design Details

### Hierarchy representation

We propose the model for representing the hierarchy of nodes within a data center
by using node labels. We assume the node labels are set up by a cloud provider,
or set up manually by administrators of on-premise clusters.

Additionally, we assume that every node used for TAS has a set of the labels
which identifies uniquely its location in the tree structure. We do not assume
global uniqueness of labels on each level, i.e. there could be two nodes with
the same "rack" label, but in different "blocks".

For example, this is a representation of the dataset hierarchy;

|  node  | cloud.provider.com/topology-block | cloud.provider.com/topology-rack |
| :----: | :-------------------------------: | :------------------------------: |
| node-1 |              block-1              |              rack-1              |
| node-2 |              block-1              |              rack-2              |
| node-3 |              block-2              |              rack-1              |
| node-4 |              block-2              |              rack-3              |

Note that, there is a pair of nodes, node-1 and node-3, with the same value of
the "cloud.provider.com/topology-rack" label, but in different blocks.

### Admin-facing API

```golang
// ResourceFlavorSpec defines the desired state of the ResourceFlavor
type ResourceFlavorSpec struct {
    ...

  // topologyName indicates topology for the TAS ResourceFlavor.
  // When specified, it enables scraping of the topology information from the
  // nodes matching to the Resource Flavor node labels.
  //
  // +optional
  TopologyName *string `json:"topologyName,omitempty"`
}

// TopologySpec defines the desired state of Topology
type TopologySpec struct {
  // levels define the levels of topology.
  //
  // +required
  // +listType=atomic
  // +kubebuilder:validation:MinItems=1
  // +kubebuilder:validation:MaxItems=8
  Levels []TopologyLevel `json:"levels,omitempty"`
}

// TopologyLevel defines the desired state of TopologyLevel
type TopologyLevel struct {
  // nodeLabel indicates the name of the node label for a specific topology
  // level. Examples:
  // - cloud.provider.com/topology-block
  // - cloud.provider.com/topology-rack
  //
  // +required
  // +kubebuilder:validation:Required
  // +kubebuilder:validation:MinLength=1
  // +kubebuilder:validation:MaxLength=316
  // +kubebuilder:validation:Pattern=`^([a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*/)?(([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9])$`
  NodeLabel string `json:"nodeLabel,omitempty"`
}
```

Example TAS Resource Flavor & Topology configuration:

```yaml
kind: ResourceFlavor
metadata:
  name: "tas-flavor"
spec:
  nodeLabels:
    "cloud.provider.com/node-group: tas"
  topologyName: default
---
kind: Topology
metadata:
  name: "default"
spec:
  levels:
  - nodeLabel: cloud.provider.com/topology-block
  - nodeLabel: cloud.provider.com/topology-rack
```

### User-facing API

The user will need to point the workload to the ClusterQueue with the TAS
ResourceFlavor. Additionally, the user specifies the annotations at the
PodTemplate level:

```golang
const (

  // PodSetRequiredTopologyAnnotation indicates that a PodSet requires
  // Topology Aware Scheduling, and requires scheduling all pods on nodes
  // within the same topology domain corresponding to the topology level
  // indicated by the annotation value (e.g. within a rack or within a block).
  PodSetRequiredTopologyAnnotation = "kueue.x-k8s.io/podset-required-topology"

  // PodSetPreferredTopologyAnnotation indicates that a PodSet requires
  // Topology Aware Scheduling, but scheduling all pods within pods on nodes
  // within the same topology domain is a preference rather than requirement.
  //
  // The levels are evaluated one-by-one going up from the level indicated by
  // the annotation. If the PodSet cannot fit within a given topology domain
  // then the next topology level up is considered. If the PodSet cannot fit
  // at the highest topology level, then it gets admitted as distributed
  // among multiple topology domains.
  PodSetPreferredTopologyAnnotation = "kueue.x-k8s.io/podset-preferred-topology"

  // PodSetUnconstrainedTopologyAnnotation indicates that a PodSet does not have any topology requirements.
  // Kueue admits the PodSet if there's enough free capacity available.
  // Recommended for PodSets that don't need low-latency or high-throughput pod-to-pod communication,
  // but want to leverage TAS capabilities improve accuracy of admitting jobs
  //
  // +kubebuilder:validation:Type=boolean
  PodSetUnconstrainedTopologyAnnotation = "kueue.x-k8s.io/podset-unconstrained-topology"

  // PodSetSliceRequiredTopologyAnnotation indicates that a PodSet requires
  // Topology Aware Scheduling, and requires scheduling each PodSet slice on nodes
  // within the topology domain corresponding to the topology level
  // indicated by the annotation value (e.g. within a rack or within a block).
  PodSetSliceRequiredTopologyAnnotation = "kueue.x-k8s.io/podset-slice-required-topology"

  // PodSetSliceSizeAnnotation describes the requested size of a podset slice
  // for which Kueue finds a requested topology domain
  //
  // This annotation is required if `kueue.x-k8s.io/podset-slice-required-topology`
  // is defined
  PodSetSliceSizeAnnotation = "kueue.x-k8s.io/podset-slice-size"

  // PodSetSliceRequiredTopologyConstraints defines a JSON-style, multi-layer topology constraints
  // in the format of:
  // kueue.x-k8s.io/podset-slice-required-topology-constraints: |
  // [
  //   {"topology": "cloud.provider.com/aizone", "size": "64"},
  //   {"topology": "cloud.provider.com/block", "size": "32"},
  //   {"topology": "cloud.provider.com/rack", "size": "16"}
  // ]
  // This is an extension of existing PodSetSliceRequiredTopologyAnnotation to support multi-layer topology constraints.
  PodSetSliceRequiredTopologyConstraints = "kueue.x-k8s.io/podset-slice-required-topology-constraints"
)
```

### Validation

The validations at the creation time:
- the ResourceFlavor defining `TopologyAwareScheduling` needs to have at least
  one node label

We introduce the following validations (post-creation time, workload violating
the rules is deactivated):
- the value of `kueue.x-k8s.io/podset-required-topology` is one of the labels
  specified in the topology node labels
- the value of `kueue.x-k8s.io/podset-preferred-topology` is one of the labels
  specified in the topology node labels
- if "podset-required-topology" or "podset-preferred-topology" is specified for
  one PodTemplate, then one of them is specified for every PodTemplate in the
  Job spec.
- the annotations `kueue.x-k8s.io/podset-required-topology`,
  `kueue.x-k8s.io/podset-preferred-topology`, and `kueue.x-k8s.io/podset-unconstrained-topology`
  are mutually exclusive.
- the value of `kueue.x-k8s.io/podset-slice-required-topology` is one of the labels
  specified in the topology node labels
- the value of `kueue.x-k8s.io/podset-slice-required-topology` has to represent
  a topology "below" the topology defined by `kueue.x-k8s.io/podset-preferred-topology`
  or `kueue.x-k8s.io/podset-required-topology`
- if `kueue.x-k8s.io/podset-slice-required-topology` is specified then
  `kueue.x-k8s.io/podset-slice-size` is also required (unless the Workload type
  specified its own default. See [Slice size validation](#slice-size-validation))
- the value of `kueue.x-k8s.io/podset-slice-size` has to be a numeric value greater or equal
  than 1. It has to evenly divide the size of a PodSet.
- multi-layer topology constraints (`kueue.x-k8s.io/podset-slice-required-topology-constraints`):
  - it has to be exclusively used with `kueue.x-k8s.io/podset-slice-required-topology` and `kueue.x-k8s.io/podset-slice-size`
  - it must be ordered from coarsest to finest
  - each layer's size must be evenly divisible by the size of the layer immediately below it
  - the number of layers must be less than or equal to the number of levels in the topology
- if `kueue.x-k8s.io/podset-group-name` is specified, the `kueue.x-k8s.io/podset-required-topology`
  or `kueue.x-k8s.io/podset-preferred-topology` has to also be specified in all other
  PodTemplates included in the PodSet Group and it has to have the same value.

#### PodSet Slice size validation

By default if `kueue.x-k8s.io/podset-slice-required-topology` is specified then
`kueue.x-k8s.io/podset-slice-size` is also required. Otherwise an error is returned.
This decision has been made, because for most of the Workload types there is no
sensible default to fallback to.

However, in case of the JobSet we expect that the most frequent use-case will be to
define PodSet Slice as a single Job, thus if `kueue.x-k8s.io/podset-slice-size`
is not defined for JobSet it defaults to `parallelism`.

For `kueue.x-k8s.io/podset-slice-required-topology-constraints`, each entry in the
JSON array must specify both `topology` and `size`. No defaulting logic is applied
here even for JobSet.

### Internal APIs

We extend the `Workload` structure to reflect the topology request at the
Job level.

```golang
type PodSet struct {
  ...
  // topologyRequest defines the topology request for the PodSet.
  //
  // +optional
  TopologyRequest *PodSetTopologyRequest `json:"topologyRequest,omitempty"`
}

type PodSetTopologyRequest struct {
  // required indicates the topology level required by the PodSet, as
  // indicated by the `kueue.x-k8s.io/podset-required-topology` PodSet
  // annotation.
  //
  // +optional
  Required *string `json:"required,omitempty"`

  // preferred indicates the topology level preferred by the PodSet, as
  // indicated by the `kueue.x-k8s.io/podset-preferred-topology` PodSet
  // annotation.
  //
  // +optional
  Preferred *string `json:"preferred,omitempty"`

  // unconstrained indicates that Kueue has the freedom to schedule the PodSet within
  // the entire available capacity, without constraints on the compactness of the placement.
  // This is indicated by the `kueue.x-k8s.io/podset-unconstrained-topology` PodSet annotation.
  //
  // +optional
  // +kubebuilder:validation:Type=boolean
  Unconstrained *bool `json:"unconstrained,omitempty"`

  // podIndexLabel indicates the name of the label indexing the pods.
  // For example, in the context of
  // - kubernetes job this is: kubernetes.io/job-completion-index
  // - JobSet: kubernetes.io/job-completion-index (inherited from Job)
  // - Kubeflow: training.kubeflow.org/replica-index
  PodIndexLabel *string `json:"podIndexLabel,omitempty"`

  // subGroupIndexLabel indicates the name of the label indexing the instances of replicated Jobs (groups)
  // within a PodSet. For example, in the context of JobSet this is jobset.sigs.k8s.io/job-index.
  SubGroupIndexLabel *string `json:"subGroupIndexLabel,omitempty"`

  // subGroupCount indicates the count of replicated Jobs (groups) within a PodSet.
  // For example, in the context of JobSet this value is read from jobset.sigs.k8s.io/replicatedjob-replicas.
  SubGroupCount *int32 `json:"subGroupCount,omitempty"`

  // podSetGroupName indicates the name of the group of PodSets to which this PodSet belongs to.
  // PodSets with the same `PodSetGroupName` should be assigned the same ResourceFlavor
  //
  // +optional
  PodSetGroupName *string `json:"podSetGroupName,omitempty"`

  // podSetSliceRequiredTopology indicates the topology level required by the PodSet slice, as
  // indicated by the `kueue.x-k8s.io/podset-slice-required-topology` annotation.
  //
  // +optional
  PodSetSliceRequiredTopology *string `json:"podSetSliceRequiredTopology,omitempty"`

  // podSetSliceSize indicates the size of a subgroup of pods in a PodSet for which
  // Kueue finds a requested topology domain on a level defined
  // in `kueue.x-k8s.io/podset-slice-required-topology` annotation.
  //
  // +optional
  PodSetSliceSize *int32 `json:"podSetSliceSize,omitempty"`

  // podsetSliceRequiredTopologyConstraints holds the parsed content of the
  // `kueue.x-k8s.io/podset-slice-required-topology-constraints` annotation.
  // It defines multi-layer topology constraints as a flat list ordered from
  // coarsest to finest. Each entry specifies a topology level and the
  // required group size at that level.
  //
  // This field is mutually exclusive with podSetSliceRequiredTopology /
  // podSetSliceSize, which are used for two-level scheduling.
  //
  // +optional
  // +listType=atomic
  // +kubebuilder:validation:MaxItems=3
  PodsetSliceRequiredTopologyConstraints []PodsetSliceRequiredTopologyConstraint `json:"podsetSliceRequiredTopologyConstraints,omitempty"`
}

// PodsetSliceRequiredTopologyConstraint defines a single layer in a
// multi-layer topology constraint.
type PodsetSliceRequiredTopologyConstraint struct {
  // topology indicates the topology level required for this constraint layer.
  //
  // +required
  // +kubebuilder:validation:MinLength=1
  // +kubebuilder:validation:MaxLength=63
  Topology string `json:"topology"`

  // size indicates the number of pods in each group at this constraint layer.
  //
  // +required
  // +kubebuilder:validation:Minimum=1
  Size int32 `json:"size"`
}
```

We extend the `PodSetAssignment` structure to keep track of the number of pods
at each topology level to the specific subset of nodes.

```golang
type PodSetAssignment struct {
  ...

  // +optional
  TopologyAssignment *TopologyAssignment `json:"topologyAssignment,omitempty"`
```

The format of `TopologyAssignment` depends on the API version; see details [below](#topology-assignment-representation).

Kueue uses the `kueue.x-k8s.io/topology` scheduling gate to delay the
`nodeSelector` assignment, because different pods in the same PodSet may have
different values:

```golang
const (
  // TopologySchedulingGate is used to delay scheduling of a Pod until the
  // nodeSelectors corresponding to the assigned topology domain are injected
  // into the Pod.
  TopologySchedulingGate = "kueue.x-k8s.io/topology"

  // WorkloadAnnotation is an annotation set on the Job's PodTemplate to
  // indicate the name of the admitted Workload corresponding to the Job. The
  // annotation is set when starting the Job, and removed on stopping the Job.
  WorkloadAnnotation = "kueue.x-k8s.io/workload"

  // TASLabel is a label set on the Job's PodTemplate to indicate that the
  // PodSet is admitted using TopologyAwareScheduling, and all Pods created
  // from the Job's PodTemplate also have the label.
  TASLabel = "kueue.x-k8s.io/tas"
)
```

#### Topology assignment representation

`TopologyAssignment` indicates the topology assignment divided into topology domains corresponding to the lowest level of the topology. The assignment specifies the number of Pods to be scheduled per topology domain and the node selectors for each topology domain, in the following way:

- the node selector keys are specified by the `Levels` field (same for all domains),
- the corresponding node selector values - and the Pod counts - are specified by the other field (`Domains` until v1beta1 or `Slices` since v1beta2).

If the `Levels` field contains `kubernetes.io/hostname` label, the `TopologyAssignment` will contain data only for this label, and omit higher levels in the topology.

##### Until v1beta1

In the older format, each domain assignment is represented by a single entry in the `Domains` list. 

```golang
type TopologyAssignment struct {
  // levels is an ordered list of keys denoting the levels of the assigned
  // topology (i.e. node label keys), from the highest to the lowest level of
  // the topology.
  //
  // +required
  // +listType=atomic
  // +kubebuilder:validation:MinItems=1
  // +kubebuilder:validation:MaxItems=8
  Levels []string `json:"levels"`

  // domains is a list of topology assignments split by topology domains at
  // the lowest level of the topology.
  //
  // +required
  Domains []TopologyDomainAssignment `json:"domains"`
}

type TopologyDomainAssignment struct {
  // values is an ordered list of node selector values describing a topology
  // domain. The values correspond to the consecutive topology levels, from
  // the highest to the lowest.
  //
  // +required
  // +listType=atomic
  // +kubebuilder:validation:MinItems=1
  // +kubebuilder:validation:MaxItems=8
  Values []string `json:"values"`

  // count indicates the number of Pods to be scheduled in the topology
  // domain indicated by the values field.
  //
  // +required
  // +kubebuilder:validation:Minimum=1
  Count int32 `json:"count"`
}
```

Example:

```yaml
topologyAssignment:
  levels:
  - cloud.provider.com/topology-block
  - cloud.provider.com/topology-rack
  domains:
  - values: [block-1, rack-1]
    count: 4
  - values: [block-1, rack-2]
    count: 2
```

Here:
- 4 Pods are to be scheduled on nodes matching the node selector:
  ```
  cloud.provider.com/topology-block: block-1
  cloud.provider.com/topology-rack: rack-1
  ```
- 2 Pods are to be scheduled on nodes matching the node selector:
  ```
  cloud.provider.com/topology-block: block-1
  cloud.provider.com/topology-rack: rack-2
  ```

Below there is an equivalent of the above example, assuming that the Topology
object defines `kubernetes.io/hostname` as the lowest level in topology.
Hence we omit higher topology levels, since the hostname label
is sufficient to uniquely identify a particular node.

```yaml
topologyAssignment:
  levels:
  - kubernetes.io/hostname
  domains:
  - values: [hostname-1]
    count: 4
  - values: [hostname-2]
    count: 2
```

##### Since v1beta2

In v1beta2, the data format is reshaped, with the main goal of improving handling of huge workloads. The 3 main ideas here are:

- splitting the whole assignment structure into slices,
- broader use of "parallel lists" which should be "zipped together" to produce their meaning \
  (just like a _single_ list `Levels` in the old format specified the topology levels used in _every_ domain of the assignment),
- extracting common prefixes and suffixes of node names.

```golang
type TopologyAssignment struct {
  // (same role & comments as in v1beta1)
  Levels []string `json:"levels,omitempty"`

  // slices represent topology assignments for subsets of pods of a workload.
  // The full assignment is obtained as a union of all slices.
  // +required
  // +listType=atomic
  // +kubebuilder:validation:MaxItems=1000
  Slices []TopologyAssignmentSlice `json:"slices,omitempty"`
}

// TopologyAssignmentSlice fully specifies the topology assignment for a subset of pods of a workload.
type TopologyAssignmentSlice struct {
  // domainCount is the number of domains covered by this slice.
  // +required
  // +kubebuilder:validation:Minimum=1
  DomainCount int32 `json:"domainCount,omitempty"`

  // valuesPerLevel has one entry for each of the Levels specified in the TopologyAssignment.
  // The entry corresponding to a particular level specifies the placement of pods at that level.
  // +required
  // +listType=atomic
  // +kubebuilder:validation:MinItems=1
  // +kubebuilder:validation:MaxItems=16
  ValuesPerLevel []TopologyAssignmentSliceLevelValues `json:"valuesPerLevel,omitempty"`

  // podCounts specifies the number of pods allocated per each domain.
  // +required
  PodCounts TopologyAssignmentSlicePodCounts `json:"podCounts,omitempty"`
}

type TopologyAssignmentSliceLevelValues struct {
  // universal, if set, specifies a single topology placement value (at a particular topology level)
  // that applies to all pods in the current TopologyAssignmentSlice.
  // Exactly one of universal, individual must be set.
  // +optional
  Universal *string `json:"universal,omitempty"`

  // individual, if set, specifies multiple topology placement values (at a particular topology level)
  // that apply to the pods in the current TopologyAssignmentSlice.
  // Exactly one of universal, individual must be set.
  // +optional
  Individual *TopologyAssignmentSliceLevelIndividualValues `json:"individual,omitempty"`
}

type TopologyAssignmentSliceLevelIndividualValues struct {
  // prefix specifies a common prefix for all values in this slice assignment.
  // It must be either nil pointer or a non-empty string.
  // +optional
  // +kubebuilder:validation:MaxLength=63
  prefix *string `json:"prefix,omitempty"`
  // suffix specifies a common suffix for all values in this slice assignment.
  // It must be either nil pointer or a non-empty string.
  // +optional
  // +kubebuilder:validation:MaxLength=63
  suffix *string `json:"suffix,omitempty"`

  // roots specifies the values in this assignment (excluding prefix and suffix, if non-empty).
  // Its length must be equal to the "domainCount" field of the TopologyAssignmentSlice.
  // +required
  // +listType=atomic
  // +kubebuilder:validation:MinItems=1
  // +kubebuilder:validation:MaxItems=100000
  // +kubebuilder:validation:items:MaxLength=63
  Roots []string `json:"roots,omitempty"`
}

type TopologyAssignmentSlicePodCounts struct {
  // universal, if set, specifies the number of pods allocated in every domain in this slice.
  // Exactly one of universal, individual must be set.
  // +optional
  // +kubebuilder:validation:Minimum=1
  Universal *int32 `json:"universal,omitempty"`

  // individual, if set, specifies the number of pods allocated in each domain in this slice.
  // If set, its length must be equal to the "domainCount" field of the TopologyAssignmentSlice.
  // Exactly one of universal, individual must be set.
  // +optional
  // +listType=atomic
  // +kubebuilder:validation:MinItems=1
  // +kubebuilder:validation:MaxItems=100000
  // +kubebuilder:validation:items:Minimum=1
  Individual []int32 `json:"podCounts,omitempty"`
}
```

The above mentioned cross-validity rules (mutually exclusive fields, equal lengths, etc.) will be all expressed as `kubebuilder:validation:XValidation` rules on the respective "smallest common container types".

Example (representing the same assignment as in the first example for the [old format](#until-v1beta1)):

```yaml
topologyAssignment:
  levels:
  - cloud.provider.com/topology-block
  - cloud.provider.com/topology-rack
  slices:
  - domainCount: 2
    valuesPerLevel:
    - universal: block-1
    - individual:
        prefix: rack-
        roots: [1, 2]
    podCounts:
      individual: [4, 2]
```

The above example has one slice, specifying 2 domains on 2 topology levels. On the block level, all domains take the same value (`block-1`), which allows using `universal`. On the rack level, the values diverge (`rack-1`) vs. (`rack-2`) but they still share a relatively long common prefix. The new representation allows deduplicating characters between these.

Multiple slices may be used e.g. for a host-level assignment when the nodes being assigned are split into multiple "node pools". The following example illustrates this for an assignment of 12 pods on 12 nodes (1 per 1), where the nodes come from 2 pools (of size 5 and 7 respectively), with node names looking like `pool-X-node-Y`:

```yaml
topologyAssignment:
  levels: [kubernetes.io/hostname]
  slices:
  - domainCount: 5
    valuesPerLevel:
    - individual:
        prefix: pool-1-node-
        roots: [1, 2, 3, 4, 5]
    podCounts:
      universal: 1
  - domainCount: 7
    valuesPerLevel:
    - individual:
        prefix: pool-2-node-
        roots: [1, 2, 3, 4, 5, 6, 7]
    podCounts:
      universal: 1
```

The main motivation behind the new format is the etcd size limit of 1.5MiB per single resource, which currently restricts the number of nodes that can participate in the assignment for a single workload. (For the v1beta1 format, and the real-life node naming schemes of main K8s vendors, the limit is around 20-30k). The new format helps addressing this problem in 2 time perspectives:

- In the short term, it increases the number of nodes which can fit into 1 etcd entry.

  - By just using a single slice, with extracting common prefix and suffix of all node names, our simulations (for some real-life node naming schemes) suggested a limit of around 60k nodes.
  
  - Multiple slices allow optimizing even further, if desired. \
    Our simulations of more complex algorithms (e.g. heuristic pruning of prefix tree) allowed fitting over 100k nodes. \
    (However, at that point we reached a tradeoff between bytesize, encoding time, and conceptual simplicity. Resolving that tradeoff is out of scope of this design; the important thing is that the proposed data format supports various specific algorithms).

- In the long term, as the number of nodes grows, at some point we'll inevitably hit the 1.5MiB limit anyway. \
  When this happens, we foresee a need to store the slices as separate CRD instances (see [description](#topologyassignmentslices-as-separate-crd-instances) in the "Alternatives" section). \
  While the v1beta2 format does not yet do that, by introducing `Slices` we come much closer to this. \
  Once there is a need, we can promote (some of) `Slices` to instances of a standalone CRD - but the appropriate type system is already there.

#### Node failures

Initially we plan to support node becoming not ready (as indicated in Node
`status.conditions.ready` field) and node being deleted. A new controller will
monitor nodes and will update each affected TAS workload with the information
about the failed nodes. This information will then be consumed by a new mechanism
in scheduler where we will try to find a new topology assignment and replace the
failed node(s) (by changing the assignment only on the affected pods). Initially we plan
to only replace in the case of a single node failure and if no preemption/reclamation
is neccessary to fit the workload. Since this mechanism is dedicated
to only replace nodes, it will only work for Topologies which specify
`kubernetes.io/hostname` at the lowest level.

##### Until v0.13
We use an Annotation at a Workload level:

```golang
const (
	// NodeToReplaceAnnotation is an annotation on a Workload. It holds a
	// name of a failed node running at least one pod of this workload.
	NodeToReplaceAnnotation = "alpha.kueue.x-k8s.io/node-to-replace"
)
```
The annotation contains the name of (a single) failed node to replace.
##### Since v0.14
We propose to remove the Annotation and replace it with new field in Workload status that will it will contain
a list of failed nodes:

```golang
type WorkloadStatus struct {
  (...)
    // unhealthyNodes holds the failed nodes running at least one pod of this workload
    // when Topology-Aware Scheduling is used. This field should not be set by the users.
    // It indicates Kueue's scheduler is searching for replacements of the failed nodes.
    // Requires enabling the TASFailedNodeReplacement feature gate.
    //
    // +optional
    UnhealthyNodes []UnhealthyNode `json:"unhealthyNodes,omitempty"`
}

type UnhealthyNode struct {
    // name is the name of the unhealthy node.
    //
    // +required
    // +kubebuilder:validation:Required
    Name string `json:"name"`
}

```

Sometimes node failures are only transient and the node might recover, and so reacting
immediately to NotReady condition could result in unnecessary node replacements.

For that reason we introduce two heuristics for marking nodes to replace for a given workload:
1. when the node is NotReady for over 30s (used by default)
2. when the node is NotReady and all of the workload's Pods scheduled on that node are terminated
or terminating (used when the `TASReplaceNodeOnPodTermination` feature gate is enabled which is
available starting with Kueue v0.13)

For the future releases we are going to consider API configuration for the approach, but first we
would like to collect more user feedback using the feature gates while in Alpha.

Kueue tries to find a replacement for a failed node until success (or until it gets
evicted by e.g. `waitForPodsReady.recoveryTimeout`). One can limit the number of retries
to only one, by setting the `TASFailedNodeReplacementFailFast` feature gate to `true`.

#### Tainted nodes treatment

We introduce the `TASReplaceNodeOnNodeTaints` feature gate from v0.17 as Beta, and backport to v0.15.5 and v0.16.2 as Alpha.
When enabled, Kueue treats tainted nodes as unhealthy. This applies to nodes with `NoExecute` taint,
or nodes with `NoSchedule` taint where all pods of the workload running on that node are failing, terminating, or in unscheduled state.

- **NoExecute**: Nodes with the `NoExecute` effect taint, that is not tolerated by the workload, are considered unhealthy.
The pods on such nodes are expected to be terminated by the node controller. Once terminated, Kueue will attempt
to replace the node if `TASFailedNodeReplacement` is enabled, and evict the workload if no replacement is possible.
If `tolerationSeconds` is specified, Kueue waits for the duration before treating the node as unhealthy.
- **NoSchedule**: Nodes with the `NoSchedule` effect taint, that is not tolerated by the workload, are considered unhealthy
only if all pods of the workload that have topology assignment to that node are terminating, in the failed state,
or if they are unscheduled. In this case, Kueue can trigger node replacement.

  While `NoSchedule` taint does not evict running pods (unlike `NoExecute`), Kueue triggers recovery for unscheduled or failed pods.
  If a workload is assigned to a node that is tainted with `NoSchedule`, the pods will remain in a pending state because
  the Kubernetes scheduler will not schedule them on that node.

  Kueue also performs node replacement if pods fail or terminate after `NoSchedule` is assigned. Since the taint may indicate
  an underlying node problem, replacement ensures the workload is not blocked by the unavailable node.

For workloads for which a single Node replacement is possible, and the pods bound to the node are unscheduled (no `spec.nodeName` set),
because they cannot run due to a taint, Kueue marks the pods as `Failed` and adds the following condition to the pods:
  ```yaml
  type: TerminatedByKueue
  status: True
  reason: UnschedulableOnAssignedNode
  message: "..."
  ```
  This ensures that the pods are re-created by the Job controller for placement on other nodes, while keeping the original
  pods in Failed state for debuggability. Without this step, the pending pods would block the creation of replacement pods.

  In addition, Kueue emits a Normal event with reason `PodTerminated` on the Pod to inform about the termination.

Nodes with `.spec.unschedulable` set to true are treated as having `NoSchedule` taint.

##### User stories

###### NoExecute

As a cluster administrator, I want to be able to safely drain a node (using `kubectl drain` which applies a `NoExecute` taint)
that is running TAS workloads. Kueue should detect this taint, consider the node as unhealthy, and trigger the replacement
of the affected pods on other suitable nodes, or evict the workload if no replacement is possible, maintaining the improved availability provided by TAS.

###### NoSchedule

As a cluster administrator, I want Kueue to proactively handle situations where a node assigned to an admitted workload
becomes unschedulable (marked with `NoSchedule` taint, e.g., due to an underlying infrastructure issue).
If the pods cannot be scheduled on the assigned node due to this taint, or they are failing/terminating, Kueue should
recognize the node as unhealthy for this workload and attempt to find a replacement node, or evict the workload if no replacement
is possible, preventing the pods from getting stuck in a pending or failing state indefinitely.

### Implicit defaulting of TAS annotations

Requiring to set the TAS annotations (see [User facing API](#user-facing-api))
on every PodSet creates a friction for adoption of TAS, and a point of failure.

In order to reduce friction we assume that TAS is used for scheduling workloads
targetting CQs with only TAS Resource Flavors (RFs with the `spec.topologyName`
field specified).

A PodSet scheduled with TAS without the explicit annotation is handled as
if it had the `podset-unconstrained-topology: true` annotation pointing to the lowest
topology level defined in the Topology referenced by the selected TAS flavor.
We call it "implicit default" as the annotation isn't persisted.

### Computing the assignment

The extended PodSet assignment is set on admitting the workload. In order to
compute the assignment Kueue caches the information about the node allocatable
capacity and the current usage by other workloads in this resource flavor.

The information about the allocatable capacity is scraped from nodes based on
the `status.allocatable` field.

The cached information about the used resources is updated whenever a workload
is admitted, suspended, resumed or finished. Additionally, we scrape the usage
information from all non-TAS pods bound to the TAS nodes - this is done to
account for Pods not created by Workloads not managed by Kueue (such as
DaemonSet pods).

The available capacity for TAS nodes is then computed as a difference between
the allocatable space and current usage.

For a given PodSet Kueue:
- when the `podset-required-topology` is used, then Kueue tries to find any value of the
 level label which can accommodate all the pods or slice. If there is no such value, then
 the workload keeps waiting in the queue.
- when the `podset-preferred-topology` is used, then Kueue tries to find a level
  at which the PodSet fully fits within a topology domain corresponding to the
  level. Kueue starts the search from the specified level, but if the PodSet
  does not fit, then it tries higher levels in the hierarchy.

Kueue places pods on domains with different algorithms, depending on the annotation and chosen profile:
- `LeastFreeCapacity` algorithm - Kueue selects as many domains as needed (if it meets user's requirement) starting from the one with the least free capacity;
- `BestFit` algorithm - Kueue selects as many domains as needed (if it meets user's requirement) starting from the one with the most free capacity.
However, it optimizes the selection of the last domain at each level to minimize the remaining free resources.
- `BalancedPlacement` algorithm - Kueue selects as many domains as needed (if it meets user's requirement)
and places pods evenly on the selected domains. The balanced placement is performed only at two consecutive
levels, where the higher of these two levels is indicated by the `preferred` annotation
(for more details see [Balanced placement](#balanced-placement)).

#### Example
Consider a rack with four nodes that can accommodate 3, 3, 2, and 1 pod, respectively. A PodSet consists of 7 pods.

BestFit algorithm iterates over the nodes and select the first two nodes,
each with 3 available pods, as they possess the most free capacity. With 1 pod remaining to schedule,
algorithm optimizes the choice of the last node (domain) and selects the node that can accommodate exactly 1 pod.

The `LeastFreeCapacity` algorithm iterates over the nodes in reverse order.
Consequently, it selects the nodes with 1, 2, 3, and 3 available pods, reserving capacity for only 1 pod on the last node.

#### Selecting the algorithm
Selection of the algorithm depends on TAS profiles expressed by feature gates, and PodSet's annotation. 

The `TASProfile...` feature gates are mutually exclusive.

Eventually, we plan to introduce TAS configuration allowing the user to select the desired algorithm, and then to remove the feature gates.

##### Until v0.14
Until v0.14, the available feature gates are as follows:

| feature gate / annotation                | preferred         | required          | unconstrained     |
| ---------------------------------------- | ----------------- | ----------------- | ----------------- |
| None                                     | BestFit           | BestFit           | BestFit           |
| TASProfileMixed (deprecated)             | BestFit           | BestFit           | LeastFreeCapacity |

These reflect our general recommendation of `BestFit` for the topology-aware cases; however, `LeastFreeCapacity` remains an available option, especially for the `unconstrained` topology requests.

##### Since v0.15
Since v0.15, the available feature gates are as follows:

| feature gate / annotation                  | preferred         | required          | unconstrained     |
| ------------------------------------------       | ----------------- | ----------------- | ----------------- |
| None <br/> or TASProfileMixed (default)          | BestFit           | BestFit           | LeastFreeCapacity |
| TASProfileBestFit (deprecated)                   | BestFit           | BestFit           | BestFit           |
| TASProfileLeastFreeCapacity (removed in v0.17)   | LeastFreeCapacity | LeastFreeCapacity | LeastFreeCapacity |
| TASBalancedPlacement                             | BalancedPlacement | BestFit           | LeastFreeCapacity |

Based on the user feedback, we decided to make `TASProfileMixed` default starting in v0.15. (The corresponding feature gate is hence obsolete; we formally keep it "deprecated" for backwards compatibility). It differs from the previous default only in the `unconstrained` case - in which Kueue should prioritize minimizing fragmentation which is provided by the `LeastFreeCapacity` algorithm.

For users still preferring the "always BestFit" profile, we introduce the `TASProfileBestFit` feature gate, marking it as deprecated. We will remove it in v0.17 if we see no report indicating a need for that configuration.

### Two-level Topology Aware scheduling
In consideration of a [Story 5](#story-5) a two-level scheduling is introduced.
We introduce a notion of PodSet Slice, which is a subset of PodSet. All slices
have the same size and evenly divide the PodSet. To simplify implementation we
are allowing only for a "required" topology for slice. The requested topology
level for slices has to be on the same or below level of the main topology.

Requesting slice topology changes the way algorithm chooses domains. In all
modes in [Computing the assignment](#computing-the-assignment) algorithm greedily
selects domains based on the free capacity (either most or least). However, with
slices it considers the amount of slices that can fit into a domain as free
capacity, but if that number is equal then it prioritizes domains with fewer
remaining resources.

#### Example
Consider a rack with nodes that can accommodate 6, 5, 4, 3, and 2 pods respectively.
The required topology for slice is "host" and the podset slice size is 2.

As an example let's analyze the assignment based on the size of a podset and selected
mode. See the table below where the numbers are pods assigned to a particular node.
The header for columns dedicated to nodes correspond node's initial capacity.

| mode              | Pods count | 6   | 5   | 4   | 3   | 2   |
| ----------------- | ---------- | --- | --- | --- | --- | --- |
| BestFit           | 12         | 6   | .   | 4   | .   | 2   |
| LeastFreeCapacity | 10         | .   | 2   | 4   | 2   | 2   |

Explanation:
- `BestFit` - We prioritized 3rd node over 2nd node because 3rd node was a tight fit among all domains that could fit 2 slices. The last domain has been "optimized" to find the tight fit.
- `LeastFreeCapacity` - We prioritized 3rd node, because it is a tight fit among all domains that could fit 2 slices.

It is worth noting that the tight fit mentioned above does not guarantee that no free capacity will be left within the assigned domains.

### Multi-level Topology Aware scheduling

> [!NOTE]
> For the alpha implementation, the multi-level code path and API surface are
> kept separate from two-level scheduling. In a future iteration, we plan to
> unify them behind a single internal data structure.

In consideration of [Story 9](#story-9), multi-level scheduling extends two-level
scheduling to support more than two levels of topology constraints (e.g.,
datacenter → block → rack → host). Up to 3 constraint layers
(`PodsetSliceRequiredTopologyConstraints`) can be specified, each defining a
topology level and group size. Each layer must reference a topology level
strictly lower (deeper) than the previous layer, and its size must evenly divide
the parent layer's size. This feature is gated by `TASMultiLayerTopology`
(alpha, disabled by default since v0.17).

In two-level scheduling, below the outermost slice level the algorithm
distributes individual pods (unit size = 1). With multi-level, a
`sliceSizeAtLevel` map records the required group size at each intermediate
level. During the downward traversal, the algorithm looks up this map to
distribute pods in correctly-sized groups rather than individually. Before
assigning groups at a given inner level, the algorithm recomputes `sliceState`
on the child domains as `state / innerSliceSize`, since the `sliceState`
populated during phase 1 reflects only the outermost slice size. The same
sorting and selection logic (BestFit / BalancedPlacement) is applied at each
level.

#### Example

Consider a topology with 3 levels: block → rack → host. A block contains 2
racks, and each rack contains 4 hosts. Each host can accommodate 8 pods. The
PodSet has 64 pods with `sliceSize=32` at the block level and one additional
slice layer with `sliceSize=16` at the rack level.

| Phase | Level | Action                                                                                                                                                                       |
|-------|-------|------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| 1     | block | Each block has capacity 64 pods. `sliceState` = 64/32 = 2 slices per block. Select the block with the best fit - one block hosts all 64 pods (2 slices).                     |
| 2     | rack  | `sliceSizeAtLevel` gives 16 at the rack level. Recompute child `sliceState` = 32/16 = 2. Distribute 64 pods across 2 racks in groups of 16: each rack gets 32 pods (2 × 16). |
| 3     | host  | No further slice layer, so `sliceSizeAtLevel` = 1. Distribute 32 pods per rack across 4 hosts individually: each host gets 8 pods.                                           |

### Cross-PodSet Topology Aware scheduling

In consideration of a [Story 6](#story-6) a cross-podset topology aware scheduling
is required, due to the fact that leader and workers are separate PodSets. To be able
to co-locate leaders with its workers in the same topology domain, we divide the
problem into subproblems:

- Ensure leader and workers end up on the same flavor
- Find topology domain to co-locate leader and workers

#### Ensure leader and workers end up on the same flavor

To ensure leader and workers are assigned the same flavor a notion of `PodSet Group` is
introduced, indicated by the PodSetTopologyRequest's PodSetGroupName field (see [Internal APIs](#internal-apis)):

This field specifies the name of a group of PodSets that should be placed on the same flavor.
This field is optional and if a PodSet does not define it, it will be placed on a flavor
independently of any other PodSets.

If two or more PodSets use the same value in `PodSetGroup`, they will be grouped together.
Their resource requests will be summed together and the result will be used to find a flavor
with proper resources and enough quota to fit the whole group.

It is important to mention, that the introduction of `PodSet Group` changes the flavor
assignment unit to `PodSet Group`.

User can influence the value of `PodSetGroup` by setting `kueue.x-k8s.io/podset-group-name`
annotation.

### Enforcing the assignment

When the workload has the PodSet assignments and is about to start we modify the
corresponding PodTemplates in the Job object to inject the
`kueue.x-k8s.io/topology` scheduling gate. Note that the assignment always
includes the lowest level of the topology (see
[PodSetAssignment is per lowest-topology level](#podsetassignment-is-per-lowest-topology-level)).

Then, there is a new component, called `TopologyUngater`, which is a Workload
reconciler which lists all pods for a given TAS PodSet, and ensures that the
pods in the expected number are un-gated to a given value.

Along with the scheduling gate, to each pod template the
`kueue.x-k8s.io/workload` label / annotation is
added to facilitate quick lookup (List request) for all pods corresponding to
the workload (and more specifically PodSetAssignment) by `TopologyAssigner`.

The `kueue.x-k8s.io/podset` is the label that was extracted and moved to general usage regardless of TAS feature.
This label serves as a link between the Job's PodTemplate and the name of the PodSet associated with the admitted Workload.
Also it is indication of the Job in progress as it is added when it starts and it is removed when it's stopped.

The TopologyUngater watches for pod events which trigger the reconciliation
loop. The reconciliations are batched by 1s periods to make sure multiple
created pods at the similar time don't trigger too many reconciliations.

In order to prevent ungating more pods as expected for a PodSet we consider to
use the expectations mechanism. The expectations are set for when we are about
to ungate a Pod. The expectation is fulfilled if the Pod is observed as ungated
or the ungating request fails. We hold ungating if there are pending ungatings
within the PodSet.

### Support for Elastic Workloads

TAS supports integration with ElasticJobsViaWorkloadSlices when the
ElasticJobsViaWorkloadSlicesWithTAS feature gate is enabled. See
[KEP-77: Dynamically Sized Jobs](../77-dynamically-sized-jobs#topology-aware-scheduling-integration)
for details on supported modes and implementation.

### Balanced placement
The balanced placement algorithm provides an alternative to the greedy packing strategies. Instead of iterating over the domains sorted from largest to smallest available space (or based on some other criteria) and trying to pack as many pods as possible to each domain until the request fits, it first finds the optimal set of domains that fit the request and then distributes the pods as evenly as possible across these domains. 

Greedy placement strategies (such as `BestFit` and `LeastFreeCapacity`) might result in a placement with a small
number of pods assigned to the last considered domain (even though the existing algorithms choose the best possible
last domain). For example 12 pods distributed among domains with capacities (10,10) will be placed (10,2). However,
in some applications, a more balanced placement (6,6) would be more efficient. Some examples of such cases would be
all-to-all communication procedures (e.g. Allgather) since more balanced placement leads to more efficient
cross-domain traffic.

In the first implemetation, we propose to perform the balanced algorithm only on two consequtive levels indicated
by the user with the `preferred` flag. Let L be the level indicated by the `preferred` flag. We assume that the
request must fit within a single domain on level L-1 and otherwise we fallback to the standard algorithm. The above
assumptions are motivated by the application of the balanced placement algorithm to all-to-all communication
on the specific networking for GPUs. If the concept of balanced placement would be useful in other contexts it
would be possible to lift these assumptions. This balancing algorithm is enabled by the `TASBalancedPlacement` feature gate.

The algorithm could be summarized as follows:

```
1. For each domain on level L-1:
   - check if the entire request fits on this domain
   - calculate T, the maximum possible minimum number of pods that would be placed on a domain on level L+1 if the request is placed on this domain.
2. If no domain on level L-1 fits the entire request, fallback to the standard algorithm.
3. Otherwise, pick a domain D on level L-1 that 
   - first maximizes the value of T
   - secondly minimizes the number of domains that need to be used on level L to fit the request 
4. Prune every descendant of D with capacity below T.
5. On level L find an optimal subset of children of D that fit the request:
   - first minimize the size of the subset
   - secondly minimize the total capacity of the subset
   - thirdly maximize entropies of children capacities of the subset
6. On level L+1 find an optimal subset of children of domains from step 5 that fit the request:
   - first minimize the size of the subset
   - secondly minimize the total capacity of the subset
7. For the subset found in step 6, place the pods by first placing T pods on each domain, and then distribute the rest abritrarily.
```

The balancing algorithm will support all the job types currently supported by TAS including:

 - JobSet. Instead of assigning pods, the balancing algorithm will assign slices.
 - LeaderWorkerSet. Will co-locate leader with workers while keeping the overall
 assignment per node balanced.

#### Example
For a 3-level topology (block, rack, hostname), in the TopologyRequest, the user specifies `preferred = rack`. Then the balanced placement will find the following assignments:

| Capacities |Request | SliceSize | Assignment | Comment|
| --- | --- | --- | ----------- | --- |
| [[15], [15]] | 25| 1 | [[13], [12]] |
| [[15, 13, 10]] | 23| 1 | [[12, 11, 0]] |
| [[20, 10], [15, 15]] | 22| 1 | [[0, 0], [11, 11]] | prefer rack that leads to higher value of T
| [[20, 10], [15, 15]] | 20| 1 | [[20, 0], [0, 0]] |
| [[10, 5], [5, 5, 5]] | 15|1 |  [[0, 0], [5, 5, 5]] | prefer more balanced rack
| [[15],[15]] [[15, 15]] | 25 | 1 | [[0],[0]] [[13, 12]] | prefer block where the request fits in a single rack
| [[15], [15], [15, 15]] | 25|5 |  [[0], [0], [15, 10]] | `podset-slice-required-topology = hostname`
### Support for ProvisioningRequests

We are going to support autoscaling via ProvisioningRequest AdmissionCheck.

The key assumptions of the solution:
- When there is ProvisioningRequest on the list of AdmissionChecks Kueue
  Scheduler only reserves quota, but skips TAS assignment until all
  AdmissionChecks (including the ProvisioningRequest) are `Ready` (green).
- When all AdmissionChecks are `Ready` Kueue Scheduler does "second pass" on the
  workload to extend the "TAS" assignment, but respecting the previously
  selected resource flavor.
- In order to do "second pass" for such workloads Kueue Scheduler maintains a
  dedicated "secondPassQueue" queue of workloads ready for the second pass -
  with QuotaReservation and all AdmissionChecks `Ready`, but without required
  TopologyAssignments.

#### Determining the need for second pass

In order to determine if the "second pass" is needed, and thus if
`TopologyAssignment` is required, Kueue sets the following field as true
at the moment of QuotaReservation (first pass).

```golang
// DelayedTopologyRequestState indicates the state of the delayed TopologyRequest.
// +enum
type DelayedTopologyRequestState string

const (
   // This state indicates the delayed TopologyRequest is waiting for determining.
   DelayedTopologyRequestStatePending DelayedTopologyRequestState = "Pending"

   // This state indicates the delayed TopologyRequest was requested and completed.
   DelayedTopologyRequestStateReady DelayedTopologyRequestState = "Ready"
)

type PodSetAssignment struct {
  ...
  // delayedTopologyRequest indicates the topology assignment is delayed.
  // Topology assignment might be delayed in case there is ProvisioningRequest
  // AdmissionCheck used.
  // Kueue schedules the second pass of scheduling for each workload with at
  // least one PodSet which has delayedTopologyRequest=Pending and without
  // topologyAssignment. Once the TopologyAssignment is determined the state is
  // set to Ready.
  //
  // +optional
  DelayedTopologyRequest *DelayedTopologyRequestState `json:"delayedTopologyRequest,omitempty"`
}
```

This field will be particularly useful to implement functions such as
[SyncAdmittedCondition](https://github.com/kubernetes-sigs/kueue/blob/f61f8e9549801e0f92c4f38fa06649cccfcc8d7d/pkg/workload/admissionchecks.go#L33)
without the need to inspect the status of other objects (such as the
ResourceFlavor and ClusterQueue in case of implicit defaulting).

#### Targeting the newly provisioned nodes

Some classes of provisioning requests may be always provisioning new nodes.
In that case we need to give TAS the information about the node selector which
should be used to target the newly provision nodes. We propose all the necessary
information is contained in the ProvisioningRequest status, in the
[provisioningClassDetails](https://github.com/kubernetes/autoscaler/blob/79a1375afe82416c528e72448ca5d5d96e6ad4ba/cluster-autoscaler/apis/provisioningrequest/autoscaling.x-k8s.io/v1/types.go#L169)
field.

See the details of the API extension in the [Provisioning Request Support KEP](/keps/1136-provisioning-request-support).

#### Error handling

For some ProvisioningRequest classes, even if `Provisioned=true`, the nodes may
still not allow for scheduling for a while, several minutes, and remain NotReady,
for example, waiting for installation of some GPU drivers.

To solve this complication, when the "second pass" fails scheduler keeps quota
for the workload and retries with exponential delay.

The exact base delay and limit are subject to implementation decision, but they
should be of order of magnitude: base delay of 10s, and limit of 5min. Once the
time limit is exceeded the workload is evicted.

We keep the retry counter in memory for simplicity of the implementation, and
because the time limit is assumed small (several minutes), and Kueue is expected
to be restarted rarely. If Kueue is restarted the consequences are limited - the
workload may need to repeat a couple of attempts. We may revisit this decision
based on user feedback.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes
necessary to implement this enhancement.

##### Prerequisite testing updates

#### Unit Tests

We add integration tests for the following scenarios:
- a new node is added making workload admission possible
- the PodSet assignment is computed successfully for `podset-required-topology`
- the PodSet assignment cannot be computed for `podset-required-topology`
- the PodSet assignment is computed successfully for `podset-preferred-topology`
  across multiple values
- the PodSet assignment cannot be computed for `podset-preferred-topology`
- the schedulingGate is added to the pod template

#### Integration tests

We are going to add the integration tests to make sure the implementation is
well covered. In particular, the following scenarios need coverage:
- adding new nodes
- PodSet assignment for `podset-preferred-topology` and `podset-required-topology`
- Job's pod template has the "topology" scheduling gate injected
- un-gate pods by with the "topology" scheduling gate
- the Workload can be suspended and unsuspended

### Graduation Criteria

#### Alpha:

- support for all built-in integrations
- support multi-level hierarchy
- support TAS with minimal cross to other features (no cohorts, no preemption,
  no reclaimable pods)

The new validations which are for MVP, but likely will be relaxed in the future:
- ClusterQueue is marked inactive if it contains a TAS ResourceFlavor and
  belongs to a cohort
- ClusterQueue is marked inactive if it contains a TAS ResourceFlavor and
  enables preemptions
- ClusterQueue is marked inactive if it contains a TAS ResourceFlavor and uses
  MultiKueue admission check
- ClusterQueue is marked inactive if it contains a TAS ResourceFlavor and uses
  ProvisioningRequest admission check

#### Beta

- support rank-aware packing of pods, so that pods with consecutive ranks are
  placed within the same topology domain, if possible ([story 4](#story-4)).
- support replacement timeout in WaitForPodsReady
- re-evaluate accounting for used resources by watching DaemonSets
- re-evaluate the need for the "auto" mode which does not require a user to
  specify the hierarchy name, but just selects the lowest one
- re-evaluate the need to support for "preferred/required" preferences at the
  ReplicatedJob level (see [Story 2](#story-2))
- re-evaluate the need to support for "preferred/required" preferences at the
  Workload level (see [Story 3](#story-3))
- re-evaluate the need for the `kueue.x-k8s.io/tas`
- re-evaluate handling of topologies without `kubernetes.io/hostname` in the lowest
  level, the main issues are: (a) no check for fragmentation, and (b) no support
  for node taints. Some options to consider include virtual level as proposed in
  the [issue](https://github.com/kubernetes-sigs/kueue/issues/3658#issuecomment-2505583333)
  or explicit level added by webhook.
- re-evaluate the need for admin-facing configuration of the second phase
  requeuing for ProvisioningRequests based on user feedback
- change how the information about the failed nodes is stored at a Workload from Annotation into a field in workload.Status
- handle a more comprehensive set of failure scenarios (e.g., including node becoming unschedulable due to a taint)
- re-evaluate replacing `NodeToReplace` annotation with a status field, to optimize number of requests in scheduler loop. [Discussion](https://github.com/kubernetes-sigs/kueue/issues/5560)

#### Stable

- support indicating the order of Pods for external Jobs (see extension to [Story 4](#story-4))
Consider the following improvements and implement if feasible:
- put pods with consecutive indexes of an IndexedJob on close nodes
- support the following features: reclaimable, pods, cohorts preemption
 within cluster queue
- support re-computing the TopologyAssignment while the workload
 is running
- perform full scheduling simulation rather than just capacity counting
 (including pod affinities and anti-affinities)
- drop `TASLeastAllocated` feature gate
- introduce configuration for setting TAS profiles/algorithms: https://github.com/kubernetes-sigs/kueue/issues/4570
- introduce a performance test for TAS [#4634](https://github.com/kubernetes-sigs/kueue/issues/4634)
- add observability metrics, some ideas are in the [discussion](https://github.com/kubernetes-sigs/kueue/pull/5078#discussion_r2060580973)
- introduce configuration for the Node Hot Swap sub-feature: https://github.com/kubernetes-sigs/kueue/issues/6514

## Implementation History

- 2024-07-25 - PR merged [Allow mutating schedulingGates when the Jobset is suspended](Allow mutating schedulingGates when the Jobset is suspended)
- 2024-07-30 - this KEP


## Drawbacks

Tracking of nodes by Kueue will increase its code complexity and memory
consumption.

## Alternatives

### Account usage by watching DaemonSet pods

The idea is to account for DaemonSet pods by tracking the DaemonSets rather than
their Pods. This can prevent the [race condition](Race condition when accounting
for DaemonSet pods) risk.

**Reasons for discarding/deferring**

When accounting for the the DaemonSets we would need to verify their selectors
would allow match a node in question.

This is a complication which may not be necessary for MVP since the workloads
will mostly expected to consume accelerators (GPUs and TPUs).

We will consider this for Beta or GA based on the users' feedback.

### Use label for workload

We considered using the `kueue.x-k8s.io/workload` label rather than annotation.

**Reasons for discarding/deferring**

Currently workloads can have names which exceed the maximal length for a label.
So, we would need to shorten the maximal workload name by reducing the const
`maxPrefixLength` in `workload_names.go`. However, we can also facilitate
fast lookups based on labels if we index the workloads.

### Implement it in ClusterAutoscaler or kube-scheduler

This feature requires tracking of the nodes to reconstruct the nodes capacity
for all topology domains, and this is a novelty in Kueue which didn't do that,
leaving the features requiring knowledge about Nodes to kube-scheduler or
Cluster Autoscaler.

**Reasons for discarding/deferring**

kube-scheduler:
- scheduler does not have ability to do what `TopologyUngater` does, it is
  lower-level of abstraction
- the topology information is not represented in the core k8s, and scheduler
  wouldn't read the information from CRDs
- kube-scheduler doesn't do PodGroup level scheduling or preemption which is
  required

ClusterAutoscaler:
- Isn't focused on scheduling or preemption; its responsibility is to provision
  new Nodes
- Operates at the level of individual Pods rather than Jobs or PodGroups

Also, both of the components don't have a concept of quota, which we want to be
supported with this feature. So, if Kueue admits a Job with "required: rack"
just based on quota, the Pods might be created, but the components wouldn't
be able to schedule the Pods if there is not enough capacity in the topology
domain.

### Workload API alternatives

#### Drop the topologyAssignment.levels field

It was questioned during discussions (see [thread](https://github.com/kubernetes-sigs/kueue/pull/3237#discussion_r1802855787))
that maybe we don't need the `topologyAssignment.levels` field as it contains
information which could be obtained from the "live" Topology API via the
ResourceFlavor API.

**Reasons for discarding/deferring**

* implementation cost, reading the information from the Topology API would
  require lookup of the Topology API found via ResourceFlavor API. This would
  probably need to cache the information.
* the references between the "live" API objects can change since admission.
  Also, the API objects can be mutated or deleted. For example, if the list of
  levels in the topology API is changed since the admission time, then
  TopologyUngater would construct invalid node selectors. Preventing this would
  add complexity.
* consistency with what we do for the [Flavors](https://github.com/kubernetes-sigs/kueue/blob/d9db44460c0b61532b3ca03c7f98ef935dd848ff/apis/kueue/v1beta1/workload_types.go#L98-L99)
  as the full mapping between resources and flavors is stored at the moment of
  admission, rather than reading resources from the "live" CQ API object.
* the size of the field is limited by the number of levels (max 8 items).

#### Rename the topologyAssignment.domains.values field as levelValues

It was questioned during discussions (see [thread](https://github.com/kubernetes-sigs/kueue/pull/3237#discussion_r1802849888))
that maybe we could rename the `values` field in `topologyAssignment.domains` as
`levelValues`. The field `levelValues` could better express the intention that
it specifies values for the keys in the `levels` field.

**Reasons for discarding/deferring**

* the longer field name would increase the size of the JSON serialization of the
  API object, as the field name is repeated for each assigned topology domain.
  For some setup (lowest hierarchy level specifying single node) we may have
  tens of thousands of domains.
* the field is on Workload API, which is generally not a user-facing API in
  Kueue. Workload objects are meant for the internal mechanics of Kueue.
* the intention of the field can be expressed in a comment.

### Drop dedicated TAS label

During Beta graduation, we found the effort-less approach to identify TAS Pods
by alternative approach without TAS label. 
So, this proposal (drop TAS label) was introduced.
The following is the outdated evaluation when the first implementations. 

We could reduce the API surface by dropping the TAS label. The label is going
to be used in two places:
1. in TopologyUngater to quickly skip non-TAS pods in the events handler
2. when accounting for usage from Pods to quickly list only non-TAS pods

However, it was pointed out in discussions that the use case could probably be
fulfilled with a dedicated indexed virtual field.

**Reasons for discarding/deferring**

Increased code complexity which could defer the 0.9 release for the Alpha
version. We will re-evaluate the need for the label before the Beta release.

### MostFreeCapacity algorithm

Alongside the `BestFit` and `LeastFreeCapacity` algorithms, the `MostFreeCapacity`
had been implemented and was present in Kueue until it was dropped in version 0.13.

The algorithm worked by selecting as many domains as needed (if it meets the user's
requirement) starting from the one with the most free capacity.

The difference from `BestFit` algorithm is in the lack of optimization of
the last domain.

This algorithm was enabled by the `TASProfileMostFreeCapacity` feature flag and it
was independent of PodSet's annotations:

| featuregate/annotation     | preferred        | required         | unconstrained    |
| -------------------------- | ---------------- | ---------------- | ---------------- |
| TASProfileMostFreeCapacity | MostFreeCapacity | MostFreeCapacity | MostFreeCapacity |

#### Example
Consider a rack with four nodes that can accommodate 3, 3, 2, and 1 pod, respectively.
A PodSet consists of 7 pods.

Both the BestFit and MostFreeCapacity algorithms will initially iterate over the nodes
and select the first two, each with 3 available pods, as they possess the most
free capacity. With 1 pod remaining to schedule, the difference between the algorithms
becomes apparent:
- The `BestFit` algorithm optimizes the choice of the last node (domain) and selects the node that can accommodate exactly 1 pod.
- The `MostFreeCapacity` algorithm simply selects the node with the most remaining free capacity, which is 2 in this case.

**Reasons for discarding/deferring**
Due to code simplicity concerns and a lack of use cases for the algorithm,
the decision was made to remove it in favor of `BestFit`.

### TopologyAssignmentSlices as separate CRD instances

In the [v1beta2 format](#since-v1beta2) for TopologyAssignment, we introduce TopologyAssignmentSlices embedded in the WorkloadStatus. This helps fitting larger workloads within a single etcd entry, but still hits a scalability limit.

One way of going beyond that limit would be to extract the slices into separate instances of a dedicated CRD (analogously to how [EndpointSlice](https://github.com/kubernetes/kubernetes/blob/3b632270e9b866ee8bf62e89377ae95987671b49/pkg/apis/discovery/types.go#L24-L29) has been introduced in K8s core). This would, in practice, allow storing arbitrarily many nodes, though at some cost. (See "Reasons for deferring" below).

For these reasons, if we choose to do it, we would likely extract only "excess" slices, so that the TAS assignment for smaller workloads can be still kept inside WorkloadStatus.

**Reasons for discarding/deferring**

- Decreased UX of the API (some info delegated to other objects).
- Decreased performance of Kueue scheduler (need to do more etcd reads and writes). \
  (In particular, even when we end up using slices in separate CRDs, the "compression capabilities" introduced in v1beta2 are going to improve performance by reducing the necessary number of such slices).
