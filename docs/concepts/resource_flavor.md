# ResourceFlavor

Resources in a cluster are typically not homogeneous. Resources could differ in:
- pricing and availability (for example, spot verus on-demand VMs)
- architecture (for example, x86 versus ARM CPUs)
- brands and models (for example, Radeon 7000 versus Nvidia A100 versus T4 GPUs)

A ResourceFlavor is an object that represents these resource variations and
allows you to associate them with cluster nodes through labels and taints.

**Note**: If your the resources in your cluster are homogeneous, you can use 
an [empty ResourceFlavor](#empty-resourceflavor) instead of adding labels to custom ResourceFlavors.

A sample ResourceFlavor looks like the following:

```yaml
apiVersion: kueue.x-k8s.io/v1alpha2
kind: ResourceFlavor
metadata:
  name: spot
nodeSelector:
  instance-type: spot
taints:
- effect: NoSchedule
  key: spot
  value: "true"
```

You can use the `.metadata.name` field to reference a ResourceFlavor from a
[ClusterQueue](/docs/concepts/cluster_queue.md) in the `.spec.resources[*].flavors[*].name` field.

## ResourceFlavor labels

**Requires Kubernetes 1.23 or newer**

To associate a ResourceFlavor with a subset of nodes of your cluster, you can
configure the `.nodeSelector` field with matching node labels that uniquely identify
the nodes. If you are using [cluster autoscaler](https://github.com/kubernetes/autoscaler/tree/master/cluster-autoscaler)
(or equivalent controllers), make sure that the controller is configured to add those labels when
adding new nodes.

To guarantee that the workload Pods run on the nodes associated to the flavor
that Kueue selected, Kueue performs the following
steps:

1. When admitting a workload, Kueue evaluates the
   [`.nodeSelector`](https://kubernetes.io/docs/concepts/scheduling-eviction/assign-pod-node/#nodeselector)
   and [`.affinity.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution`](https://kubernetes.io/docs/concepts/scheduling-eviction/assign-pod-node/#node-affinity)
   fields in the PodSpecs of your [Workload](workload.md) against the
   ResourceFlavor labels.

   `ResourceFlavors` that don't match the node affinity of the workload
   cannot be assigned to a workload's podSet.


2. Once the workload is admitted, Kueue adds the ResourceFlavor labels to the
  `.nodeSelector` of the underlying workload Pod templates. This occurs if the workload
   didn't specify the `ResourceFlavor` labels already.

   For example, for a [batch/v1.Job](https://kubernetes.io/docs/concepts/workloads/controllers/job/),
   Kueue adds the labels to the `.spec.template.spec.nodeSelector` field. This
   guarantees that the workload's Pods can only be scheduled on the nodes
   targeted by the flavor that Kueue assigned to the workload.

## ResourceFlavor taints

To restrict the usage of a ResourceFlavor, you can configure the `.taints` field
with taints. These taints should typically match the taints of the Nodes associated with ResourceFlavor.

Taints on the ResourceFlavor work similarly to [node taints](https://kubernetes.io/docs/concepts/scheduling-eviction/taint-and-toleration/).
For Kueue to admit a workload to use the ResourceFlavor, the PodSpecs in the
workload should have a toleration for it. As opposed to the behavior for
[ResourceFlavor labels](#resourceflavor-labels), Kueue does not add tolerations
for the flavor taints.

## Empty ResourceFlavor

If your cluster has homogeneous resources, or if you don't need to manage
quotas for the different flavors of a resource separately, you can create a
ResourceFlavor without any labels or taints. Such ResourceFlavor is called an
empty ResourceFlavor and its definition looks like the following:

```yaml
apiVersion: kueue.x-k8s.io/v1alpha2
kind: ResourceFlavor
metadata:
  name: default-flavor
```

## What's next?

- Learn about [cluster queues](/docs/concepts/cluster_queue.md).