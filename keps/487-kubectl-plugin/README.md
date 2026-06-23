# KEP-487: Kubectl plugin for listing objects

<!--
This is the title of your KEP. Keep it short, simple, and descriptive. A good
title can help communicate what the KEP is and should be considered as part of
any review.
-->

<!--
A table of contents is helpful for quickly jumping to sections of a KEP and for
highlighting any additional information provided beyond the standard KEP
template.

Ensure the TOC is wrapped with
  <code>&lt;!-- toc --&rt;&lt;!-- /toc --&rt;</code>
tags, and then generate with `hack/update-toc.sh`.
-->

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories (Optional)](#user-stories-optional)
    - [Story 1](#story-1)
    - [Story 2](#story-2)
  - [Notes/Constraints/Caveats (Optional)](#notesconstraintscaveats-optional)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Summary](#summary-1)
  - [Version](#version)
  - [Listing Jobs](#listing-jobs)
  - [Listing Workloads](#listing-workloads)
  - [Describe Workloads](#describe-workloads)
  - [Watch Workload](#watch-workload)
  - [Queues](#queues)
  - [Possible command extensions](#possible-command-extensions)
    - [Cancel Workloads](#cancel-workloads)
    - [Submit](#submit)
    - [Update](#update)
  - [Resources](#resources)
  - [Test Plan](#test-plan)
      - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

We would like to develop a Kubectl plugin for Kueue that serves as a command-line workload management tool.
It should be able to manage and list workloads and ClusterQueues with comprehensive information.


<!--
This section is incredibly important for producing high-quality, user-focused
documentation such as release notes or a development roadmap. It should be
possible to collect this information before implementation begins, in order to
avoid requiring implementors to split their attention between writing release
notes and implementing the feature itself. KEP editors and SIG Docs
should help to ensure that the tone and content of the `Summary` section is
useful for a wide audience.

A good summary is probably at least a paragraph in length.

Both in this section and below, follow the guidelines of the [documentation
style guide]. In particular, wrap lines to a reasonable length, to make it
easier for reviewers to cite specific portions, and to minimize diff churn on
updates.

[documentation style guide]: https://github.com/kubernetes/community/blob/master/contributors/guide/style-guide.md
-->

## Motivation

The basic information provided by `kubectl get` does include details such as whether a workload is admitted or why it's pending. 
As this information is available in compound objects, we need to perform different queries to the API to get it. 
Similarly, `kubectl` cannot tell the status of a ClusterQueue that is misconfigured, nor does it
provide details of an accompanying Workload listed alongside a job. We want to expose this information
to the user, and in a way that feels comfortable and familiar.


<!--
This section is for explicitly listing the motivation, goals, and non-goals of
this KEP.  Describe why the change is important and the benefits to users. The
motivation section can optionally provide links to [experience reports] to
demonstrate the interest in a KEP within the wider Kubernetes community.

[experience reports]: https://github.com/golang/go/wiki/ExperienceReports
-->

### Goals

A successful plugin should be able to answer the following questions:

1. What workloads are there in the user-queue?
2. What workloads are there in this specific namespace?
3. What workloads are there in this specific state?
4. Why is my workload pending?
5. Was there an error admitting my workload?
6. Is a ClusterQueue misconfigured or some other issue? (this is more of an admin command?)
7. All of the above, but instead of a table I want json/yaml

And for this scoped work, the plugin should be added to the [krew plugins package manager](https://krew.sigs.k8s.io/plugins/).

<!--
List the specific goals of the KEP. What is it trying to achieve? How will we
know that this has succeeded?
-->

### Non-Goals

- Serve as a duplicate implementation of information available via kubectl already
- Streaming logs

<!--
What is out of scope for this KEP? Listing non-goals helps to focus discussion
and make progress.
-->

## Proposal

We propose creating a command-line tool that can serve as a Kubectl plugin that exposes this missing information. We also propose using a design strategy that mimics existing tools that are available for other workload managers across HPC and cloud that users are comfortable with to ease adoption of both the tool and approach of submitting workloads to Kubernetes. The main command-line interactions will take the following shape:

```bash
# As a Kubectl plugin
kubectl kueue <options> <command< <args>

# As a standalone command-line tool
kueue <options> <command> <args>
```

For comparison, the Armada team developed a [general client](https://github.com/armadaproject/armada/tree/master/cmd/armadactl) that uses this strategy to be renamed and fold into kubectl. This is an ideal approach because it presents an interface to kueue to make it feel more like a standalone job manager (and not force folks to live in Kubernetes abstractions, at least entirely). 


<!--
This is where we get down to the specifics of what the proposal actually is.
This should have enough detail that reviewers can understand exactly what
you're proposing, but should not include things like API designs or
implementation. What is the desired outcome and how do we measure success?.
The "Design Details" section below is for the real
nitty-gritty.
-->

### User Stories (Optional)

<!--
Detail the things that people will be able to do if this KEP is implemented.
Include as much detail as possible so that people can understand the "how" of
the system. The goal here is to make this feel real for users without getting
bogged down.
-->

#### Story 1

As a user submitting workloads, I want to easily see the status of an entire Workload,
or check on why my workload is not being admitted. I can do this with the new proposed plugin.

```bash
kueue describe taco-123
```

#### Story 2

As a user coming from High Performance Computing (HPC), I am not comfortable with using
`kubectl` and don't want to learn an entirely new means to interact with workloads.
The proposed plugin makes the transition much easier for me. The example below
compares the proposed list command for this plugin against popular HPC resource
managers.

```bash
# Kubernetes Kueue
kueue workloads <queue-name>

# SLURM
squeue --partition <queue-name>

# Flux Framework
flux jobs -a --queue=<queue-name>

# HTCondor
condor_q
```

While this is just a sampling, the high level idea is that high performance users
are comfortable and accustomed to having command line tools to list and otherwise
interact with jobs. Having a similar tool for Kubernetes, and specifically
submitting jobs, will help to span the space.

### Notes/Constraints/Caveats (Optional)

<!--
What are the caveats to the proposal?
What are some important details that didn't come across above?
Go in to as much detail as necessary here.
This might be a good place to talk about core concepts and how they relate.
-->

### Risks and Mitigations

Maintaining the plugin in-sync with the latest version of Kueue will add additional
maintainer responsibilities.

<!--
What are the risks of this proposal, and how do we mitigate? Think broadly.
For example, consider both security and how this will impact the larger
Kubernetes ecosystem.

How will security be reviewed, and by whom?

How will UX be reviewed, and by whom?

Consider including folks who also work outside the SIG or subproject.
-->

## Design Details

<!--
This section should contain enough information that the specifics of your
change are understandable. This may include API specs (though not always
required) or even code snippets. If there's any ambiguity about HOW your
proposal will be implemented, this is the place to discuss them.
-->

### Summary

Kueuectl is a command-line tool for interacting with Kueue, and can serve as a standalone tool or be renamed/installed to act as a kubectl plugin. Using `kueuectl` a user can manage workloads. An alternative (shorter and easier to type name) might also be `kueue`, which I'll use for the remainder of this document. Thus, the main client takes the following format:

```bash
kueue [subcommand] [flags]
```

As a kubectl plugin, we would install it named as `kubectl-kueue` and then the interaction would be:

```bash
kubectl kueue [subcommand] [flags]
```

We can start with basic query of workload metadata and status, and move toward a tool that can further create / delete or otherwise interact with workloads. This design document will proceed with proposed interactions and example tables, which would be printed in the terminal.


### Version

```bash
kueue version
```

### Listing Jobs

A user may be interested to list workloads based on job name. For this need, an API group and kind would be needed.
The following should both work to produce the same result:

```bash
kueue jobs --kind MPIJob <name>
kueue jobs --kind kubeflow.org/MPIJob <name>
```

| Namespace | Name     | API Group         | Kind        | Command  | Pods | Time   |     State |
|-----------|----------|-------------------|-------------|----------|------|--------|-----------|
| insects   | ant-123  | kubeflow.org      | MPIJob      | echo     | 2    | 4.323s | Completed | 


### Listing Workloads

A user might also want to see "all" workloads. We will need to decide if "all" means all namespaces, or (akin to `kubectl` just those in default. 

```bash
kueue workloads
```

| Namespace | Name     | API Group         | Kind        | Pods | Time   |     State | Queue      |
|---------- |------------------------------|-------------|------|--------|-----------|------------|
| default   | taco-345 | flux-framework.org| MiniCluster | 3    | 2.322s |   Running | user-queue |
| default   | taco-123 | flux-framework.org| MiniCluster | 2    |        |   Pending | user-queue | 
| insects   | ant-123  | kubeflow.org      | MPIJob      | 2    | 4.323s | Completed | user-queue |

Adding the name of q queue will filter down to it:

```bash
# Generic command
kueue workloads -q <queue-name>

# As an example
kueue workloads -q user-queue
```

| Namespace | Name     | API Group         | Kind        | Pods | Time   |     State |
|-----------|----------|-------------------|-------------|------|--------|-----------|
|   default | taco-345 | flux-framework.org| MiniCluster | 3    | 2.322s |   Running | 
|   default | taco-123 | flux-framework.org| MiniCluster | 2    |        |   Pending | 
|   insects | ant-123  | kubeflow.org      | MPIJob      | 2    | 4.323s | Completed | 

For namespaces, kueue will use the same practices as kubectl, using "default" for a default, and otherwise
expecting a `-n` or `--namespace` argument for a custom namespace.

```bash
kueue workloads --namespace insects
```
If a user wants to set a default namespace, 
this [should be supported akin to kubectl](https://www.cloudytuts.com/tutorials/kubernetes/how-to-set-default-kubernetes-namespace/):

```bash
kueue set default-namespace <namespace>
kueue set default-namespace insects
kueue workloads
```

| Namespace | Name     | API Group         | Kind        | Pods | Time   |     State | Queue |
|-----------|----------|-------------------|-------------|------|--------|-----------|-------|
| inspects  | ant-123  | kubeflow.org      | MPIJob      | 2    | 4.323s | Completed | user-queue |


We can also ask for a specific workload based on a job name. E.g., if I just submit a job and know the name, this would be intuitive to type.

```bash
# kueue workloads <name>
kueue workloads job/taco-123
```

| Namespace| Name      | API Group         | Kind        | Pods | Time   |     State | Queue |
|----------|-----------|-------------------|-------------|------|--------|-----------|-------|
| default  | taco-123  | flux-framework.org| MiniCluster | 2    |        |   Pending | user-queue | 


We likely want to also filter by state (or other attributes, TBA which others?)

```bash
kueue workloads --state Pending
```

| Namespace | Name     | API Group         | Kind        | Pods  | Time   |     State | Queue |
|-----------|----------|-------------------|-------------|-------|--------|-----------|-------|
| default   | taco-123 | flux-framework.org| MiniCluster |  2    |        |   Pending | user-queue | 


### Describe Workloads

Describe is intended to show more detailed information about one or more workloads. Akin to kubectl describe, we would stack them on top of the other. Unlike kubectl, I think we should have the -o json/yaml options here (it never made sense to me that kubectl uses describe for more rich metadata, but those output variables are available with "get" !

```bash
kueue describe taco-123
kueue describe taco-123 taco-345
```
```console
Feature Set A:
  Field A            Field B     Field C
  --------           --------    ------
  cpu                650m (8%)   0 (0%)
  memory             100Mi (0%)  0 (0%)
Events:
  Field A    Field B              Field C    
  ----       -------              ------- ... 
  Normal     Starting             Pancakes.
```

While the describe tables do not include images or commands, the detailed view should.
Note that I likely will develop this when I dig into working on the tool itself, and get a sense of all the attributes available to see about workloads. Right now I'm providing a generic template anticipating that. The above will include all metadata that the workload offers, and the additional features requested in the original prompt for reasons for pending or misconfiguration.

The above should also provide different output formats:

```bash
kueue describe taco-123 -o json
kueue describe taco-123 -o yaml
```

### Watch Workload

After submitting a workload, it's nice to be able to watch / stream logs. That should be easy to do.

```bash
kueue watch taco-123
```
```console
... makin' tacos!
... taco 1 is cooked!
... taco 2 is cooking.
```

Or a user may want to watch or stream events:

kueue watch taco-123 --events
```
```console
<timestamp> <event1>
<timestamp> <event2>
...
```


### Queues

I haven't used queues extensively so this likely needs to be expanded, but akin to listing workloads, we probably want to list kueues. For all queues, this could be:

```bash
kueue queues
```

| Name | Admitted | Pending | State |
|------|-----------|---------|------|
| user-queue | 1 | 1 | Operational |

To filter to a specific queue:

```bash
kueue queues user-queue
```

We likely want to be able to provide more metadata, and either we could add a `describe-queue` subcommand, or have the above second example show the more verbose information. I think I like the first idea. We also likely want to expand these subcommands to include details that the original prompt warranted. I'm not familiar enough with Kueue yet to add them. This command should also support yaml/json.

```bash
kueue queues -o json
```


### Possible command extensions

In the future, if we can make this a full fledged client for interaction with workloads, we could consider the following commands.
Note that these are not proposed to be in the first stage of this design document.

#### Cancel Workloads

A request to cancel would be akin to deleting the CRD. A "cancel" is more intuitive / natural than a delete request for this use case.
This implementation will be tricky because we need to make the request to the underlying controller.


```bash
kueue cancel taco-123
```

It might also be useful to request a cancel all, limited to the permission that the user has, a namespace, or other filter. This likely needs a confirmation.

```bash
kueue cancel --all
> Are you sure you want to cancel all workloads y/n?
```

Or without the prompt:

```bash
kueue cancel --all --force
```

Or within a specific filter:

```bash
kueue cancel --all --namespace insects
```

To support the multi-tenancy use case, filters for each of local-queue, cluster-queue and cohort will be allowed.


#### Submit

```
# Submit, either a yaml as is
kueue submit workload.yaml

# or a simpler abstraction that uses some kind of default or template
# This would actually be really cool if we could map a community developed job or workload spec (that works for other tools) into kueue
kueue submit <something else>
```

#### Update

A workload can be updated by its creator, and this happens via kueue's internals on the level of the workload controller and scheduler.
As an example, here is what updated an attribute on a workload pod might look like:

```bash
# Note this format follows how helm sets variables
kueue update set path.to.attribute=thing

# This does not, but it could be wanted to remove an attribute entirely (instead of trying to set the default null type)
kueue update remove path.to.attribute
```

Different kinds of updates will need to be defined. To allow for an update namespace, we could take either of the following
client approaches:

```bash
kueue update pods path.to.attribute=thing
kueue update-pods path.to.attribute=thing

kueue update cohort ...
kueue update-cohort 

kueue update cluster-queue
kueue update-cluster-queue
```

### Resources

Get resources requested or allocated for a workload (can be used to debug)

```bash
kueue resource taco-123
```

| Name | Quantity |
|------|-----------|
| cpu | 4 |
| memory | 2Gi |

```bash
kueue resource taco-123 -o yaml
```

This subcommand could provide other types of resources - we should consider this!


### Test Plan

<!--
**Note:** *Not required until targeted at a release.*
The goal is to ensure that we don't accept enhancements with inadequate testing.

All code is expected to have adequate tests (eventually with coverage
expectations). Please adhere to the [Kubernetes testing guidelines][testing-guidelines]
when drafting this test plan.

[testing-guidelines]: https://git.k8s.io/community/contributors/devel/sig-testing/testing.md
-->

The kueue plugin should be tested for functionality alongside the current head of
the repository.

[x] I understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

##### Prerequisite testing updates

<!--
Based on reviewers feedback describe what additional tests need to be added prior
implementing this enhancement to ensure the enhancements have also solid foundations.
-->


#### Unit Tests

<!--
In principle every added code should have complete unit test coverage, so providing
the exact set of tests will not bring additional value.
However, if complete unit test coverage is not possible, explain the reason of it
together with explanation why this is acceptable.
-->

<!--
Additionally, try to enumerate the core package you will be touching
to implement this enhancement and provide the current unit coverage for those
in the form of:
- <package>: <date> - <current test coverage>

This can inform certain test coverage improvements that we want to do before
extending the production code to implement this enhancement.
-->

#### Integration tests


<!--
Describe what tests will be added to ensure proper quality of the enhancement.

After the implementation PR is merged, add the names of the tests here.
-->

### Graduation Criteria

<!--

Clearly define what it means for the feature to be implemented and
considered stable.

If the feature you are introducing has high complexity, consider adding graduation
milestones with these graduation criteria:
- [Maturity levels (`alpha`, `beta`, `stable`)][maturity-levels]
- [Feature gate][feature gate] lifecycle
- [Deprecation policy][deprecation-policy]

[feature gate]: https://git.k8s.io/community/contributors/devel/sig-architecture/feature-gates.md
[maturity-levels]: https://git.k8s.io/community/contributors/devel/sig-architecture/api_changes.md#alpha-beta-and-stable-versions
[deprecation-policy]: https://kubernetes.io/docs/reference/using-api/deprecation-policy/
-->

N/A

## Implementation History

<!--
Major milestones in the lifecycle of a KEP should be tracked in this section.
Major milestones might include:
- the `Summary` and `Motivation` sections being merged, signaling SIG acceptance
- the `Proposal` section being merged, signaling agreement on a proposed design
- the date implementation started
- the first Kubernetes release where an initial version of the KEP was available
- the version of Kubernetes where the KEP graduated to general availability
- when the KEP was retired or superseded
-->

## Drawbacks

## Alternatives

<!--
What other approaches did you consider, and why did you rule them out? These do
not need to be as detailed as the proposal, but should include enough
information to express the idea and why it was not acceptable.
-->

Require a user to write their own interactions with Kueue via API.

**Reasons for discarding/deferring**

This is a complex thing to do, and should not be required by a user.
