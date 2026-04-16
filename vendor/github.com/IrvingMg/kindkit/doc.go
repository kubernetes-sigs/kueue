// Package kindkit creates and manages [Kind] (Kubernetes in Docker)
// clusters from Go code.
//
// # Context handling
//
// [Create], [CreateOrReuse], [Cluster.Delete], and [Cluster.ExportLogs]
// accept a context argument, but Kind's underlying API does not
// support cancellation, so the context is ignored. [Cluster.LoadImages]
// and [Cluster.ApplyManifests] do honor the supplied context.
//
// [Kind]: https://kind.sigs.k8s.io/
package kindkit
