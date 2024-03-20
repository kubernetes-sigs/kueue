# Kueue Importer Tool

A tool able to import existing pods into kueue.

## Cluster setup.

The importer should run in a cluster having the Kueue CRDs defined and in which the `kueue-controller-manager` is not running or has the `pod` integration disabled.

For an import to succeed, all the involved Kueue objects (LocalQueues, ClusterQueues and ResourceFlavors) need to be created in the cluster, the check stage of the importer will check this and enumerate the missing objects. 

## Build

From kueue source root run:
 ```bash
go build  -C cmd/importer/ -o $(pwd)/bin/importer

 ```

## Usage

The command runs against the systems default kubectl configuration. Check the [kubectl documentation](https://kubernetes.io/docs/tasks/access-application-cluster/configure-access-multiple-clusters/) to learn more about how to Configure Access to Multiple Clusters.

The will perform following checks:

- At least one `namespace` is provided.
- The label key (`queuelabel`) providing the queue mapping is provided.
- A mapping from ane of the encountered `queuelabel` values to an existing LocalQueue exists.
- The LocalQueues involved in the import are using an existing ClusterQueue.
- The ClusterQueues involved have at least one ResourceGroup using an existing ResourceFlavor. This ResourceFlavor is used when the importer creates the admission for the created workloads.

After which, if `--dry-run=false` was specified, for each selected Pod the importer will:

- Update the Pod's Kueue related labels.
- Create a Workload associated with the Pod.
- Admit the Workload.

### Example

```bash
./bin/importer import -n ns1,ns2 --queuelabel=src.lbl --queuemapping=src-val=user-queue,src-val=user-queue --dry-run=false
```
 Will import all the pods in namespace `ns1` or `ns2` having the label `src.lbl` set.
