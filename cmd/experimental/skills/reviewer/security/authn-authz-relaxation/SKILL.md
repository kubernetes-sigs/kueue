---
name: authn-authz-relaxation
description: Flag AuthN/AuthZ relaxation — webhook token skipping, failurePolicy Ignore, missing SubjectAccessReview, wildcard RBAC, privileged pod specs (CWE-269, CWE-287, CWE-306, CWE-862, CWE-863).
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: AuthN / AuthZ Relaxation

- Webhook handlers that skip token validation or trust `X-Forwarded-For` /
  `X-Remote-User` (CWE-287, CWE-306).
- New admission webhooks registered with `failurePolicy: Ignore` without explicit,
  documented justification (CWE-352-class bypass).
- Privileged operations (cross-namespace reads, secret reads, impersonation) on a
  tenant's behalf without a preceding `SubjectAccessReview`.
- New ClusterRoles with wildcard verbs (`*`) or resources (`*`). New RBAC must use
  minimum verbs and prefer `RoleBinding` over `ClusterRoleBinding` where namespace
  scope suffices (CWE-269, CWE-862, CWE-863).
- `Secret` reads via a blanket cluster-wide verb instead of `resourceNames` scoping.
- Pod specs setting `HostPID`, `HostNetwork`, `HostIPC`, `privileged: true`,
  `allowPrivilegeEscalation: true`, mounting sensitive host paths
  (`/etc/kubernetes`, `/var/run/docker.sock`), or dropping `runAsNonRoot: true`.
