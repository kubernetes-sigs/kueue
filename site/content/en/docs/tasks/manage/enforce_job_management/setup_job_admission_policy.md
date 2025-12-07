---
title: "Setup a Job Admission Policy"
date: 2024-06-28
weight: 10
description: >
  Implementing Validating Admission Policy to prevent job creation without queue name.
---

This page shows how you can set up for Kueue a Job Admission Policy using the Kubernetes [Validating Admission Policy](https://kubernetes.io/docs/reference/access-authn-authz/validating-admission-policy), based on the [Common Expression Language (CEL)](https://github.com/google/cel-spec).

## Before you begin

Ensure the following conditions are met:

- A Kubernetes cluster is running.
- The `ValidatingAdmissionPolicy` [feature gate](https://kubernetes.io/docs/reference/command-line-tools-reference/feature-gates/) 
is enabled. In Kubernetes 1.30 or newer, the feature gate is enabled by default.
- The kubectl command-line tool can communicate with your cluster.
- [Kueue is installed](/docs/installation).

## Enforce Queue Name - Example 

The example below shows you how to set up a Job Admission Policy to reject early all Job or JobSets
without the queue-name if sent to a namespace labeled as a `kueue-managed` namespace.

You should set `manageJobsWithoutQueueName` to `false` in the Kueue Configuration to let an admin
to execute Jobs in any namespace that is not labeled as `kueue-managed`. Jobs sent to unlabeled namespaces aren't rejected, or managed
by Kueue.

{{< include "examples/sample-validating-policy.yaml" "yaml" >}}

To create the policy, download the above file and run the following command:

```shell
kubectl create -f sample-validating-policy.yaml
```

Then, apply the validating admission policy to the namespace by creating a `ValidatingAdmissionPolicyBinding`. The policy binding links the namespaces to the defined admission policy and it instructs Kubernetes how to respond to the validation outcome.

The following is an example of a policy binding:

{{< include "examples/sample-validating-policy-binding.yaml" "yaml" >}}

To create the binding, download the above file and run the following command:

```shell
kubectl create -f sample-validating-policy-binding.yaml
```

Run the following command to label each namespace where you want this policy to be enforced:

```shell
kubectl label namespace my-user-namespace 'kueue-managed=true'
```

Now, when you try to create a `Job` or a `JobSet` without the `kueue.x-k8s.io/queue-name` label or value in any namespace
that is labeled as `kueue-managed`, the error message will be similar to the following:

```
ValidatingAdmissionPolicy 'sample-validating-admission-policy' with binding 'sample-validating-admission-policy-binding' denied request: The label 'kueue.x-k8s.io/queue-name' is either missing or does not have a value set.
```

## Enforce Resource Requests - Example

Kueue calculates quota usage based on the `requests` field in the Pod specification. If a workload is submitted without any resource requests, Kueue effectively treats it as consuming zero resources, which can bypass quota limits.

You can use a `ValidatingAdmissionPolicy` to reject Workloads that do not specify at least one resource request or limit.

**Note:** Kubernetes implicitly treats `limits` as `requests` if the `requests` field is missing. The example policy below checks for the presence of either `requests` or `limits` to accommodate this behavior.

{{< include "examples/sample-enforce-requests-policy.yaml" "yaml" >}}

To create the policy, download the above file and run the following command:

```shell
kubectl create -f sample-enforce-requests-policy.yaml
```

The following is an example of the policy binding:

{{< include "examples/sample-enforce-requests-policy-binding.yaml" "yaml" >}}

To create the binding, download the above file and run the following command:

```shell
kubectl create -f sample-enforce-requests-policy-binding.yaml
```

Now, when you try to submit a workload without requests or limits defined to the kueue managed namespace, the error message is displayed in the following format:

```
ValidatingAdmissionPolicy 'sample-enforce-requests-policy' with binding 'sample-enforce-requests-policy-binding' denied request: Every container in the workload must specify either resource requests or limits.
```

## Handling of Parent/Child Relationships

One complication of this approach is that when a Workload is admitted, it may create child resources
of Kinds that are also managed by Kueue. These children are not required to have the queue-name label,
because their admission is implied by the admission of their parent.  In the example policy, the `exclude-jobset-owned`
`matchCondition` ensures that a child `Job` that is owned by a parent `JobSet` will not be denied admission.
