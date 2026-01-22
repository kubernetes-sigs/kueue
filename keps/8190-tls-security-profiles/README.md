# KEP-8190: TLS Security Profile Support

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
  - [User Stories](#user-stories)
    - [Story 1](#story-1)
    - [Story 2](#story-2)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Configuration API Change](#configuration-api-change)
  - [Supported Servers](#supported-servers)
  - [Default Values](#default-values)
  - [Implementation Approach](#implementation-approach)
  - [Test Plan](#test-plan)
      - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit Tests](#unit-tests)
    - [E2E tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
    - [Beta](#beta)
    - [Stable](#stable)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [Enum-Based API](#enum-based-api)
  - [Server-Specific Configuration](#server-specific-configuration)
  - [Environment Variables](#environment-variables)
<!-- /toc -->

## Summary

This KEP proposes adding configurable TLS security profiles to Kueue's API servers (webhooks, metrics, controller, and visibility).
This enhancement will allow administrators to explicitly configure the minimum TLS version and cipher suites
for all TLS-enabled servers in Kueue, improving security posture, compliance, and compatibility with organizational security policies.

## Motivation

In Go-based projects, explicitly configuring TLS settings is crucial for security, compatibility, and compliance. Organizations
often have strict security requirements that mandate specific TLS versions and cipher suites. Currently, Kueue relies on Go's
default TLS configurations, which may not align with organizational security policies or compliance requirements (e.g., PCI-DSS,
FIPS 140-2, SOC 2).

The lack of configurable TLS settings creates several challenges:
- Organizations cannot enforce minimum TLS version requirements (e.g., TLS 1.2 or 1.3 only)
- Administrators cannot disable weak or deprecated cipher suites
- Compliance audits may flag the use of default TLS configurations as a security concern
- Integration with security scanning tools that require specific TLS configurations is difficult

By providing explicit TLS configuration options, Kueue can better serve enterprise environments with stringent security requirements
while maintaining backward compatibility through sensible defaults.

### Goals

- Provide configuration options for minimum TLS version across all Kueue API servers
- Enable administrators to specify allowed cipher suites for TLS connections
- Apply TLS settings uniformly across webhook, metrics, controller, and visibility servers
- Maintain backward compatibility with existing deployments through sensible defaults
- Document TLS configuration best practices and security implications

### Non-Goals

- Enforcing specific TLS configurations by default (the feature should be opt-in)
- Providing automated certificate rotation beyond what already exists in Kueue
- Supporting per-server TLS configuration (all servers will use the same TLS profile)
- Implementing custom TLS handshake logic beyond Go's standard library capabilities
- Supporting TLS versions below 1.2 (which are deprecated and insecure)
- Providing a compatibility mode for legacy TLS 1.0/1.1 clients

## Proposal

We propose adding TLS configuration options to Kueue's configuration API that will be applied to all TLS-enabled servers.
The configuration will be part of the controller configuration and will allow administrators to set:

1. Minimum TLS version (1.2 or 1.3)
2. Allowed cipher suites (based on Go's crypto/tls package)

These settings will be applied uniformly across all Kueue servers (webhooks, metrics, controller, and visibility) to ensure
consistent security posture across the entire Kueue deployment.

### User Stories

#### Story 1

As a platform administrator in a regulated industry (financial services, healthcare), I need to ensure that all
components in my Kubernetes cluster use TLS 1.3 with approved cipher suites to meet compliance requirements (PCI-DSS, HIPAA).
I want to configure Kueue to enforce these requirements across all its API endpoints.

#### Story 2

As a security engineer, I need to disable weak cipher suites and enforce strong cryptographic algorithms across all
services in my cluster. I want to configure Kueue's TLS settings to align with our organization's security baseline,
which requires specific cipher suites approved by our security team.

### Risks and Mitigations

**Risk**: Misconfiguration of TLS settings could prevent clients from connecting to Kueue servers.

**Mitigation**:
- Provide comprehensive documentation with examples and best practices
- Use sensible defaults that work for most environments
- Log clear error messages when TLS configuration issues occur
- Server will fail to start with clear error messages if TLS configuration is invalid

**Risk**: Overly restrictive TLS settings could break compatibility with existing clients or monitoring tools.

**Mitigation**:
- Make the feature opt-in with backward-compatible defaults
- Document the cipher suites and TLS versions supported by common clients (kubectl, Prometheus, etc.)
- Provide migration guide for upgrading from default to custom TLS configurations
- Include troubleshooting section in documentation for common TLS connectivity issues

**Risk**: Performance impact from enforcing certain cipher suites or TLS versions.

**Mitigation**:
- Document performance characteristics of different cipher suites
- Recommend modern, hardware-accelerated cipher suites (AES-GCM) as defaults
- Provide benchmarking guidance for administrators to test performance impact

## Design Details

### Configuration API Change

We will add a new `TLSOptions` structure to the controller configuration using a string-based API:

```golang
// TLSOptions defines TLS security settings for Kueue servers
type TLSOptions struct {
	// tlsMinVersion is the minimum TLS version supported.
	// Values are from tls package constants (https://golang.org/pkg/crypto/tls/#pkg-constants).
	// Valid values are: "VersionTLS12", "VersionTLS13"
	// Default: "" (uses Go's default, currently TLS 1.2)
	// +optional
	TLSMinVersion string `json:"tlsMinVersion,omitempty"`

	// tlsCipherSuites is the list of allowed cipher suites for the server.
	// Note that TLS 1.3 ciphersuites are not configurable.
	// Values are from tls package constants (https://golang.org/pkg/crypto/tls/#pkg-constants).
	// Default: nil (uses Go's default cipher suite selection)
	// +optional
	TLSCipherSuites []string `json:"tlsCipherSuites,omitempty"`
}

// ControllerManager is added to the Configuration struct
type ControllerManager struct {
	// ... existing fields ...

	// TLS contains TLS security settings for all Kueue API servers
	// (webhooks, metrics, and visibility).
	// +optional
	TLS *TLSOptions `json:"tls,omitempty"`
}
```

This approach uses string values that are validated at runtime against Go's crypto/tls package constants.
This provides flexibility for future TLS versions and cipher suites without requiring API changes.
This approach also follows existing solutions in upstream Kubernetes in both [apiserver secure serving](https://github.com/kubernetes/kubernetes/blob/cc55e3447816e49c0bd7128668da49b856294536/staging/src/k8s.io/apiserver/pkg/server/secure_serving.go#L45) and [kubelet](https://github.com/kubernetes/kubernetes/blob/cc55e3447816e49c0bd7128668da49b856294536/cmd/kubelet/app/server.go#L1134).

### Supported Servers

The TLS configuration will be applied to the following Kueue servers:

1. **Webhook Server**: Admission webhook endpoints for validating and mutating resources
2. **Metrics Server**: Prometheus metrics endpoint (when secure metrics are enabled)
3. **Visibility Server**: API server for workload visibility queries
4. **Health Probe Server** (if applicable): Health check endpoints

### Default Values

To maintain backward compatibility and provide secure defaults:

- **TLSMinVersion**: "" (empty string, uses Go's default which is TLS 1.2)
  - Rationale: TLS 1.2 is widely supported and required by most compliance frameworks
  - TLS 1.0 and 1.1 are deprecated and will not be supported
  - Valid values: "VersionTLS12", "VersionTLS13"

- **TLSCipherSuites**: nil (uses Go's default secure cipher suite selection)
  - For TLS 1.2, recommended cipher suites include:
    - TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256
    - TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384
    - TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256
    - TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384
  - For TLS 1.3, cipher suites are not configurable (Go uses the built-in secure set)
  - Valid cipher suite values are defined in Go's crypto/tls package constants

### Implementation Approach

The implementation will follow these steps:

1. **Configuration Loading**: Parse TLS options from the controller configuration
2. **Validation**: Validate TLS version and cipher suite strings against Go's crypto/tls package at startup
3. **TLS Config Construction**: Build a `tls.Config` object with the specified options
4. **Server Configuration**: Apply the TLS config to all relevant servers using `TLSOpts`

The validation functions (`TLSVersion`, `TLSCipherSuites`) will use Go's crypto/tls package to validate the string values, ensuring only valid TLS versions and cipher suites are accepted.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

##### Prerequisite testing updates

- Create test utilities for verifying TLS settings on running servers

#### Unit Tests

The following packages will have unit test coverage:

- `pkg/controller/core`: Configuration loading
  - Test TLS configurations are loaded correctly
  - Test default values are applied correctly

- `cmd/kueue`: TLS config application to servers
  - Test TLS config is correctly applied to webhook server
  - Test TLS config is correctly applied to metrics server
  - Test TLS config is correctly applied to visibility server

Coverage target: >80% for new TLS configuration code

We will cover validation in the unit tests.
- Valid TLSVersions
- Valid CipherSuite
- CipherSuites only allowed when TLS 1.2 is specified.

#### E2E tests

Kueue has the ability to test custom configurations so there will be a e2e test verifying TLS 1.3 and running some simple smoke tests (visbility, job controller).

### Graduation Criteria

#### Beta

This feature will start in beta with the feature gate TLSOptions.

When the feature gate is disabled, there will be no passing in the TLS options from the API (current Kueue defaults)

When the feature gate is enabled, TLS options can be set for the golang servers.

#### Stable

- Adoption of feature

## Implementation History

- 2025-12: KEP proposed based on issues #8190 and OCPKUEUE-450
- 2026-01: KEP approved

## Drawbacks

1. **Configuration Complexity**: Adds more configuration options that administrators need to understand
   - Mitigation: Provide clear documentation and examples

2. **Testing Burden**: Requires testing various TLS configuration combinations
   - Mitigation: Focus on common scenarios and provide validation tools

3. **Troubleshooting Complexity**: TLS issues can be difficult to diagnose
   - Mitigation: Add comprehensive logging and error messages

4. **Maintenance**: TLS standards evolve, requiring updates to documentation and recommendations
   - Mitigation: Document the configuration in a way that's easy to update

## Alternatives

### Enum-Based API

An alternative approach uses strongly typed enums for TLS versions and cipher suites with kubebuilder validation:

```golang
// TLSVersion represents a TLS version string
// +kubebuilder:validation:Enum={"1.2","1.3"}
type TLSVersion string

const (
	// TLSVersion12 represents TLS version 1.2
	TLSVersion12 TLSVersion = "1.2"
	// TLSVersion13 represents TLS version 1.3
	TLSVersion13 TLSVersion = "1.3"
)

// CipherSuite represents a TLS cipher suite name
// +kubebuilder:validation:Enum=TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA;TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA;TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA;TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA;TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256;TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384;TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256;TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384;TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256;TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256;TLS_AES_128_GCM_SHA256;TLS_AES_256_GCM_SHA384;TLS_CHACHA20_POLY1305_SHA256
type CipherSuite string

const (
	// TLS 1.2 cipher suites
	CipherSuiteTLSECDHEECDSAWithAES128CBCSHA           CipherSuite = "TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA"
	CipherSuiteTLSECDHEECDSAWithAES256CBCSHA           CipherSuite = "TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA"
	CipherSuiteTLSECDHERSAWithAES128CBCSHA             CipherSuite = "TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA"
	CipherSuiteTLSECDHERSAWithAES256CBCSHA             CipherSuite = "TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA"
	CipherSuiteTLSECDHEECDSAWithAES128GCMSHA256        CipherSuite = "TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256"
	CipherSuiteTLSECDHEECDSAWithAES256GCMSHA384        CipherSuite = "TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384"
	CipherSuiteTLSECDHERSAWithAES128GCMSHA256          CipherSuite = "TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256"
	CipherSuiteTLSECDHERSAWithAES256GCMSHA384          CipherSuite = "TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384"
	CipherSuiteTLSECDHERSAWithCHACHA20POLY1305SHA256   CipherSuite = "TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256"
	CipherSuiteTLSECDHEECDSAWithCHACHA20POLY1305SHA256 CipherSuite = "TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256"

	// TLS 1.3 cipher suites
	CipherSuiteTLSAES128GCMSHA256       CipherSuite = "TLS_AES_128_GCM_SHA256"
	CipherSuiteTLSAES256GCMSHA384       CipherSuite = "TLS_AES_256_GCM_SHA384"
	CipherSuiteTLSCHACHA20POLY1305SHA256 CipherSuite = "TLS_CHACHA20_POLY1305_SHA256"
)

// TLSOptions defines TLS security settings for Kueue servers
type TLSOptions struct {
	// MinTLSVersion specifies the minimum TLS version that is acceptable.
	// If not specified, defaults to "1.2" for backward compatibility.
	// +optional
	MinTLSVersion TLSVersion `json:"minTLSVersion,omitempty"`

	// CipherSuites specifies the list of enabled TLS cipher suites.
	// If not specified, a secure default list will be used.
	// The available cipher suites are defined in Go's crypto/tls package.
	// See https://golang.org/pkg/crypto/tls/#pkg-constants for valid values.
	// +optional
	CipherSuites []CipherSuite `json:"cipherSuites,omitempty"`
}
```

**Rejected because**:
- Maintainability concerns around managing TLS settings over time
- When new TLS versions (e.g., TLS 1.4) or cipher suites are added to Go, we would need to update the enum definitions and kubebuilder validation markers
- Requires code changes and new releases to support new TLS options
- The string-based API with runtime validation is more flexible and can adapt to Go's crypto/tls package changes automatically

### Server-Specific Configuration

Instead of applying TLS settings globally, we could allow per-server configuration:

```golang
type TLSOptions struct {
	Webhooks   *TLSProfile `json:"webhooks,omitempty"`
	Metrics    *TLSProfile `json:"metrics,omitempty"`
	Visibility *TLSProfile `json:"visibility,omitempty"`
}
```

**Rejected because**:
- Adds unnecessary complexity for most use cases
- Inconsistent TLS policies across servers could create security gaps
- More difficult to validate and test
- Most organizations want uniform TLS policies across all endpoints

### Environment Variables

We could expose TLS settings through environment variables instead of the configuration API:

```
KUEUE_MIN_TLS_VERSION=1.3
KUEUE_TLS_CIPHER_SUITES=TLS_AES_128_GCM_SHA256,TLS_AES_256_GCM_SHA384
```

**Rejected because**:
- Less discoverable than API-based configuration
- Harder to validate at deployment time
- Inconsistent with Kueue's configuration patterns
- Environment variables are less suitable for complex list values
- Cannot be managed through GitOps workflows as easily

