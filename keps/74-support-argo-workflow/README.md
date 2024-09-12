# KEP-74: Support Argo Workflow

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1](#story-1)
    - [Story 2](#story-2)
- [Design Details](#design-details)
  - [Workflow as An Unit](#workflow-as-an-unit)
    - [Drawback and Limitations](#drawback-and-limitations)
    - [Advantages](#advantages)
  - [Layer as An Unit](#layer-as-an-unit)
    - [Examples](#examples)
      - [Example 1 (ParallelSteps Contains Leaf Template Only)](#example-1-parallelsteps-contains-leaf-template-only)
      - [Example 2 (ParallelSteps Contains Leaf Template and Step)](#example-2-parallelsteps-contains-leaf-template-and-step)
      - [Example 3 (Workflow with Single Container Template)](#example-3-workflow-with-single-container-template)
    - [How to suspend a workflow step by step](#how-to-suspend-a-workflow-step-by-step)
    - [Drawback and Limitations](#drawback-and-limitations-1)
    - [Advantages](#advantages-1)
  - [Plain Pod as An Unit](#plain-pod-as-an-unit)
    - [Drawback and Limitations](#drawback-and-limitations-2)
    - [Advantages](#advantages-2)
- [Additional Details](#additional-details)
  - [Test Plan](#test-plan)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

This KEP outlines the proposal to integrate Argo Workflows within Kueue, discussing the advantages 
and disadvantages of queuing workflows at varying granularity levels, alongside detailing 
the integration methodologies.

## Motivation

Workflows are pivotal components in domains like Scientific Computing and Simulations, where 
administrators often enforce resource usage quotas for different users or departments. Currently, 
Argo Workflows lacks native support within Kueue.

### Goals

- Enable support for Argo Workflow within Kueue, allowing users to simply add a label 
`kueue.x-k8s.io/queue-name` to their workflows and submit them initially in a suspended state.
- Should be easily extended to support other workflow managers.

### Non-Goals



## Proposal


### User Stories

#### Story 1

As a machine learning engineer, I need to preprocess data before executing a training job. My 
workflow includes two steps: data preprocessing (which doesn't require a GPU) followed by a 
PyTorchJob. I desire that the data preprocessing stage proceeds independently of GPU quota 
availability.

#### Story 2

As an ML engineer, my workflow consists of several GPU-dependent stages with uniform resource 
requirements. I aim to recycle resources allocated to earlier workflow stages to boost efficiency 
and resource utilization.

## Design Details

### Workflow as An Unit

Given the diverse resource, node affinity, and toleration requirements among workflow pods, 
determining the necessary resources for each flavor becomes challenging for the controller. 
Users must specify workflow resources via annotations like kueue.k8s.io/max-resources, tolerations 
with kueue.k8s.io/toleration, and node selectors with kueue.k8s.io/node-selector.

#### Drawback and Limitations

- Inability to set distinct nodeSelectors and tolerations for multiple pod sets within a workflow.

#### Advantages

- Simplified architecture facilitating straightforward implementation.

### Layer as An Unit

A workflow's template definition can be a container invocation (leaf template) or a list 
of steps. For workflows composed of a single leaf template, a single workload is generated.

#### Examples 

In the following example, we solely discuss which patterns of workflows should warrant the 
creation of workloads, without delving into the specifics of how these workloads are created, 
nor addressing the division of responsibilities between the workflow-controller and kueue.

##### Example 1 (ParallelSteps Contains Leaf Template Only)
For a parallelStep with only leaf templates, we create a workload for the parallelStep.
In the following example, we create workloads for `loop-example-depth-2(0:depth-1-1)` and 
`loop-example-depth-2(1:depth-1-2)`. Patterns of DAGs are similar, so we do not discuss them 
separately.

```
# kubectl create -f - << EOF
apiVersion: argoproj.io/v1alpha1
kind: Workflow
metadata:
  generateName: loops-
  namespace: argo
spec:
  entrypoint: loop-example-depth-1
  templates:
  - name: loop-example-depth-2
    steps:
    - - name: print-message-loop
        template: print-message
        arguments:
          parameters:
          - name: message
            value: "{{item}}"
        withItems:              # invoke print-message once for each item in parallel
        - hello world           # item 1
        - goodbye world         # item 2
  - name: loop-example-depth-1
    steps:
    - - name: loop-example-depth-2
        template: loop-example-depth-2
        withItems:
        - depth-1-1
        - depth-1-2
  - name: print-message
    inputs:
      parameters:
      - name: message
    container:
      image: busybox
      command: [echo]
      args: ["{{inputs.parameters.message}}"]
EOF
      
# argo get loops-mlr6m
...

STEP                                            TEMPLATE              PODNAME                               DURATION  MESSAGE
 ✔ loops-mlr6m                                  loop-example-depth-1
 └─┬─✔ loop-example-depth-2(0:depth-1-1)        loop-example-depth-2
   │ └─┬─✔ print-message-loop(0:hello world)    print-message         loops-mlr6m-print-message-2545579066  6s
   │   └─✔ print-message-loop(1:goodbye world)  print-message         loops-mlr6m-print-message-323962978   5s
   └─✔ loop-example-depth-2(1:depth-1-2)        loop-example-depth-2
     └─┬─✔ print-message-loop(0:hello world)    print-message         loops-mlr6m-print-message-520674448   4s
       └─✔ print-message-loop(1:goodbye world)  print-message         loops-mlr6m-print-message-2893948292  6s
```

##### Example 2 (ParallelSteps Contains Leaf Template and Step)

For the step composed by a leaf template and another step, we create workload for the 
leaf template. And the workload for the other step is created separately.
In the following example, we will create workload for `loops-644ch` and `loop-example-depth-2-2`.

```
apiVersion: argoproj.io/v1alpha1
kind: Workflow
metadata:
  generateName: loops-
  namespace: argo
spec:
  entrypoint: loop-example-depth-1
  templates:
  - name: loop-example-depth-2
    steps:
    - - name: print-message-loop
        template: print-message
        arguments:
          parameters:
          - name: message
            value: "{{item}}"
        withItems:              # invoke print-message once for each item in parallel
        - depth-2-1           # item 1
        - depth-2-2         # item 2
  - name: loop-example-depth-1
    steps:
    - - name: print-message
        template: print-message
        arguments:
          parameters:
          - name: message
            value: "{{item}}"
        withItems:
        - depth-1-1
        - depth-1-2
      - name: loop-example-depth-2-2
        template: loop-example-depth-2
  - name: print-message
    inputs:
      parameters:
      - name: message
    container:
      image: busybox
      command: [echo]
      args: ["{{inputs.parameters.message}}"]

# argo get loops-644ch
...
STEP                                        TEMPLATE              PODNAME                               DURATION  MESSAGE
 ✔ loops-644ch                              loop-example-depth-1
 └─┬─✔ loop-example-depth-2-2               loop-example-depth-2
   │ └─┬─✔ print-message-loop(0:depth-2-1)  print-message         loops-644ch-print-message-1796012204  4s
   │   └─✔ print-message-loop(1:depth-2-2)  print-message         loops-644ch-print-message-1116167650  6s
   ├─✔ print-message(0:depth-1-1)           print-message         loops-644ch-print-message-413467513   5s
   └─✔ print-message(1:depth-1-2)           print-message         loops-644ch-print-message-3356863351  5s
```

##### Example 3 (Workflow with Single Container Template)

We create a workload for the single container template. For example:
```
apiVersion: argoproj.io/v1alpha1
kind: Workflow
metadata:
  generateName: hello-
spec:
  entrypoint: main
  templates:
    - name: main
      plugin:
        hello: { }

# argo get hello-jtlcw
...
STEP            TEMPLATE  PODNAME  DURATION  MESSAGE
 ◷ hello-jtlcw  main
```

#### How to suspend a workflow step by step

It is hard for users to add suspend template manually in a workflow before each
leaf template. Some tools are needed to help them to do this.
We introduce three ways to manage the workflow. Responsebilities are different for the
workflow-controller and kueue-controller in two ways.

1. Give users a CLI to help users modifying workflows to add a specific suspend template for each step.
When the workflows are suspended on this special suspend template, the job-controller in Kueue
create workloads for the next step. Modification of workflow-controller is not needed for
this way, so that it is easy to iterate, and no need to manage the version of argo and kueue.
By in this way, users can modify their workflows to skip waiting in kueue, which maybe is not 
acceptable for some users.

2. Integrated Suspend Capability: We propose introducing a new specification field within workflows, such 
as suspendBySteps. If workflow.spec.suspendBySteps is set to true, the workflow-controller automatically 
injects a special suspend template into each stepGroup. The kueue's job-controller monitors these and 
generates workloads for the following steps. Upon workload admission, the suspension step is marked as 
completed.

3. (Recommended) Kueue Webhook Enhancement: A new webhook is added within Kueue to intercept pod creations in the cluster. 
This webhook verifies if the incoming pods are governed by a workflow and if the workflow carries the label 
`kueue.x-k8s.io/queue-name`. When these conditions are met, scheduling gates are appended to the pods. The 
job-controller in Kueue subsequently organizes these pods into groups (identifiable within the workflow's 
status) and creates corresponding workloads for each group. Following workload acceptance, the scheduling 
gates are removed from the pods, enabling their scheduling and execution.

#### Advantages

- It can support queuing by layer level.

### Plain Pod as An Unit

SchedulingGates are added to the pods governed by the workflow with `kueue.x-k8s.io/queue-name`. And create 
a separate workload for each of these pods. Enabling their scheduling and execution after workloads are 
admitted by Kueue.

#### Drawback and Limitations

- Pods in same stepGroup are queued by different workload.
- Gang for stepGroup is not available.

#### Advantages

- Can reuse the existing ability.

## Additional Details

The implementation details will be added after the discussion.

### Test Plan

<!--
**Note:** *Not required until targeted at a release.*
The goal is to ensure that we don't accept enhancements with inadequate testing.

All code is expected to have adequate tests (eventually with coverage
expectations). Please adhere to the [Kubernetes testing guidelines][testing-guidelines]
when drafting this test plan.

[testing-guidelines]: https://git.k8s.io/community/contributors/devel/sig-testing/testing.md
-->

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

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
The code will adhere to regular best practices for unit tests and coverage.

#### Integration tests

Integration tests should be added to ensure workflow work well like other kinds of workloads.

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

The feature starts at the beta level. 
