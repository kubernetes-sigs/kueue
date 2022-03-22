# Queued Workload

A _queued workload_ (or simply _workload_) is an application that will run to
completion. It can be composed by one or multiple Pods that, loosely or tightly
coupled, that, as a whole, complete a task. A workload is the unit of [admission](README.md#admission.md)
in Kueue.

The prototypical workload can be represented with a
[Kubernetes `v1/batch.Job`](https://kubernetes.io/docs/concepts/workloads/controllers/job/).
For this reason, we sometimes use the word _job_ to refer to any workload, and
`Job` when we refer specifically to the Kubernetes API.

However, Kueue does not directly manipulate `Job` objects. Instead, Kueue
manages `QueuedWorkload` resources that represent the resource requirements
of an arbitrary workload. Kueue automatically creates a `QueuedWorkload` for
each `Job` object and syncs the decisions and statuses.

## Custom workloads

As described previously, Kueue has built-in support for workloads created with
the Job API. But any custom workload API can integrate with Kueue by
creating a corresponding `QueuedWorkload` object for it.