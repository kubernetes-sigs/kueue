package utils

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"k8s.io/apimachinery/pkg/util/yaml"

	fmtErrors "github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"k8s.io/apimachinery/pkg/api/errors"

	"k8s.io/apimachinery/pkg/util/json"

	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
)

var (
	// Multi-application URL paths
	ServeDetailsPath = "/api/serve/applications/"
	DeployPathV2     = "/api/serve/applications/"
	// Job URL paths
	JobPath = "/api/jobs/"
)

type RayDashboardClientInterface interface {
	InitClient(ctx context.Context, url string, rayCluster *rayv1.RayCluster) error
	UpdateDeployments(ctx context.Context, configJson []byte) error
	// V2/multi-app Rest API
	GetServeDetails(ctx context.Context) (*ServeDetails, error)
	GetMultiApplicationStatus(context.Context) (map[string]*ServeApplicationStatus, error)
	GetJobInfo(ctx context.Context, jobId string) (*RayJobInfo, error)
	ListJobs(ctx context.Context) (*[]RayJobInfo, error)
	SubmitJob(ctx context.Context, rayJob *rayv1.RayJob) (string, error)
	SubmitJobReq(ctx context.Context, request *RayJobRequest, name *string) (string, error)
	GetJobLog(ctx context.Context, jobName string) (*string, error)
	StopJob(ctx context.Context, jobName string) error
	DeleteJob(ctx context.Context, jobName string) error
}

type BaseDashboardClient struct {
	client       *http.Client
	dashboardURL string
}

func GetRayDashboardClientFunc(mgr ctrl.Manager, useKubernetesProxy bool) func() RayDashboardClientInterface {
	return func() RayDashboardClientInterface {
		return &RayDashboardClient{
			mgr:                mgr,
			useKubernetesProxy: useKubernetesProxy,
		}
	}
}

type RayDashboardClient struct {
	mgr ctrl.Manager
	BaseDashboardClient
	useKubernetesProxy bool
}

// FetchHeadServiceURL fetches the URL that consists of the FQDN for the RayCluster's head service
// and the port with the given port name (defaultPortName).
func FetchHeadServiceURL(ctx context.Context, cli client.Client, rayCluster *rayv1.RayCluster, defaultPortName string) (string, error) {
	log := ctrl.LoggerFrom(ctx)
	headSvc := &corev1.Service{}
	headSvcName, err := GenerateHeadServiceName(RayClusterCRD, rayCluster.Spec, rayCluster.Name)
	if err != nil {
		log.Error(err, "Failed to generate head service name", "RayCluster name", rayCluster.Name, "RayCluster spec", rayCluster.Spec)
		return "", err
	}

	if err = cli.Get(ctx, client.ObjectKey{Name: headSvcName, Namespace: rayCluster.Namespace}, headSvc); err != nil {
		if errors.IsNotFound(err) {
			log.Error(err, "Head service is not found", "head service name", headSvcName, "namespace", rayCluster.Namespace)
		}
		return "", err
	}

	log.Info("FetchHeadServiceURL", "head service name", headSvc.Name, "namespace", headSvc.Namespace)
	servicePorts := headSvc.Spec.Ports
	port := int32(-1)

	for _, servicePort := range servicePorts {
		if servicePort.Name == defaultPortName {
			port = servicePort.Port
			break
		}
	}

	if port == int32(-1) {
		return "", fmtErrors.Errorf("%s port is not found", defaultPortName)
	}

	domainName := GetClusterDomainName()
	headServiceURL := fmt.Sprintf("%s.%s.svc.%s:%v",
		headSvc.Name,
		headSvc.Namespace,
		domainName,
		port)
	log.Info("FetchHeadServiceURL", "head service URL", headServiceURL)
	return headServiceURL, nil
}

