# Resource Reservation Exceeds Quota

## Severity: Info

## Impact

Knowledge of over requested resources will allow the user to adjust the nominal quota or resources requested by a workload.

## Summary

This alert is triggered when resource reservation is 10 times the available nominal quota in a cluster queue.

## Steps

1. Check current resource reservation for the cluster queue and ensure that the nominal quota for the resource in question is correctly configured. Update the cluster-queue-name in the script below to describe the cluster queue.
```bash
cluster_queue=< cluster-queue-name >
oc describe clusterqueue $cluster_queue
```

 - If you would just like to view the Flavors Reservation and Flavors Usage you can use the following command:
```bash
oc describe clusterqueue $cluster_queue | awk '/Flavors Reservation:/,/^$/' 
```

2. Review the workloads that are linked with the cluster queue to see if the requested resources are required. 
```bash
# Find local queues linked to the cluster queue
local_queues=$(oc get localqueues --all-namespaces -o json | jq -r --arg clusterQueue "$cluster_queue" '.items[] | select(.spec.clusterQueue == $clusterQueue) | "\(.metadata.namespace)/\(.metadata.name)"')

# Find workloads linked to the local queues
for local_queue in $local_queues; do
  namespace=$(echo $local_queue | cut -d '/' -f 1)
  queue_name=$(echo $local_queue | cut -d '/' -f 2)

  echo "Checking workloads linked to local queue $queue_name in namespace $namespace..."

  oc get workloads --namespace $namespace -o json | jq -r --arg queueName "$queue_name" '.items[] | select(.spec.queueName == $queueName) | "\(.metadata.namespace)/\(.metadata.name)"'
done
```

3. Review individual workloads. Update the namespace and workload-name in the script below to view details of the workload.
```bash
namespace=< namespace >
workload_name=< workload-name >
oc describe workload -n $namespace $workload_name
```

4. Consider increasing the cluster queue nominal quota. 
You can patch the clusterqueue using the following command. Note that you must change the values to refer to the exact resource you want to change. 
This will change the nominal quota for cpu to 10, in the first flavor referenced in the named cluster queue resource:
```bash
oc patch clusterqueue $cluster_queue --type='json' -p='[{"op": "replace", "path": "/spec/resourceGroups/0/flavors/0/resources/0/nominalQuota", "value": "10"}]'
```

5. Alternatively consider altering the resources requested in the pending workloads, if possible.
