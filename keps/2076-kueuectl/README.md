# KEP-2076: Kueuectl

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories (Optional)](#user-stories-optional)
    - [Story 1](#story-1)
    - [Story 2](#story-2)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Create ClusterQueue](#create-clusterqueue)
  - [Create LocalQueue](#create-localqueue)
  - [Create ResourceFlavor](#create-resourceflavor)
  - [List ClusterQueue](#list-clusterqueue)
  - [List LocalQueue](#list-localqueue)
  - [List Workloads](#list-workloads)
  - [List ResourceFlavors](#list-resourceflavors)
  - [Stop ClusterQueue](#stop-clusterqueue)
  - [Resume ClusterQueue](#resume-clusterqueue)
  - [Stop LocalQueue](#stop-localqueue)
  - [Resume LocalQueue](#resume-localqueue)
  - [Stop Workload](#stop-workload)
  - [Resume Workload](#resume-workload)
  - [Pass-through](#pass-through)
  - [Delete Workload](#delete-workload)
  - [Test Plan](#test-plan)
      - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

We want to create a command line tool for Kueue that allows to:

* list Kueue's objects with easy to use Kueue-specific filtering,
* create Local and ClusterQueues without writing yamls,
* perform management operations on LQs, CQs, Workloads, ResourceFlavors and other Kueue objects.

## Motivation

Currently many administrative operations around Kueue are largely inconvenient. 
They require full API understanding, are relatively error-prone or are simply
tedious without writing a custom mini script or complex pipe processing.

### Goals

* Provide a command line tool for system administrator to:

    * Create ClusterQueues and LocalQueues.
    * Listing Queues and Workloads that meet certain criteria.
    * Stopping and resuming execution in ClusterQueues and LocalQueues.
    * Stopping and resuming individual Workloads.
    * (In the future) Migrating workloads between LocalQueues and other advanced operations
    
* Build it on top of kubectl (as a [kubectl plugin](https://kubernetes.io/docs/tasks/extend-kubectl/kubectl-plugins/)) to reuse all of
the authentication/cluster selection methods.

* Expose the plugin in Krew.

### Non-Goals

* Provide any other interface than a command line (no web ui).
* Provide a tool targeted at ml researchers to make running jobs on Kubernetes easier.
* Expose additional metrics or statuses.

## Proposal

Create kueue kubectl plugin with a set of listing and management commands in form of:

```
kubectl kueue <command> <object> <flags>
```

Additionally provide a wrapper script to allow shorter syntax like:

```
kueuectl <command> <object> <flags>
```

The commands automatically submit all changes unless `--dry-run` option is given - in that 
case the tool will print out yamls without making any changes in the cluster (we may
submit the yaml with dryrun enabled to the APIServer to get validation error).

### User Stories (Optional)

#### Story 1

I want to stop admission of new Workloads on a specific ClusterQueue but allow the already
running Workloads to complete without manually editing the CQ's definition.

#### Story 2

I want to create a LocalQueue pointing to a specific CQ without creating a one-time yaml.

### Risks and Mitigations

* There will be an additional binary placed on sysadmin's machine, whose version will
have to be keep in sync.

## Design Details

The following commands will be provided within the plugin:

### Create ClusterQueue 

Creates a ClusterQueue with the given name, cohort, specified quota and other details.
Format:

```
kueuectl create cq|clusterqueue cqname 
–-cohort=cohortname                        # defaults to "" - no cohort

--queuing-strategy=strategy                # defaults to BestEffortFIFO
--namespace-selector=selector              # defaults to {} - all namespaces can use the queue
--reclaim-within-cohort=policy             # defaults to Never
--preemption-within-cluster-queue = policy # defaults to Never

–-nominal-quota="rfname1:resource1=value;resource2=value;resource3=value,rfname2:resource3=value;resource4=value"
–-borrowing-limit="rfname1:resource1=value;resource2=value;resource3=value,rfname2:resource3=value;resource4=value"
–-lending-limit="rfname1:resource1=value;resource2=value;resource3=value,rfname2:resource3=value;resource4=value"
```

It is possible to create a ClusterQueue with multiple resource flavors/FlavorQuotas inside
a ResourceGroup and multiple ResourceGroups covering different sets of resources.
The command will create the appropriate resource groups with resource flavors 
in the order they appear in the command line. If two settings have at least 
one common resource quota specified, they will land in the same ResourceGroup.

Output: 
A simple confirmation, like in regular kubectl create, 
`clusterqueue.kueue.x-k8s.io/xxxxx created`


### Create LocalQueue

Creates a LocalQueue with the given name pointing to specified ClusterQueue.
The command validates that the target ClusterQueue exists and 
its namespace selector matches to LocalQueue's namespace.

Format:

```
kueuectl create lq|localqueue lqname
–-namespace=namespace                  # uses context's default namespace if not specified
–-clusterqueue=cqname
--ignore-unknown-cq
```

Output:  
A simple confirmation `localqueue.kueue.x-k8s.io/xxxxx created`

### Create ResourceFlavor

Creates a ResourceFlavor with the given name.

Format:

```
kueuectl create rf|resourceflavor rfname
--node-labels key1=value1,key2=value2
--node-taints key1=value:NoSchedule,key2:NoExecute
--tolerations key1=value:NoSchedule,key2:NoExecute,key3=value,key4,:PreferNoSchedule
```

Output:  
A simple confirmation `resourceflavor.kueue.x-k8s.io/xxxxx created`

### List ClusterQueue

List all ClusterQueues, potentially limiting output to those that are active/inactive and 
matching the label selector. Format:

```
kueuectl list cq|clusterqueue(s)
--active=*|true|false
--selector=selector                   # label selector
```

Output columns:

* Name
* Cohort
* Pending Workloads
* Admitted Workloads
* Status (active or not)
* Age

### List LocalQueue 

Lists LocalQueues that match the given criteria: point to a specific CQ, 
being active/inactive, belonging to the specified namespace or matching the label
selector.
Format: 

```
kueuectl list lq|localqueue(s) 
–-namespace=ns                        # uses context's default namespaces if not specified
--all-namespaces | -A
-–clusterqueue=clusterqueue 
–-active=*|true|false
--selector=selector                   # label selector
```

Outputs columns:

* Namespace (if -A is used)
* Name
* ClusterQueue
* Pending Workloads 
* Admitted Workloads 
* Status (active or not)
* Age

### List Workloads

Lists Workloads that match the provided criteria. Format:

```
kueuectl list workloads 
--namespace=ns                        # uses context's default namespace if not specified
--all-namespaces | -A
--clusterqueue=cq 
-–localqueue=lq 
-—status=all|pending|quotareserved|admitted|finished
--selector=selector
```

Output:

* Namespace (if -A is used)
* Workload name
* Job Type (truncated to 10 chars)
* Job Name
* LocalQueue
* ClusterQueue
* Status
* Position in Queue (if Pending)
* Age

### List ResourceFlavors

Lists ResourceFlavors. Format:

```
kueuectl list resourceflavors 
```

Output:

* Name
* Node Labels
* Age

### Stop ClusterQueue

Stops admission and execution inside the specified ClusterQueue, possibly
limiting the action only to the selected ResourceFlavor.
Format:

```
kueuectl stop clusterqueue|cq cqname
--keep-already-running
--resource-flavor=rfnam               # Requires additional API.
```

Output:
None.

### Resume ClusterQueue

Resumes admission inside the specified ClusterQueue.
Format:
```
kueuectl resume clusterqueue|cq cqname 
-–resource-flavor=rfname              # Requires additional API.
```
Output:
None.

### Stop LocalQueue

Stops execution (or just admission) of Workloads coming from the given LocalQueue. 
This requires adding StopPolicy to LocalQueue and enforcing its changes in ClusterQueue (#2109).
Format:
```
kueuectl stop localqueue|lq lqname
--keep-already-running
```
Output:
None.


### Resume LocalQueue

Resumes admission of Workloads coming from the given LocalQueue.
Format:
```
kueuectl resume localqueue|lq lqname 
```
Output:
None.

### Stop Workload

Puts the given Workload on hold. The Workload will not be admitted and 
if it is already admitted it will be put back to queue just as if it was preempted
(using `.spec.active` field).

Format:
```
kueuectl stop workload name --namespace=ns
```
Output:
None.


### Resume Workload

Resumes the Workload, allowing its admission according to regular ClusterQueue rules.
Format:
```
kueuectl resume workload name --namespace=ns
```
Output:
None.

### Pass-through

For completeness there will be 5 additional commands that will simply execute regular kubectl
so that the users won't have to remember to switch the command to kubectl.

* `get workload|wl|clusterqueue|cq|localqueue|lq|resourceflavor|rf`
* `describe workload|wl|clusterqueue|cq|localqueue|lq|resourceflavor|rf`
* `edit workload|wl|clusterqueue|cq|localqueue|lq|resourceflavor|rf`
* `patch workload|wl|clusterqueue|cq|localqueue|lq|resourceflavor|rf`
* `delete clusterqueue|cq|localqueue|lq|resourceflavor|rf`

### Delete Workload

Motivation:
The pass-through delete workload command deletes the Workload API object, which 
is later recreated leading to its job to be re-queued. This can confuse users, 
as the recreated Workload has the same name giving the impression that the 
`delete workload` command did not succeed.

This command will delete the corresponding Workload Job(s), and then the Workload
will be asynchronously deleted using Kueue. If the Workload has associated Jobs,
the command will prompt for deletion approval and display the Jobs that will be
affected. If there are no associated Jobs, the command will proceed to delete
the Workload directly.

Format:
```
kueuectl delete workload name
```

Flags:
```
-n, --namespace       The namespace from which to delete the Workload and its corresponding Job. 
-y, --yes             Automatic yes to the prompt for deleting the Workload.
    --all             Delete all Workloads, in the specified namespace.
    --all-namespaces  Using with --all flag. Delete all Workloads, in all namespaces.
```

Approval Output:
```
This operation will also delete:
  - jobs.batch/name associated with the namespace/name workload
Do you want to proceed (y/n)?
```

Deletion Process Output:
```
jobs.batch/name deleted
```

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

##### Prerequisite testing updates

#### Unit Tests

Regular unit tests for the commands will be provided. 

#### Integration tests

Integration/E2E tests will be provided for all of the commands.

### Graduation Criteria

Beta:
* Positive feedback from users.
* All bugs and issues fixed.

GA/Stable:
* Positive feedback from users
* No request for column/flags changes for 0.5 year.

## Implementation History

KEP: 2023-04-27.

## Alternatives

* Use existing kubectl functionality and perform management operations via
API manipulations.
* Don't use kubectl plugins but write CLI from scratch.
