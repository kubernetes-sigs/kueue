# Kueue Importer Tool

A tool able to import existing pods into kueue.

## Cluster setup.

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

The are two ways the mapping from a pod to a LocalQueue can be specified:

#### Simple mapping

It's done by specifying a label name and any number of <label-value>=<localQueue-name> a command line arguments eg.  `--queuelabel=src.lbl --queuemapping=src-val=user-queue,src-val2=user-queue2`.

#### Advanced mapping

It's done providing an yaml mapping file name as `--queuemapping-file` argument, it's expected content being:

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

### Import
After which, if `--dry-run=false` was specified, for each selected Pod the importer will:

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


#### Run in cluster

`cmd/importer/run-in-cluster` provides the necessary kustomize manifests needed to run the importer from within the cluster, In order to use them you should:

1. Update the used image

A minimal image containing the importer can be built by

```bash
make importer-image
```

Make the created image accessible by your cluster.

Note: Importer images will be available in `gcr.io/k8s-staging-kueue/importer` soon.

And run
```bash
(cd cmd/importer/run-in-cluster && kustomize edit set image importer=<image:tag>)
```

2. Updated the importer args in `cmd/importer/run-in-cluster/importer.yaml`
3. Update the mapping configuration in `cmd/importer/run-in-cluster/mapping.yaml`
4. Deploy the configuration:

```bash
 kubectl apply -k cmd/importer/run-in-cluster/
```

And check  the logs

```yaml
kubectl -n kueue-importer logs kueue-importer -f
```