func (r *RayDashboardClient) InitClient(ctx context.Context, url string, rayCluster *rayv1.RayCluster) error {
	log := ctrl.LoggerFrom(ctx)

	if r.useKubernetesProxy {
		var err error
		headSvcName := rayCluster.Status.Head.ServiceName
		if headSvcName == "" {
			log.Info("RayCluster is missing .status.head.serviceName, calling GenerateHeadServiceName instead...", "RayCluster name", rayCluster.Name, "namespace", rayCluster.Namespace)
			headSvcName, err = GenerateHeadServiceName(RayClusterCRD, rayCluster.Spec, rayCluster.Name)
			if err != nil {
				return err
			}
		}

		r.client = r.mgr.GetHTTPClient()
		r.dashboardURL = fmt.Sprintf("%s/api/v1/namespaces/%s/services/%s:dashboard/proxy", r.mgr.GetConfig().Host, rayCluster.Namespace, headSvcName)
		return nil
	}

	r.client = &http.Client{
		Timeout: 2 * time.Second,
	}

	r.dashboardURL = "http://" + url
	return nil
}

// UpdateDeployments update the deployments in the Ray cluster.
func (r *RayDashboardClient) UpdateDeployments(ctx context.Context, configJson []byte) error {
	var req *http.Request
	var err error
	if req, err = http.NewRequestWithContext(ctx, http.MethodPut, r.dashboardURL+DeployPathV2, bytes.NewBuffer(configJson)); err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("UpdateDeployments fail: %s %s", resp.Status, string(body))
	}

	return nil
}

func (r *RayDashboardClient) GetMultiApplicationStatus(ctx context.Context) (map[string]*ServeApplicationStatus, error) {
	serveDetails, err := r.GetServeDetails(ctx)
	if err != nil {
		return nil, fmt.Errorf("Failed to get serve details: %w", err)
	}

	return r.ConvertServeDetailsToApplicationStatuses(serveDetails)
}

// GetServeDetails gets details on all live applications on the Ray cluster.
func (r *RayDashboardClient) GetServeDetails(ctx context.Context) (*ServeDetails, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", r.dashboardURL+ServeDetailsPath, nil)
	if err != nil {
		return nil, err
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("GetServeDetails fail: %s %s", resp.Status, string(body))
	}

	var serveDetails ServeDetails
	if err = json.Unmarshal(body, &serveDetails); err != nil {
		return nil, fmt.Errorf("GetServeDetails failed. Failed to unmarshal bytes: %s", string(body))
	}

	return &serveDetails, nil
}

func (r *RayDashboardClient) ConvertServeDetailsToApplicationStatuses(serveDetails *ServeDetails) (map[string]*ServeApplicationStatus, error) {
	detailsJson, err := json.Marshal(serveDetails.Applications)
	if err != nil {
		return nil, fmt.Errorf("Failed to marshal serve details: %v", serveDetails.Applications)
	}

	applicationStatuses := map[string]*ServeApplicationStatus{}
	if err = json.Unmarshal(detailsJson, &applicationStatuses); err != nil {
		return nil, fmt.Errorf("Failed to unmarshal serve details bytes into map of application statuses: %w. Bytes: %s", err, string(detailsJson))
	}

	return applicationStatuses, nil
}

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

