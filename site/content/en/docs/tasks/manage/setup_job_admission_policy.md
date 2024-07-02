---
title: "Setup a Job Admission Policy"
date: 2024-06-28
weight: 10
description: >
  Implementing Validating Admission Policy to prevent job creation without queue name.
---

This page shows you how to set up a [Validating Admission Policy](https://kubernetes.io/docs/reference/access-authn-authz/validating-admission-policy) to prevent the creation of jobs without a queue name within a namespace.

## Before you begin

Ensure the following conditions are met:

- A Kubernetes cluster is running.
- The kubectl command-line tool can communicate with your cluster.

## Creating a Validating Admission Policy

To verify a job contains a queue name, check that the `kueue.x-k8s.io/queue-name` label exists and has a value set using the [Common Expression Language (CEL)](https://github.com/google/cel-spec).

Here is an example of a ValidatingAdmissionPolicy that rejects `Job` and `JobSet`  creation without a queue name:

{{< include "examples/sample-validating-policy.yaml" "yaml" >}}

Notice this policy applies a match condition to evaluate resources only in the `my-namespace` namespace.

To create the policy, download the above file and run the following command:

```shell
kubectl create -f sample-validating-policy.yaml
```

## Setting up the policy in a Cluster

Apply the validating admission policy to the namespace by creating a `ValidatingAdmissionPolicyBinding`. The policy binding links the namespace to the defined admission policy and it instructs Kubernetes how to respond to the validation outcome.

The following is an example of a policy binding:

{{< include "examples/sample-validating-policy-binding.yaml" "yaml" >}}

To create the binding, download the above file and run the following command:

```shell
kubectl create -f sample-validating-policy-binding.yaml
```

Now, when you try to create a `Job` or a `JobSet` without the `kueue.x-k8s.io/queue-name` label or value, an error message will return:

```
ValidatingAdmissionPolicy 'sample-validating-admission-policy' with binding 'sample-validating-admission-policy-binding' denied request: The label 'kueue.x-k8s.io/queue-name' is either missing or does not have a value set.
```

