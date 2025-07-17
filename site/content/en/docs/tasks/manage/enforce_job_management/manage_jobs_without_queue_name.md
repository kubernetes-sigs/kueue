---
title: "Setup manageJobsWithoutQueueName"
date: 2024-06-28
weight: 10
description: >
  Using manageJobsWithoutQueueName and mangedJobsNamespaceSelector to prevent admission of Workloads without assigned LocalQueues.
---

This page describes how to configure Kueue to ensure that all Workloads submitted in namespaces
intended for use by _batch users_ will be managed by Kueue even if they lack a `kueue.x-k8s.io/queue-name` label.

## Before you begin

Learn how to [install Kueue with a custom manager configuration](/docs/installation/#install-a-custom-configured-released-version).

## Configuration

You will need to modify the manage configuration to set `manageJobsWithoutQueueName` to true.

If you also modify the default configuration to enable the `Pod`, `Deployment` or `StatefulSet` integrations,
you may also need to override the default value of `managedJobsNamespaceSelector` to limit the scope of
`manageJobsWithoutQueueName` to only apply to _batch user_ namespaces.

The default value for `managedJobsNamespaceSelector` is
```yaml
matchExpressions:
- key: kubernetes.io/metadata.name
  operator: NotIn
  values: [ kube-system, kueue-system ]
```
This dafault value exempts the `kube-system` and `kueue-system` namespaces from management; all other
namespaces will be managed by Kueue when `manageJobsWithoutQueueName` is true.

Alternatively, the _batch administrator_ can label namespaces that are intended for _batch users_
and define a selector that matches only those namespaces.  For example,
```yaml
managedJobsNamespaceSelector:
  matchLabels:
    kueue.x-k8s.io/managed-namespace: true
```

## Expected Behavior

In all namespaces that match the namespace selector, any Workloads submitted without a `kueue.x-k8s.io/queue-name`
label will be suspended.  These Workloads will not be considered for admission by Kueue until
they are edited to have a `kueue.x-k8s.io/queue-name` label.
