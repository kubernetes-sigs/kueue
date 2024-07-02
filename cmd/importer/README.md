# Kueue Importer Tool

A tool able to import existing pods into kueue.

## Cluster setup

The importer should run in a cluster having the Kueue CRDs defined and in which the `kueue-controller-manager` is not running or has the `pod` integration framework disabled. Check Kueue's [installation guide](https://kueue.sigs.k8s.io/docs/installation/) and [Run Plain Pods](https://kueue.sigs.k8s.io/docs/tasks/run_plain_pods/#before-you-begin) for details.

For an import to succeed, all the involved Kueue objects (LocalQueues, ClusterQueues and ResourceFlavors) need to be created in the cluster, the check stage of the importer will check this and enumerate the missing objects.

## Build

From kueue source root run:

 ```bash
make importer-build

 ```

## Usage

The command runs against the systems default kubectl configuration. Check the [kubectl documentation](https://kubernetes.io/docs/tasks/access-application-cluster/configure-access-multiple-clusters/) to learn more about how to Configure Access to Multiple Clusters.

### Check

The importer will perform following checks:

- At least one `namespace` is provided.
- For every Pod a  mapping to a LocalQueue is available.
- The target LocalQueue exists.
- The LocalQueues involved in the import are using an existing ClusterQueue.
- The ClusterQueues involved have at least one ResourceGroup using an existing ResourceFlavor. This ResourceFlavor is used when the importer creates the admission for the created workloads.

There are two ways the mapping from a pod to a LocalQueue can be specified:

#### Simple mapping

It's done by specifying a label name and any number of <label-value>=<localQueue-name> a command line arguments e.g.  `--queuelabel=src.lbl --queuemapping=src-val=user-queue,src-val2=user-queue2`.

#### Advanced mapping

It's done providing a yaml mapping file name as `--queuemapping-file` argument, it's expected content being:

```yaml
- match:
    labels:
      src.lbl: src-val
  toLocalQueue: user-queue
- match:
    priorityClassName: p-class
    labels:
      src.lbl: src-val2
      src2.lbl: src2-val
  toLocalQueue: user-queue2
- match:
    labels:
      src.lbl: src-val3
  skip: true
```

- During the mapping, if the match rule has no `priorityClassName` the `priorityClassName` of the pod will be ignored, if more than one `label: value` pairs are provided, all of them should match.
- The rules are evaluated in order.
- `skip: true` can be used to ignore the pods matching a rule.

#### Other flags

After building the executable the full list of supported flags can be retrieved by:

```bash
./bin/importer import help
```
Sample output:

```bash
Usage:
  importer import [flags]

Flags:
      --add-labels stringToString     additional label=value pairs to be added to the imported pods and created workloads (default [])
      --burst int                     client Burst, as described in https://kubernetes.io/docs/reference/config-api/apiserver-eventratelimit.v1alpha1/#eventratelimit-admission-k8s-io-v1alpha1-Limit (default 50)
  -c, --concurrent-workers uint       number of concurrent import workers (default 8)
      --dry-run                       don't import, check the config only (default true)
  -h, --help                          help for import
  -n, --namespace strings             target namespaces (at least one should be provided)
      --qps float32                   client QPS, as described in https://kubernetes.io/docs/reference/config-api/apiserver-eventratelimit.v1alpha1/#eventratelimit-admission-k8s-io-v1alpha1-Limit (default 50)
      --queuelabel string             label used to identify the target local queue
      --queuemapping stringToString   mapping from "queuelabel" label values to local queue names (default [])
      --queuemapping-file string      yaml file containing extra mappings from "queuelabel" label values to local queue names

Global Flags:
  -v, --verbose count   verbosity (specify multiple times to increase the log level)

```

- At least one `namespace` needs to be specified
- `queuelabel` and `queuemapping` should always be used together.
- One and only one of `queuemapping` and `queuemapping-file` should always be provided.


### Import

When running the importer, if `--dry-run=false` was specified, for each selected Pod the importer will:

- Update the Pod's Kueue related labels.
- Create a Workload associated with the Pod.
- Admit the Workload.

### Example

#### Simple mapping

```bash
./bin/importer import -n ns1,ns2 --queuelabel=src.lbl --queuemapping=src-val=user-queue,src-val2=user-queue2 --dry-run=false
```

 Will import all the pods in namespace `ns1` or `ns2` having the label `src.lbl` set in LocalQueues `user-queue` or `user-queue2` depending on `src.lbl` value.

#### Advanced mapping

 With mapping file:

```yaml
- match:
    labels:
      src.lbl: src-val
  toLocalQueue: user-queue
- match:
    priorityClassName: p-class
    labels:
      src.lbl: src-val2
      src2.lbl: src2-val
  toLocalQueue: user-queue2
```

```bash
./bin/importer import -n ns1,ns2  --queuemapping-file=<mapping-file-path> --dry-run=false
```

 Will import all the pods in namespace `ns1` or `ns2` having the label `src.lbl` set to `src-val` in LocalQueue `user-queue` regardless of their priorityClassName and those with `src.lbl==src-val2` ,`src2.lbl==src2-val` and `priorityClassName==p-class`in `user-queue2`.

### Run in cluster

`cmd/importer/run-in-cluster` provides the necessary kustomize manifests needed to run the importer from within the cluster.

In order to use the manifests, you should:

1. (Optional) Build the image

You can build a minimal image containing the importer by running the command:

```bash
make importer-image
```

Make the created image accessible by your cluster.


2. Set the image to use

```bash
(cd cmd/importer/run-in-cluster && kustomize edit set image importer=<image:tag>)
```
You can use the image that you built in step one or one of images published in
https://us-central1-docker.pkg.dev/k8s-staging-images/kueue/importer, for example:
`us-central1-docker.pkg.dev/k8s-staging-images/kueue/importer:main-latest`

3. Update the importer args in `cmd/importer/run-in-cluster/importer.yaml` as needed.

Note: `dry-run` is set to `false` by default.

4. Update the mapping configuration in `cmd/importer/run-in-cluster/mapping.yaml`

5. (Optional) Check your config by dry-running it locally e.g.
```bash
./bin/importer --dry-run=true <your-custom-flags> --queuemapping-file=cmd/importer/run-in-cluster/mapping.yaml
```

6. Deploy the configuration:

```bash
 kubectl apply -k cmd/importer/run-in-cluster/
```

And check the logs

```yaml
kubectl -n kueue-importer logs kueue-importer -f
```
