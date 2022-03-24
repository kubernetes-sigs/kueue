# Cluster Queue

A `ClusterQueue` is a cluster-scoped object that governs a pool of resources
such as CPU, memory and hardware accelerators. A `ClusterQueue` defines:
- The resource _flavors_ that it manages, with usage limits and order of consumption.
- Fair sharing rules across the tenants of the cluster.

Only cluster administrators should create `ClusterQueue` objects.

## Resource Flavors

Resources in a cluster are typically not homogeneous. Resources could differ in:
- pricing and availability (ex: spot vs on-demand VMs)
- architecture (ex: x86 vs ARM CPUs)
- brands and models (ex: Radeon 7000 vs Nvidia A100 vs T4 GPUs)

A `ResourceFlavor` is an object that represents these variations and allows
administrators to associate them with node labels and taints.

## Cohort

ClusterQueues can be grouped in _cohorts_. ClusterQueues that belong to the
same cohort can borrow unused quota from each other, if they have matching
resource flavors.
