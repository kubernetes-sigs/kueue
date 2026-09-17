---
name: kueue-general-guidelines
description: Apply project-wide design guidance when drafting, updating, or reviewing Kueue enhancement proposals (KEPs), especially enhancements motivated by or initially implemented for an out-of-tree Job integration.
---

# Kueue General Guidelines

Apply the following guideline to Kueue enhancement proposals. When reviewing a
proposal, identify violations and recommend a concrete generalization.

## Avoid Out-of-Tree Job-Specific KEPs

- Do not define a KEP around the API or specification of a particular
  out-of-tree Job type. An out-of-tree Job uses a workload API that is not a
  built-in Kubernetes API such as Pod, Job, or Deployment.
- Generalize the enhancement around Kueue concepts and behavior so that its
  design does not lock Kueue into one integration's API or specification.
- Allow the initial implementation to support only one out-of-tree Job type,
  but keep the KEP's title, problem statement, semantics, and API design
  integration-independent.
- Present the initially supported Job type as an implementation, adapter, or
  motivating example of the general capability—not as the capability itself.

Source: [Kueue PR #13494 review comment](https://github.com/kubernetes-sigs/kueue/pull/13494#discussion_r4019097617)
