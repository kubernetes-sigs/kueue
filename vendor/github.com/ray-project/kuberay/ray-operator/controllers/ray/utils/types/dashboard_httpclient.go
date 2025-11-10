package types

import (
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
)

type RuntimeEnvType map[string]interface{}

// RayJobInfo is the response of "ray job status" api.
// Reference to https://docs.ray.io/en/latest/cluster/running-applications/job-submission/rest.html#ray-job-rest-api-spec
// Reference to https://github.com/ray-project/ray/blob/cfbf98c315cfb2710c56039a3c96477d196de049/dashboard/modules/job/pydantic_models.py#L38-L107
type RayJobInfo struct {
	ErrorType    *string           `json:"error_type,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	RuntimeEnv   RuntimeEnvType    `json:"runtime_env,omitempty"`
	JobStatus    rayv1.JobStatus   `json:"status,omitempty"`
	Entrypoint   string            `json:"entrypoint,omitempty"`
	JobId        string            `json:"job_id,omitempty"`
	SubmissionId string            `json:"submission_id,omitempty"`
	Message      string            `json:"message,omitempty"`
	StartTime    uint64            `json:"start_time,omitempty"`
	EndTime      uint64            `json:"end_time,omitempty"`
}

// RayJobRequest is the request body to submit.
// Reference to https://docs.ray.io/en/latest/cluster/running-applications/job-submission/rest.html#ray-job-rest-api-spec
// Reference to https://github.com/ray-project/ray/blob/cfbf98c315cfb2710c56039a3c96477d196de049/dashboard/modules/job/common.py#L325-L353
type RayJobRequest struct {
	RuntimeEnv   RuntimeEnvType     `json:"runtime_env,omitempty"`
	Metadata     map[string]string  `json:"metadata,omitempty"`
	Resources    map[string]float32 `json:"entrypoint_resources,omitempty"`
	Entrypoint   string             `json:"entrypoint"`
	SubmissionId string             `json:"submission_id,omitempty"`
	NumCpus      float32            `json:"entrypoint_num_cpus,omitempty"`
	NumGpus      float32            `json:"entrypoint_num_gpus,omitempty"`
}

type RayJobResponse struct {
	JobId string `json:"job_id"`
}

type RayJobStopResponse struct {
	Stopped bool `json:"stopped"`
}

type RayJobLogsResponse struct {
	Logs string `json:"logs,omitempty"`
}
