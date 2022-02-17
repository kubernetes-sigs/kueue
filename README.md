# Kueue

Kueue is a set of APIs and controller for job queueing. It is a job-level manager that decides when 
a job should start (as in pods can be created) and when it should stop (as in active pods should be 
deleted). The main design principle for Kueue is to avoid duplicating existing functionality: autoscaling, 
pod-to-node scheduling, job lifecycle management and advanced admission control are the responsibility of 
core k8s components or commonly accepted frameworks, namely cluster-autoscaler, kube-scheduler and kube-controller-manager 
and gatekeeper, respectively.

[bit.ly/kueue-apis](https://bit.ly/kueue-apis) (please join the [mailing list](https://groups.google.com/a/kubernetes.io/g/wg-batch) to get access) discusses the
API proposal and a high-level description of how it operates; while [bit.ly/kueue-controller-design](https://bit.ly/kueue-controller-design) presents the detailed design of the controller.

## Running

You can run Kueue with the following command:

```sh
IMG=registry.example.com/kueue:latest make docker-build docker-push deploy
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
