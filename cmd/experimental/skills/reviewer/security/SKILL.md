---
name: security
description: Review a diff for security issues exploitable by a tenant, neighbouring controller, or malicious workload spec — input validation, injection, DoS, nil-safety, authz, info disclosure, supply chain, webhook safety.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Security

Use this when reviewing a diff for code patterns exploitable by an authenticated
low-privileged tenant, a compromised neighbouring controller, or a malicious workload
spec. Kueue is a privileged in-cluster controller with broad watch permissions that
also sits in the admission path via webhooks — one insecure pattern can crash-loop the
batch-scheduling surface, leak tenant data, allow quota escape, or enable arbitrary
code execution.

## Rules

Each rule in this domain is its own sub-skill. Scan the diff for violations of every
rule below.

| Rule | Triggers on |
|---|---|
| [input-validation](./input-validation/SKILL.md) | missing validation on user-settable CR fields, webhook payloads, labels, annotations, or untrusted deserialization |
| [injection](./injection/SKILL.md) | exec/SSRF/template/query/selector built from user-supplied strings |
| [path-traversal](./path-traversal/SKILL.md) | file paths or archive extraction without root containment |
| [resource-bounds-dos](./resource-bounds-dos/SKILL.md) | uncapped loops/allocations, missing timeouts, reconciler-wedging input, unbounded metric labels |
| [nil-safety](./nil-safety/SKILL.md) | unguarded dereferences from malformed CRs; goroutines without recover |
| [authn-authz-relaxation](./authn-authz-relaxation/SKILL.md) | webhook token skipping, failurePolicy Ignore, wildcard RBAC, privileged pod specs |
| [information-disclosure](./information-disclosure/SKILL.md) | full-object logging, request echoing, credentials in status/annotations, kubeconfig logging |
| [supply-chain-hygiene](./supply-chain-hygiene/SKILL.md) | unpinned images, TLS bypass, unjustified replace directives, curl\|sh, moving-tag Actions |
| [annotation-label-namespace-abuse](./annotation-label-namespace-abuse/SKILL.md) | generic annotation/label loops that copy or forward values |
| [feature-gated-insecure-paths](./feature-gated-insecure-paths/SKILL.md) | gated behavior whose security assumptions fail when accidentally enabled |
| [integration-adapter-trust-boundary](./integration-adapter-trust-boundary/SKILL.md) | adapters reading credential-like fields from third-party CRDs without treating them as untrusted |
| [webhook-safety-regressions](./webhook-safety-regressions/SKILL.md) | loosened failurePolicy, shortened timeouts, TLS bypass, non-idempotent mutating webhooks |

@./input-validation/SKILL.md
@./injection/SKILL.md
@./path-traversal/SKILL.md
@./resource-bounds-dos/SKILL.md
@./nil-safety/SKILL.md
@./authn-authz-relaxation/SKILL.md
@./information-disclosure/SKILL.md
@./supply-chain-hygiene/SKILL.md
@./annotation-label-namespace-abuse/SKILL.md
@./feature-gated-insecure-paths/SKILL.md
@./integration-adapter-trust-boundary/SKILL.md
@./webhook-safety-regressions/SKILL.md

## How to report

Cite the exact file and line, name the rule violated and the relevant CWE/CVE where
applicable, and classify severity (high / medium / low). Anything that crashes the
controller, leaks credentials, bypasses authz, or enables injection is high. Missing
caps, unbounded cardinality, or weakened supply-chain controls are typically medium.
Stylistic security smells with no concrete exploit path are low.

Return two sections:

### Findings

A bullet list, one per violation, in this format:

```
- <High/Medium/Low> | <Finding Title>: <one-sentence explanation grounded in the diff, with CWE/CVE where applicable>
```

### Recommendations

One recommendation per finding above, numbered sequentially within this skill. Each
recommendation MUST contain all four sections below.

```
### Recommendation N: <short title>

**Problem**: Describe the specific issue in the diff.
**Reason**: Explain why this is a problem — what goes wrong or degrades over time.
**Solution**: Give a concrete, actionable fix. Where possible, show a before/after code snippet.
**Locations**: List every place this issue occurs, one per line, in `filepath:line` form.
```
