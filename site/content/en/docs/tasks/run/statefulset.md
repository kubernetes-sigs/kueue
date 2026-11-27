---
title: "Run StatefulSet"
linkTitle: "StatefulSet"
date: 2024-07-25
weight: 6
description: >
  Run a StatefulSet as a Kueue-managed workload.
---

This page shows how to leverage Kueue's scheduling and resource management
capabilities when running StatefulSets.

We demonstrate how to support scheduling StatefulSets in Kueue based on the
[Plain Pod Group](https://kueue.sigs.k8s.io/docs/tasks/run/plain_pods/) integration,
where StatefulSet is represented as a single independent workload.

This guide is for [serving users](/docs/tasks#serving-user) that have a basic understanding of Kueue.
For more information, see [Kueue's overview](/docs/overview).

## Before you begin

1. The `statefulset` integration is enabled by default.

2. For Kueue v0.15 and earlier, learn how to [install Kueue with a custom manager configuration](/docs/installation/#install-a-custom-configured-released-version)
   and ensure that you have the `statefulset` integration enabled, for example:
   ```yaml
   apiVersion: config.kueue.x-k8s.io/v1beta2
   kind: Configuration
   integrations:
     frameworks:
      - "statefulset"
   ```
   {{% alert title="Pod integration requirements" color="primary" %}}
   Since Kueue v0.15, you don't need to explicitly enable `"pod"` integration to use the `"statefulset"` integration.

   For Kueue v0.14 and earlier, `"pod"` integration must be explicitly enabled.

   See [Run Plain Pods](/docs/tasks/run/plain_pods/#before-you-begin) for configuration details.
   {{% /alert %}}

3. Check [Administer cluster quotas](/docs/tasks/manage/administer_cluster_quotas) for details on the initial Kueue setup.

## Running a StatefulSet admitted by Kueue

When running a StatefulSet on Kueue, take into consideration the following aspects:

### a. Queue selection

The target [local queue](/docs/concepts/local_queue) should be specified in the `metadata.labels` section of the StatefulSet configuration.

```yaml
metadata:
   labels:
      kueue.x-k8s.io/queue-name: user-queue
```

### b. Configure the resource needs
The resource needs of the workload can be configured in the `spec.template.spec.containers`.

```yaml
spec:
   template:
      spec:
         containers:
            - resources:
                 requests:
                    cpu: 3
```

### c. Scaling

Currently, scaling operations on StatefulSets are not supported.
This means you cannot perform scale up or scale down operations directly through Kueue.

### d. Updating the Pod template

While Kueue manages a StatefulSet, it rejects changes to the fields of the Pod template
that affect admission:

- `nodeSelector`, `affinity`, `tolerations`, `topologySpreadConstraints`, `runtimeClassName`,
  `priority`, `overhead` and `resourceClaims`.
- The number of `containers` and `initContainers`, and their `resources.requests` and `ports`.
- The Pod-level `resources.requests`, and adding or removing the Pod-level `resources`.

You can change the other fields, for example the image, the command or the environment variables
of a container.

Since Kueue v0.21, Kueue also rejects changes that keep the requests in the template but change
the effective resource requests of the Pods, which the quota of the Workload is based on:

- Adding, changing or removing a limit of a container that has no request for that resource.
- Changing a Pod-level limit in `resources.limits` of a resource that has no Pod-level request.
- Turning an init container into a sidecar container with `restartPolicy: Always`.

Kueue computes the effective requests as for the quota of the Workloads: limits are used as missing
requests, for the containers and for the Pod-level resources, then the defaults of the LimitRanges
in the namespace fill in the requests that are still missing. With Pod-level resources, the
effective request of a resource is the larger of the Pod-level request and the requests of the
containers (init containers count at their peak, sidecar containers together with the regular
containers).
A change that leaves the effective requests the same is accepted, for example changing a limit
that has a matching request, or raising a container limit that has no request while
the container requests stay at or below the Pod-level limit.
To change a Pod-level limit later, set the Pod-level request of that resource when you create the
StatefulSet: adding a Pod-level request is rejected while Kueue manages the StatefulSet.
Earlier versions of Kueue accepted all of these changes.

To turn this off, disable the `ValidateEffectiveRequestsOnPodTemplateUpdate`
[feature gate](/docs/getting-started/installation/#feature-gates-for-alpha-and-beta-features).
Kueue then rejects only the changes listed at the start of this section, so the quota of the
Workload can differ from what the new Pods request.

## Example
Here is a sample StatefulSet:

{{< include "examples/serving-workloads/sample-statefulset.yaml" "yaml" >}}

You can create the StatefulSet using the following command:

```sh
kubectl create -f sample-statefulset.yaml
```