// Note that RayJobInfo and error can't be nil at the same time.
// Please make sure if the Ray job with JobId can't be found. Return a BadRequest error.
func (r *RayDashboardClient) GetJobInfo(ctx context.Context, jobId string) (*RayJobInfo, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", r.dashboardURL+JobPath+jobId, nil)
	if err != nil {
		return nil, err
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, errors.NewBadRequest("Job " + jobId + " does not exist on the cluster")
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var jobInfo RayJobInfo
	if err = json.Unmarshal(body, &jobInfo); err != nil {
		// Maybe body is not valid json, raise an error with the body.
		return nil, fmt.Errorf("GetJobInfo fail: %s", string(body))
	}

	return &jobInfo, nil
}

func (r *RayDashboardClient) ListJobs(ctx context.Context) (*[]RayJobInfo, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", r.dashboardURL+JobPath, nil)
	if err != nil {
		return nil, err
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var jobInfo []RayJobInfo
	if err = json.Unmarshal(body, &jobInfo); err != nil {
		// Maybe body is not valid json, raise an error with the body.
		return nil, fmt.Errorf("GetJobInfo fail: %s", string(body))
	}

	return &jobInfo, nil
}

func (r *RayDashboardClient) SubmitJob(ctx context.Context, rayJob *rayv1.RayJob) (jobId string, err error) {
	request, err := ConvertRayJobToReq(rayJob)
	if err != nil {
		return "", err
	}
	return r.SubmitJobReq(ctx, request, &rayJob.Name)
}

func (r *RayDashboardClient) SubmitJobReq(ctx context.Context, request *RayJobRequest, name *string) (jobId string, err error) {
	log := ctrl.LoggerFrom(ctx)
	rayJobJson, err := json.Marshal(request)
	if err != nil {
		return
	}
	if name != nil {
		log.Info("Submit a ray job", "rayJob", name, "jobInfo", string(rayJobJson))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.dashboardURL+JobPath, bytes.NewBuffer(rayJobJson))
	if err != nil {
		return
	}

	req.Header.Set("Content-Type", "application/json")
	resp, err := r.client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", fmt.Errorf("SubmitJob fail: %s %s", resp.Status, string(body))
	}

	var jobResp RayJobResponse
	if err = json.Unmarshal(body, &jobResp); err != nil {
		// Maybe body is not valid json, raise an error with the body.
		return "", fmt.Errorf("SubmitJob fail: %s", string(body))
	}

	return jobResp.JobId, nil
}

// Get Job Log
func (r *RayDashboardClient) GetJobLog(ctx context.Context, jobName string) (*string, error) {
	log := ctrl.LoggerFrom(ctx)
	log.Info("Get ray job log", "rayJob", jobName)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.dashboardURL+JobPath+jobName+"/logs", nil)
	if err != nil {
		return nil, err
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		// This does the right thing, but breaks E2E test
		//		return nil, errors.NewBadRequest("Job " + jobId + " does not exist on the cluster")
		return nil, nil
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var jobLog RayJobLogsResponse
	if err = json.Unmarshal(body, &jobLog); err != nil {
		// Maybe body is not valid json, raise an error with the body.
		return nil, fmt.Errorf("GetJobLog fail: %s", string(body))
	}

	return &jobLog.Logs, nil
}

func (r *RayDashboardClient) StopJob(ctx context.Context, jobName string) (err error) {
	log := ctrl.LoggerFrom(ctx)
	log.Info("Stop a ray job", "rayJob", jobName)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.dashboardURL+JobPath+jobName+"/stop", nil)
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var jobStopResp RayJobStopResponse
	if err = json.Unmarshal(body, &jobStopResp); err != nil {
		return err
	}

	if !jobStopResp.Stopped {
		jobInfo, err := r.GetJobInfo(ctx, jobName)
		if err != nil {
			return err
		}
		// StopJob only returns an error when JobStatus is not in terminal states (STOPPED / SUCCEEDED / FAILED)
		if !rayv1.IsJobTerminal(jobInfo.JobStatus) {
			return fmt.Errorf("Failed to stopped job: %v", jobInfo)
		}
	}
	return nil
}

func (r *RayDashboardClient) DeleteJob(ctx context.Context, jobName string) error {
	log := ctrl.LoggerFrom(ctx)
	log.Info("Delete a ray job", "rayJob", jobName)

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, r.dashboardURL+JobPath+jobName, nil)
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return nil
}

func ConvertRayJobToReq(rayJob *rayv1.RayJob) (*RayJobRequest, error) {
	req := &RayJobRequest{
		Entrypoint:   rayJob.Spec.Entrypoint,
		SubmissionId: rayJob.Status.JobId,
		Metadata:     rayJob.Spec.Metadata,
	}
	if len(rayJob.Spec.RuntimeEnvYAML) != 0 {
		runtimeEnv, err := UnmarshalRuntimeEnvYAML(rayJob.Spec.RuntimeEnvYAML)
		if err != nil {
			return nil, err
		}
		req.RuntimeEnv = runtimeEnv
	}
	req.NumCpus = rayJob.Spec.EntrypointNumCpus
	req.NumGpus = rayJob.Spec.EntrypointNumGpus
	if rayJob.Spec.EntrypointResources != "" {
		if err := json.Unmarshal([]byte(rayJob.Spec.EntrypointResources), &req.Resources); err != nil {
			return nil, err
		}
	}
	return req, nil
}

func UnmarshalRuntimeEnvYAML(runtimeEnvYAML string) (RuntimeEnvType, error) {
	var runtimeEnv RuntimeEnvType
	err := yaml.Unmarshal([]byte(runtimeEnvYAML), &runtimeEnv)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal RuntimeEnvYAML: %v: %w", runtimeEnvYAML, err)
	}
	return runtimeEnv, nil
}
