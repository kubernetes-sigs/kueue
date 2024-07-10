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
- The kubectl command-line tool can communicate with your cluster.
- [Kueue is installed](/docs/installation).

## Example

The example below shows you how to set up the Job Admission Policy to reject early all Job or JobSets
without the queue-name if sent to a namespace other than `admin-namespace`.

You should set `manageJobsWithoutQueueName` to `false` in the Kueue Configuration to let an admin
to execute Jobs in the `admin-namespace`. Jobs sent to this namespace aren't rejected, or managed
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

Now, when you try to create a `Job` or a `JobSet` without the `kueue.x-k8s.io/queue-name` label or value in any namespace other than `admin-namespace`,
the error message will be similar to the following:

```
ValidatingAdmissionPolicy 'sample-validating-admission-policy' with binding 'sample-validating-admission-policy-binding' denied request: The label 'kueue.x-k8s.io/queue-name' is either missing or does not have a value set.
```

