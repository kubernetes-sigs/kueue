# KEP-4377: TLS Support for Metrics

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
  - [Existing Deployments Options](#existing-deployments-options)
  - [Future Deployment Options](#future-deployment-options)
  - [Unsupported Deployment Options](#unsupported-deployment-options)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories (Optional)](#user-stories-optional)
    - [Story 1](#story-1)
  - [Notes](#notes)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Configuration API Change](#configuration-api-change)
  - [Deployment changes](#deployment-changes)
    - [Kustomize](#kustomize)
    - [Helm](#helm)
  - [KubeBuilder Recommendation](#kubebuilder-recommendation)
  - [Prometheus Service Monitor Changes](#prometheus-service-monitor-changes)
  - [Deployment Changes](#deployment-changes-1)
  - [Controller Changes](#controller-changes)
  - [Test Plan](#test-plan)
      - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [Opt in API](#opt-in-api)
<!-- /toc -->

## Summary

Kueue should allow admins to provide their own certificates for metrics endpoints.

## Motivation

Kueue has a few options for managing certificates.
Kueue provides the ability to manage certificates for both webhooks and metrics.
For webhooks Kueue provide either external certificates or we use internal certificate rotation.

For metrics Kueue only allows one to use internal certificates.
In order to have metrics exposed, one needs to turn off tls verify on the service Monitors.
For many organizations, this can be flagged as a security vulnerability.

We will elaborate on the existing deployment options.

### Existing Deployments Options

Kueue provides the following deployment options for webhooks and metrics.
Metrics will be assumed to be deployed but they are also optional.

Option 1:

- Admin deploys Kueue with InternalCertManagement set to true.
- This will use internal certificates for webhooks.
- Metrics will use internal certificates.
- ServiceMonitor will skip tls checks via InsecureSkipVerify: true.

Option 2:

- Admin deploys Kueue and Cert Manager (InternalCertManagement set to false).
- This will use external certificates for webhooks.
- Metrics will use internal certificates.
- ServiceMonitor will skip tls checks via InsecureSkipVerify: true.

### Future Deployment Options

Option 3:

- Admin deploys Kueue and Cert Manager (InternalCertManagement set to false).
- Kueue will use external certificates for webhooks.
- Kueue will use external certificates for metrics.
- ServiceMonitor will verify the TLS certificates from a metrics secret created by external certificate solution.

### Unsupported Deployment Options

Not supported Option:

- Admin deploys Kueue and Cert Manager
- Sets InternalCertManagement to true
- Cert manager is used to provision certificates for metrics
- Internal cert rotation is used for webhook.
- ServiceMonitor will verify the TLS certificates from a metrics secret.

To block this option we will validate that one cannot fall into this scenario.

### Goals

- Provide a way for metrics to verify tls certificates.

### Non-Goals

- Disable secure metrics serving.

It is important to secure metrics if they are enabled.
We will not provide a way to disable this.

- Allow for external certificates for metrics but internal certificates for webhooks.

- Adopt Internal Cert Manager solution for metrics and provide self signed certificates.

- Supporting APIService certificates

APIService currently uses Insecure flags but there has been no effort made yet to enhance that for Cert manager. It would be good to have support for this but it is out of scope of this KEP.

## Proposal

We will require that if one is using external certificates and prometheus is enabled,
one must use certificates for metrics. 
We will effectively be disabling the use of controller runtime generated certificates if an external certificate solution is used.

We will pass this options to the controller runtime `MetricsServer` option where the validation will be done by
the controller runtime.

### User Stories (Optional)

<!--
Detail the things that people will be able to do if this KEP is implemented.
Include as much detail as possible so that people can understand the "how" of
the system. The goal here is to make this feel real for users without getting
bogged down.
-->

#### Story 1

As a cluster adminstration, I want to use an external certificate service for all certificates. 
I want to use external certificates for webhooks and metrics.
I am concerned with turning off tls verification for prometheus and I want to validate the certificates.

### Notes

Kueue at the moment skips all tls checking for prometheus endpoints.

```yaml
# Prometheus Monitor Service (Metrics)
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: controller-manager-metrics-monitor
  namespace: system
spec:
  endpoints:
    - path: /metrics
      port: https
      scheme: https
      bearerTokenFile: /var/run/secrets/kubernetes.io/serviceaccount/token
      tlsConfig:
        insecureSkipVerify: true
  selector:
    matchLabels:
      app.kubernetes.io/component: controller
      app.kubernetes.io/name: kueue
```

### Risks and Mitigations

This is more secure option for deploying Kueue but it would require an external dependency on Cert Manager or some other
certificate management solution.

## Design Details

### Configuration API Change

We will update the configuration to reflect this new state.

```golang
type InternalCertManagement struct {
  // Enable controls the use of internal cert management for the webhook
  // and metrics endpoints.
  // When enabled Kueue is using libraries to generate and
  // self-sign the certificates.
  // When disabled, you need to provide the certificates for
  // the webhooks and metrics through a third party certificate
  // This secret is mounted to the kueue controller manager pod. The mount
  // path for webhooks is /tmp/k8s-webhook-server/serving-certs, whereas for
  // metrics endpoint the expected path is `/etc/kueue/metrics-certs`.
  Enable *bool `json:"enable,omitempty"`
 . . .
}
```

### Deployment changes

#### Kustomize

For kustomize based installation, we will add the following optional patches:

- Certificate for metrics
- Patch to deployment to mount volumes to the deployment
- ServiceMonitor patches to enable tls checks

These will be enabled similar to how we enable Cert Manager and Prometheus (uncomment those sections).

#### Helm

We will also provide the ability for our Helm charts to use this feature.

The helm chart needs to provide the following abilities:

- Ability to add volumes/volumeMounts to the deployment.
- Ability to inject custom TlsConfigs for the ServiceMonitor.
- Ability to provision a certificate for metrics using Cert Manager.

### KubeBuilder Recommendation

Kubebuilder recommends that one enables some kind of external certificate solution for metrics.
[Recommended For Production](https://book.kubebuilder.io/reference/metrics#recommended-enabling-certificates-for-production-disabled-by-default)

> The default scaffold in cmd/main.go uses a controller-runtime feature to automatically generate a self-signed certificate to secure the metrics server.
  While this is convenient for development and testing, it is not recommended for production.
  Those certificates are used to secure the transport layer (TLS). The token authentication using authn/authz, which is enabled by default serves as the application-level credential. However, for example, when you enable the integration of your metrics with Prometheus, those certificates can be used to secure the communication.

With the deprecation of Kube-RBAC-Proxy from Kueue, we are already requiring secure metrics via the use of the below code:

```golang
if secureMetrics {
  ...
  metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
}
```

### Prometheus Service Monitor Changes

The example below shows:

```yaml
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: controller-manager-metrics-monitor
  namespace: system
spec:
  endpoints:
    - path: /metrics
      port: https
      scheme: https
      bearerTokenFile: /var/run/secrets/kubernetes.io/serviceaccount/token
      tlsConfig:
        serverName: SERVICE_NAME.SERVICE_NAMESPACE.svc
        insecureSkipVerify: false
        ca:
          secret:
            name: metrics-server-cert
            key: ca.crt
        cert:
          secret:
            name: metrics-server-cert
            key: tls.crt
        keySecret:
          name: metrics-server-cert
          key: tls.key
  selector:
    matchLabels:
      app.kubernetes.io/component: controller
      app.kubernetes.io/name: kueue
```

### Deployment Changes

Whenever you add Certificate management, one needs to mount a volume to the deployment that references a secret that is created by
an external service.

Following the kubebuilder examples, one would add the following example to the deployment:

```yaml
# Add the volumeMount for the metrics-server certs
- op: add
  path: /spec/template/spec/containers/0/volumeMounts/-
  value:
    mountPath: /etc/k8s-metrics-server/metrics-certs
    name: metrics-certs
    readOnly: true

# Add the metrics-server certs volume configuration
- op: add
  path: /spec/template/spec/volumes/-
  value:
    name: metrics-certs
    secret:
      secretName: metrics-server-cert
      optional: false
      items:
        - key: ca.crt
          path: ca.crt
        - key: tls.crt
          path: tls.crt
        - key: tls.key
          path: tls.key
```

### Controller Changes

To keep configurations minimal, we will require that the certificates be mounted to "/etc/kueue/metrics-certs" 
and the tls certificate and key are called `tls.crt` and `tls.key`, respectively.

```golang
	if !cfg.InternalCertManager {
		metricsCertPath := "/etc/kueue/metrics-certs"
		setupLog.Info("Initializing metrics certificate watcher using provided certificates",
			"metrics-cert-path", metricsCertPath)

		var err error
		metricsCertWatcher, err := certwatcher.New(
			filepath.Join(metricsCertPath, "tls.crt"),
			filepath.Join(metricsCertPath, "tls.key"),
		)
		if err != nil {
			setupLog.Error(err, "to initialize metrics certificate watcher", "error", err)
			os.Exit(1)
		}

		metricsServerOptions.TLSOpts = append(metricsServerOptions.TLSOpts, func(config *tls.Config) {
			config.GetCertificate = metricsCertWatcher.GetCertificate
		})
	}
```

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

##### Prerequisite testing updates

<!--
Based on reviewers feedback describe what additional tests need to be added prior
implementing this enhancement to ensure the enhancements have also solid foundations.
-->

#### Unit Tests

This will mostly impact cmd/kueue which does not have unit tests.

#### Integration tests

None.

### Graduation Criteria

I think this can go to stable as is as it is an opt-in feature.

## Implementation History

Draft: Feb 25 2025

## Drawbacks

This puts more support burden on upstream Kueue as we had a new pattern for certificates.

## Alternatives

### Opt in API

We proposed an API for metrics to use certificates. This would support the existing solutions but it causes a more complicated support issue.
Generally, if one is using an external certificate service, they probably do not want to rely on internal certificates.

Due to this, we decided to drop the support of using controller runtime certificates for those that are using Cert Manager.

We keep the same behavior in the case that internal certificates are used.
