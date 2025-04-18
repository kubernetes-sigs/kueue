---
title: "Cohort"
date: 2025-04-16
weight: 4
description: >
  A cluster-scoped resource for organizing quotas
---

## Hello, Cohorts
Cohorts give you the ability to organize your Quotas. ClusterQueues within the same Cohort (or same CohortTree for [Hieararchical Cohorts](#hierarchical-cohorts)) can share resources with each other. The simplest possible Cohort is the following:

```yaml
apiVersion: kueue.x-k8s.io/v1alpha1
kind: Cohort
metadata:
  name: "hello-cohort"
```

A ClusterQueue may join this Cohort by referencing it:
```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: ClusterQueue
metadata:
  name: "my-cluster-queue"
spec:
  cohort: "hello-cohort"
```

## Configuring Quotas

Similarly to [ClusterQueues](/docs/concepts/cluster_queue/#flavors-and-resources) Resource quotas may be defined at the Cohort level, and consumed by ClusterQueues within the Cohort.

```yaml
apiVersion: kueue.x-k8s.io/v1alpha1
kind: Cohort
metadata:
  name: "hello-cohort"
spec:
  resourceGroups:
    - coveredResources: ["cpu"]
      flavors:
      - name: "default-flavor"
        resources:
        - name: "cpu"
          nominalQuota: 12
```

In order for a ClusterQueue to borrow resources from its Cohort, it **must**
define nominal quota for the desired Resource and Flavor -  even if this value is 0.

```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: ClusterQueue
metadata:
  name: "my-cluster-queue"
spec:
  cohort: "hello-cohort"
  resourceGroups:
  - coveredResources: ["cpu"]
    flavors:
    - name: "default-flavor"
      resources:
      - name: "cpu"
        nominalQuota: 0
```

## Hierarchical Cohorts
Cohorts may be organized in a tree structure. We refer to the grouping of ClusterQueues and Cohorts that are part of the same tree as a **CohortTree**.

ClusterQueues within a given CohortTree may use resources within it,
subject to [Borrowing and Lending limits](/docs/reference/kueue.v1beta1/#kueue-x-k8s-io-v1beta1-ResourceQuota).
These Borrowing and Lending Limits can be specified for Cohorts, as well as for ClusterQueues.

Here is a simple CohortTree, with three Cohorts:
```yaml
apiVersion: kueue.x-k8s.io/v1alpha1
kind: Cohort
metadata:
  name: "root-cohort"
---
apiVersion: kueue.x-k8s.io/v1alpha1
kind: Cohort
metadata:
  name: "important-org"
spec:
  parent: "root-cohort"
  fairSharing:
    weight: "0.75"
---
apiVersion: kueue.x-k8s.io/v1alpha1
kind: Cohort
metadata:
  name: "regular-org"
spec:
  parent: "root-cohort"
  fairSharing:
    weight: "0.25"
```

This example assumes that Fair Sharing is enabled. In this case, the important org will trend towards using 75% of common resources, while the regular org towards using 25%.
