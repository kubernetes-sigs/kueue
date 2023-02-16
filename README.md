# Kueue

Kueue is a set of APIs and controller for [job](docs/concepts/workload.md)
[queueing](docs/concepts#queueing). It is a job-level manager that decides when
a job should be [admitted](docs/concepts#admission) to start (as in pods can be
created) and when it should stop (as in active pods should be deleted).

## Why use Kueue

Kueue is a lean controller that you can install on top of a vanilla Kubernetes
cluster. Kueue does not replace any existing Kubernetes components. Kueue is
compatible with cloud environments where:

- Compute resources are elastic and can be scaled up and down.
- Compute resources are heterogeneous (in architecture, availability, price, etc.).

Kueue APIs allow you to express:

- Quotas and policies for fair sharing among tenants.
- Resource fungibility: if a [resource flavor](docs/concepts/cluster_queue.md#resourceflavor-object)
  is fully utilized, Kueue can admit the job using a different flavor.

The main design principle for Kueue is to avoid duplicating mature functionality
in [Kubernetes components](https://kubernetes.io/docs/concepts/overview/components/)
and well-established third-party controllers. Autoscaling, pod-to-node scheduling and
job lifecycle management are the responsibility of cluster-autoscaler,
kube-scheduler and kube-controller-manager, respectively. Advanced
admission control can be delegated to controllers such as [gatekeeper](https://github.com/open-policy-agent/gatekeeper).

## Production Readiness status

- ✔️ Coverage with unit and integration tests: ~72%
- ✔️ Up-to-date [documentation](/docs).
- ✔️ Monitoring via [metrics](/docs/reference/metrics.md)
- ✔️ API validation and defaulting.
- ⏳ Scalability tests in progress.
- ⚠️ Latest API version: v1alpha2.

  _Based on community feedback, we continue to simplify and evolve the API to
  address new use cases_.

## Installation

**Requires Kubernetes 1.22 or newer**.

To install the latest release of Kueue in your cluster, run the following command:

```shell
kubectl apply -f https://github.com/kubernetes-sigs/kueue/releases/download/v0.2.1/manifests.yaml
```

The controller runs in the `kueue-system` namespace.

Read the [installation guide](/docs/setup/install.md) to learn more.

## Usage

A minimal configuration can be set by running the [samples](config/samples):

```
kubectl apply -f config/samples/single-clusterqueue-setup.yaml
```

Then you can run a job with:

```
kubectl create -f config/samples/sample-job.yaml
```

Learn more about:

- Kueue [concepts](docs/concepts).
- Common and advanced [tasks](docs/tasks).

## Architecture

<!-- TODO(#64) Remove links to google docs once the contents have been migrated to this repo -->

Learn more about the architecture of Kueue with the following design docs:

- [bit.ly/kueue-apis](https://bit.ly/kueue-apis) discusses the API proposal and a high
  level description of how Kueue operates. Join the [mailing list](https://groups.google.com/a/kubernetes.io/g/wg-batch)
to get document access.
- [bit.ly/kueue-controller-design](https://bit.ly/kueue-controller-design)
presents the detailed design of the controller.

## Roadmap

This is a high-level overview of the main priorities for 2023, in expected order of release:

- Job preemption to reclaim borrowed quota and to accommodate high priority jobs [#83](https://github.com/kubernetes-sigs/kueue/issues/83), this is planned for v0.3
- Cooperative preemption support for workloads that implement checkpointing [#477](https://github.com/kubernetes-sigs/kueue/issues/477)
- Flavor assignment strategies, e.g. _minimizing cost_ vs _minimizing borrowing_ [#312](https://github.com/kubernetes-sigs/kueue/issues/312)
- Integration with cluster-autoscaler for guaranteed resource provisioning
- Integration with common custom workloads [#74](https://github.com/kubernetes-sigs/kueue/issues/74):
  - Kubeflow (TFJob, MPIJob, etc.)
  - Spark
  - Ray
  - Workflows (Tekton, Argo, etc.)

These are features that we aim to have in the long-term, in no particular order:

- Budget support [#28](https://github.com/kubernetes-sigs/kueue/issues/28)
- Dashboard for management and monitoring for administrators
- Multi-cluster support

## Community, discussion, contribution, and support

Learn how to engage with the Kubernetes community on the [community page](http://kubernetes.io/community/).

You can reach the maintainers of this project at:

- [Slack](https://kubernetes.slack.com/messages/wg-batch)
- [Mailing List](https://groups.google.com/a/kubernetes.io/g/wg-batch)

### Code of conduct

Participation in the Kubernetes community is governed by the [Kubernetes Code of Conduct](code-of-conduct.md).
