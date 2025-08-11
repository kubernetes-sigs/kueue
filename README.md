# Kueue

[![GoReport Widget]][GoReport Status]
[![Latest Release](https://img.shields.io/github/v/release/kubernetes-sigs/kueue?include_prereleases)](https://github.com/kubernetes-sigs/kueue/releases/latest)

[GoReport Widget]: https://goreportcard.com/badge/sigs.k8s.io/kueue
[GoReport Status]: https://goreportcard.com/report/sigs.k8s.io/kueue

<img src="https://github.com/kubernetes-sigs/kueue/blob/main/site/static/images/logo.svg" width="100" alt="kueue logo">

Kueue is a set of APIs and controller for [job](https://kueue.sigs.k8s.io/docs/concepts/workload)
[queueing](https://kueue.sigs.k8s.io/docs/concepts#queueing). It is a job-level manager that decides when
a job should be [admitted](https://kueue.sigs.k8s.io/docs/concepts#admission) to start (as in pods can be
created) and when it should stop (as in active pods should be deleted).

Read the [overview](https://kueue.sigs.k8s.io/docs/overview/) and watch the Kueue-related [talks & presentations](https://kueue.sigs.k8s.io/docs/talks_and_presentations/) to learn more.

## Features overview

- **Job management:** Support job queueing based on [priorities](https://kueue.sigs.k8s.io/docs/concepts/workload/#priority) with different [strategies](https://kueue.sigs.k8s.io/docs/concepts/cluster_queue/#queueing-strategy): `StrictFIFO` and `BestEffortFIFO`.
- **Advanced Resource management:** Comprising: [resource flavor fungibility](https://kueue.sigs.k8s.io/docs/concepts/cluster_queue/#flavorfungibility), [Fair Sharing](https://kueue.sigs.k8s.io/docs/concepts/preemption/#fair-sharing), [cohorts](https://kueue.sigs.k8s.io/docs/concepts/cluster_queue/#cohort) and [preemption](https://kueue.sigs.k8s.io/docs/concepts/cluster_queue/#preemption) with a variety of policies between different tenants.
- **Integrations:** Built-in support for popular jobs, e.g. [BatchJob](https://kueue.sigs.k8s.io/docs/tasks/run/jobs/), [Kubeflow training jobs](https://kueue.sigs.k8s.io/docs/tasks/run/kubeflow/), [RayJob](https://kueue.sigs.k8s.io/docs/tasks/run/rayjobs/), [RayCluster](https://kueue.sigs.k8s.io/docs/tasks/run/rayclusters/), [JobSet](https://kueue.sigs.k8s.io/docs/tasks/run/jobsets/),  [plain Pod and Pod Groups](https://kueue.sigs.k8s.io/docs/tasks/run/plain_pods/).
- **System insight:** Build-in [prometheus metrics](https://kueue.sigs.k8s.io/docs/reference/metrics/) to help monitor the state of the system, and on-demand visibility endpoint for [monitoring of pending workloads](https://kueue.sigs.k8s.io/docs/tasks/manage/monitor_pending_workloads/pending_workloads_on_demand/).
- **AdmissionChecks:** A mechanism for internal or external components to influence whether a workload can be [admitted](https://kueue.sigs.k8s.io/docs/concepts/admission_check/).
- **Advanced autoscaling support:** Integration with cluster-autoscaler's [provisioningRequest](https://kueue.sigs.k8s.io/docs/admission-check-controllers/provisioning/#job-using-a-provisioningrequest) via admissionChecks.
- **All-or-nothing with ready Pods:** A timeout-based implementation of [All-or-nothing scheduling](https://kueue.sigs.k8s.io/docs/tasks/manage/setup_wait_for_pods_ready/).
- **Partial admission and dynamic reclaim:** mechanisms to run a job with [reduced parallelism](https://kueue.sigs.k8s.io/docs/tasks/run/jobs/#partial-admission), based on available quota, and to [release](https://kueue.sigs.k8s.io/docs/concepts/workload/#dynamic-reclaim) the quota the pods complete..
- **Mixing training and inference**: Simultaneous management of batch workloads along with serving workloads (such as [Deployments](https://kueue.sigs.k8s.io/docs/tasks/run/deployment/) or [StatefulSets](https://kueue.sigs.k8s.io/docs/tasks/run/statefulset/))
- **Multi-cluster job dispatching:** called [MultiKueue](https://kueue.sigs.k8s.io/docs/concepts/multikueue/), allows to search for capacity and off-load the main cluster.
- **Topology-Aware Scheduling**: Allows to optimize the Pod-to-Pod communication throughput by [scheduling aware of the data-center topology](https://kueue.sigs.k8s.io/docs/concepts/topology_aware_scheduling/).

## Production Readiness status

- ✔️ API version: v1beta1, respecting [Kubernetes Deprecation Policy](https://kubernetes.io/docs/reference/using-api/deprecation-policy/)
- ✔️ Up-to-date [documentation](https://kueue.sigs.k8s.io/docs).
- ✔️ Test Coverage:
  - ✔️ Unit Test [testgrid](https://testgrid.k8s.io/sig-scheduling#periodic-kueue-test-unit-main).
  - ✔️ Integration Test [testgrid](https://testgrid.k8s.io/sig-scheduling#periodic-kueue-test-integration-main)
  - ✔️ Integration MultiKueue Tests [testgrid](https://testgrid.k8s.io/sig-scheduling#periodic-kueue-test-integration-multikueue-main)
  - ✔️ E2E Tests for Kubernetes
    [1.31](https://testgrid.k8s.io/sig-scheduling#periodic-kueue-test-e2e-main-1-31),
    [1.32](https://testgrid.k8s.io/sig-scheduling#periodic-kueue-test-e2e-main-1-32),
    [1.33](https://testgrid.k8s.io/sig-scheduling#periodic-kueue-test-e2e-main-1-33),
    on Kind.
  - ✔️ E2E TAS Test [testgrid](https://testgrid.k8s.io/sig-scheduling#periodic-kueue-test-e2e-tas-main)
  - ✔️ E2E Custom Configs Test [testgrid](https://testgrid.k8s.io/sig-scheduling#periodic-kueue-test-e2e-customconfigs-main)
  - ✔️ E2E Cert Manager Test [testgrid](https://testgrid.k8s.io/sig-scheduling#periodic-kueue-test-e2e-certmanager-main)
  - ✔️ Performance Test [testgrid](https://testgrid.k8s.io/sig-scheduling#periodic-kueue-test-scheduling-perf-main)
- ✔️ Scalability verification via [performance tests](https://github.com/kubernetes-sigs/kueue/tree/main/test/performance).
- ✔️ Monitoring via [metrics](https://kueue.sigs.k8s.io/docs/reference/metrics).
- ✔️ Security: RBAC based accessibility.
- ✔️ Stable [release](RELEASE.md) cycle (2-3 months).
- ✔️ [Adopters](https://kueue.sigs.k8s.io/docs/adopters/) running on production.

  _Based on community feedback, we continue to simplify and evolve the API to
  address new use cases_.

## Installation

**Requires Kubernetes 1.29 or newer**.

To install the latest release of Kueue in your cluster, run the following command:

```shell
kubectl apply --server-side -f https://github.com/kubernetes-sigs/kueue/releases/download/v0.13.2/manifests.yaml
```

The controller runs in the `kueue-system` namespace.

Read the [installation guide](https://kueue.sigs.k8s.io/docs/installation/) to learn more.

## Usage

A minimal configuration can be set by running the [examples](site/static/examples):

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

## Roadmap

High-level overview of the main priorities for 2025:
- Improve user experience for [MultiKueue](https://kueue.sigs.k8s.io/docs/concepts/multikueue/) - multi-cluster Job dispatching, in particular:
  * sequential attempts to try worker clusters [#3757](https://github.com/kubernetes-sigs/kueue/issues/3757)
  * log retrieval from worker clusters [3526](https://github.com/kubernetes-sigs/kueue/issues/3526)
- Improve user experience for [Topology Aware Scheduling](https://kueue.sigs.k8s.io/docs/concepts/topology_aware_scheduling/), in particular:
  * make Topology Aware Scheduling compatible with cohorts and preemption [#3761](https://github.com/kubernetes-sigs/kueue/issues/3761)
  * optimize the algorithm to minimize fragmentation [#3756](https://github.com/kubernetes-sigs/kueue/issues/3756)
  * better accuracy of scheduling by tighter integration with kube-scheduler [#3755](https://github.com/kubernetes-sigs/kueue/issues/3755)
  * reduce friction by defaulting the PodSet annotations [#3754](https://github.com/kubernetes-sigs/kueue/issues/3754)
- Productization of the Kueue dashboard [#940](https://github.com/kubernetes-sigs/kueue/issues/940)
- Support Hierarchical Cohorts with FairSharing [#3759](https://github.com/kubernetes-sigs/kueue/issues/3759)
- Improved support for AI inference, including:
  * partial preemption of serving workloads [#3762](https://github.com/kubernetes-sigs/kueue/issues/3762)
  * LeaderWorkerSet support [#3232](https://github.com/kubernetes-sigs/kueue/issues/3232)
- Progress towards the stable API (v1beta2) [#768](https://github.com/kubernetes-sigs/kueue/issues/768)

Long-term aspirational goals:
- Integration with workflow frameworks [#74](https://github.com/kubernetes-sigs/kueue/issues/74)
- Support dynamically-sized Jobs [#77](https://github.com/kubernetes-sigs/kueue/issues/77)
- Budget support [#28](https://github.com/kubernetes-sigs/kueue/issues/28)
- Flavor assignment strategies, e.g. _minimizing cost_ vs _minimizing borrowing_ [#312](https://github.com/kubernetes-sigs/kueue/issues/312)
- Cooperative preemption support for workloads that implement checkpointing [#477](https://github.com/kubernetes-sigs/kueue/issues/477)
- Delayed preemption for two-stage admission [#3758](https://github.com/kubernetes-sigs/kueue/issues/3758)
- Support Structured Parameters (DRA) in Kueue [#2941](https://github.com/kubernetes-sigs/kueue/issues/2941)
- Graduate the API to v1 [#3476](https://github.com/kubernetes-sigs/kueue/issues/3476)

## Community, discussion, contribution, and support

Learn how to engage with the Kubernetes community on the [community page](http://kubernetes.io/community/)
and the [contributor's guide](CONTRIBUTING.md).

You can reach the maintainers of this project at:

- [Slack](https://kubernetes.slack.com/messages/wg-batch)
- [Mailing List](https://groups.google.com/a/kubernetes.io/g/wg-batch)

### Graphic assets

- [Kueue](https://github.com/cncf/artwork/tree/main/projects/kubernetes/sub-projects/kueue)
- [KueueViz](https://github.com/cncf/artwork/tree/main/projects/kubernetes/sub-projects/kueueviz)

### Code of conduct

Participation in the Kubernetes community is governed by the [Kubernetes Code of Conduct](code-of-conduct.md).
