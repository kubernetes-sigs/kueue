# Kueue

Kueue is a set of APIs and controller for [job](docs/concepts/queued_workload.md)
[queueing](docs/concepts/README.md#queueing). It is a job-level manager that
decides when a job should be [admitted](docs/concepts/README.md#admission) to start
(as in pods can be created) and when it should stop (as in active pods should be 
deleted).

The main design principle for Kueue is to avoid duplicating mature functionality
in [Kubernetes components](https://kubernetes.io/docs/concepts/overview/components/)
and well-established third-party controllers. Autoscaling, pod-to-node scheduling and
job lifecycle management are the responsibility of cluster-autoscaler,
kube-scheduler and kube-controller-manager, respectively. Advanced
admission control can be delegated to controllers such as [gatekeeper](https://github.com/open-policy-agent/gatekeeper).

<!-- TODO(#64) Remove links to google docs once the contents have been migrated to this repo -->
[bit.ly/kueue-apis](https://bit.ly/kueue-apis) (please join the [mailing list](https://groups.google.com/a/kubernetes.io/g/wg-batch)
to get access) discusses the API proposal and a high-level description of how it
operates; while [bit.ly/kueue-controller-design](https://bit.ly/kueue-controller-design)
presents the detailed design of the controller.

## Usage

**Requires k8s 1.22 or newer**

You can run Kueue with the following command:

```sh
IMAGE_REGISTRY=registry.example.com/my-user make image-build image-push deploy
```

The controller will run in the `kueue-system` namespace.
Then, you can and apply some of the [samples](config/samples):

```
kubectl apply -f config/samples/minimal.yaml
kubectl create -f config/samples/sample-job.yaml
```

## Community, discussion, contribution, and support

Learn how to engage with the Kubernetes community on the [community page](http://kubernetes.io/community/).

You can reach the maintainers of this project at:

- [Slack](https://kubernetes.slack.com/messages/wg-batch)
- [Mailing List](https://groups.google.com/a/kubernetes.io/g/wg-batch)

### Code of conduct

Participation in the Kubernetes community is governed by the [Kubernetes Code of Conduct](code-of-conduct.md).
