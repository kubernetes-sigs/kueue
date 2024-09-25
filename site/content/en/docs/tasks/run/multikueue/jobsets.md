The same `jobset-sample.yaml` file from [single cluster environment](docs/tasks/run/jobsets) can be used in a [MultiKueue environment](#multikueue-environment).
In that setup, the `spec.managedBy` field will be set to `kueue.x-k8s.io/multikueue`
automatically, if not specified, as long as  the `kueue.x-k8s.io/queue-name` annotation
is specified and the corresponding Cluster Queue uses the Multi Kueue admission check.