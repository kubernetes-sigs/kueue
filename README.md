# Kueue

[![GoReport Widget]][GoReport Status]
[![Latest Release](https://img.shields.io/github/v/release/kubernetes-sigs/kueue?include_prereleases)](https://github.com/kubernetes-sigs/kueue/releases/latest)

[GoReport Widget]: https://goreportcard.com/badge/github.com/kubernetes-sigs/kueue
[GoReport Status]: https://goreportcard.com/report/github.com/kubernetes-sigs/kueue

<img src="https://github.com/kubernetes-sigs/kueue/blob/main/site/static/images/logo.svg" width="100" alt="kueue logo">

Kueue is a set of APIs and controller for [job](https://kueue.sigs.k8s.io/docs/concepts/workload)
[queueing](https://kueue.sigs.k8s.io/docs/concepts#queueing). It is a job-level manager that decides when
a job should be [admitted](https://kueue.sigs.k8s.io/docs/concepts#admission) to start (as in pods can be
created) and when it should stop (as in active pods should be deleted).

Read the [overview](https://kueue.sigs.k8s.io/docs/overview/) to learn more.

## Production Readiness status

- ✔️ API version: v1beta1, respecting [Kubernetes Deprecation Policy](https://kubernetes.io/docs/reference/using-api/deprecation-policy/)
- ✔️ Up-to-date [documentation](https://kueue.sigs.k8s.io/docs).
- ✔️ Good Test Coverage:
  - ✔️ Unit Test: ~77.2% (controllers are excluded for they're covered by integration tests)
  - ✔️ Integration Test: 124 testcases.
  - ✔️ E2E Test: available for kubernetes 1.24, 1.25, 1.26 on Kind.
- ✔️ Scalability verification via [performance tests](https://github.com/kubernetes-sigs/kueue/tree/main/test/performance).
- ✔️ Monitoring via [metrics](https://kueue.sigs.k8s.io/docs/reference/metrics).
- ✔️ Security: RBAC based accessibility.
- ✔️ Stable release cycle(2-3 months) for new features, bugfixes, cleanups.

  _Based on community feedback, we continue to simplify and evolve the API to
  address new use cases_.

## Installation

**Requires Kubernetes 1.22 or newer**.

To install the latest release of Kueue in your cluster, run the following command:

```shell
kubectl apply -f https://github.com/kubernetes-sigs/kueue/releases/download/v0.4.2/manifests.yaml
```

The controller runs in the `kueue-system` namespace.

Read the [installation guide](https://kueue.sigs.k8s.io/docs/installation/) to learn more.

## Usage

A minimal configuration can be set by running the [examples](examples):

```shell
kubectl apply -f examples/admin/single-clusterqueue-setup.yaml
```

Then you can run a job with:

```shell
kubectl create -f examples/jobs/sample-job.yaml
```

Learn more about:

- Kueue [concepts](https://kueue.sigs.k8s.io/docs/concepts).
- Common and advanced [tasks](https://kueue.sigs.k8s.io/docs/tasks).

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

Learn how to engage with the Kubernetes community on the [community page](http://kubernetes.io/community/)
and the [contributor's guide](CONTRIBUTING.md).

You can reach the maintainers of this project at:

- [Slack](https://kubernetes.slack.com/messages/wg-batch)
- [Mailing List](https://groups.google.com/a/kubernetes.io/g/wg-batch)

### Code of conduct

Participation in the Kubernetes community is governed by the [Kubernetes Code of Conduct](code-of-conduct.md).
