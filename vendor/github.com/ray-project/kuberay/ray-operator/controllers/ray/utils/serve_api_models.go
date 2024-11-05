package utils

// Please see the Ray Serve docs
// https://docs.ray.io/en/latest/serve/api/doc/ray.serve.schema.ServeDeploySchema.html for the
// multi-application schema.

// ServeDeploymentStatus and ServeApplicationStatus describe the format of status(es) that will
// be returned by the GetMultiApplicationStatus method of the dashboard client
// Describes the status of a deployment
type ServeDeploymentStatus struct {
	Name    string `json:"name,omitempty"`
	Status  string `json:"status,omitempty"`
	Message string `json:"message,omitempty"`
}

// Describes the status of an application
type ServeApplicationStatus struct {
	Deployments map[string]ServeDeploymentStatus `json:"deployments"`
	Name        string                           `json:"name,omitempty"`
	Status      string                           `json:"status"`
	Message     string                           `json:"message,omitempty"`
}

// V2 Serve API Response format. These extend the ServeDeploymentStatus and ServeApplicationStatus structs,
// but contain more information such as route prefix because the V2/multi-app GET API fetchs general metadata,
// not just statuses.
type ServeDeploymentDetails struct {
	ServeDeploymentStatus
	RoutePrefix string `json:"route_prefix,omitempty"`
}

type ServeApplicationDetails struct {
	Deployments map[string]ServeDeploymentDetails `json:"deployments"`
	ServeApplicationStatus
	RoutePrefix string `json:"route_prefix,omitempty"`
	DocsPath    string `json:"docs_path,omitempty"`
}

type ServeDetails struct {
	Applications map[string]ServeApplicationDetails `json:"applications"`
	DeployMode   string                             `json:"deploy_mode,omitempty"`
}
