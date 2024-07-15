---
title: "kubectl kueue create"
linkTitle: "Create"
date: 2024-07-02
weight: 20
description: >
  Create a resource
---

### Usage:

```
kubectl kueue create [TYPE]
```

### Examples:

```bash
# Create a local queue
kubectl kueue create localqueue my-local-queue -c my-cluster-queue

# Create a local queue with unknown cluster queue
kubectl kueue create localqueue my-local-queue -c my-cluster-queue -i

# Create a ClusterQueue 
kueuectl create clusterqueue my-cluster-queue
  
# Create a ClusterQueue with cohort, namespace selector and other details
kueuectl create clusterqueue my-cluster-queue \
  --cohort=cohortname \
  --queuing-strategy=StrictFIFO \
  --namespace-selector=fooX=barX,fooY=barY \
  --reclaim-within-cohort=Any \
  --preemption-within-cluster-queue=LowerPriority

# Create a ClusterQueue with nominal quota and one resource flavor named alpha
kueuectl create clusterqueue my-cluster-queue --nominal-quota=alpha:cpu=9;memory=36Gi

# Create a ClusterQueue with multiple resource flavors named alpha and beta
kueuectl create clusterqueue my-cluster-queue \
  --nominal-quota=alpha:cpu=9;memory=36Gi;nvidia.com/gpu=10,beta:cpu=18;memory=72Gi;nvidia.com/gpu=20, \
  --borrowing-limit=alpha:cpu=1;memory=1Gi;nvidia.com/gpu=1,beta:cpu=2;memory=2Gi;nvidia.com/gpu=2 \
  --lending-limit=alpha:cpu=1;memory=1Gi;nvidia.com/gpu=1,beta:cpu=2;memory=2Gi;nvidia.com/gpu=2
	
# Create a resource flavor 
kueuectl create resourceflavor my-resource-flavor

# Create a resource flavor with labels
kueuectl create resourceflavor my-resource-flavor \
  --node-labels beta.kubernetes.io/arch=arm64,beta.kubernetes.io/os=linux

# Create a resource flavor with taints
kueuectl create resourceflavor my-resource-flavor \
  --node-taints key1=value1:NoSchedule,key2=value2:NoSchedule

# Create a resource flavor with tolerations
kueuectl create resourceflavor my-resource-flavor \
  --tolerations key1=value:NoSchedule,key2:NoExecute,key3=value,key4,:PreferNoSchedule
```

## Resource types

The following table includes a list of all the supported resource types and their abbreviated aliases:

| Name           | Short | API version            | Namespaced | Kind           |
|----------------|-------|------------------------|------------|----------------|
| localqueue     | lq    | kueue.x-k8s.io/v1beta1 | true       | LocalQueue     |
| clusterqueue   | cq    | kueue.x-k8s.io/v1beta1 | false      | ClusterQueue   |
| resourceflavor | rf    | kueue.x-k8s.io/v1beta1 | false      | ResourceFlavor |