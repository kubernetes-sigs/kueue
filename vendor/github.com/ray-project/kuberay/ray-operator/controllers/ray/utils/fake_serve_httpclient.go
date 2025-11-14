package utils

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"

	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	"github.com/ray-project/kuberay/ray-operator/controllers/ray/utils/dashboardclient"
	utiltypes "github.com/ray-project/kuberay/ray-operator/controllers/ray/utils/types"
)

type FakeRayDashboardClient struct {
	multiAppStatuses  map[string]*utiltypes.ServeApplicationStatus
	GetJobInfoMock    atomic.Pointer[func(context.Context, string) (*utiltypes.RayJobInfo, error)]
	serveDetails      utiltypes.ServeDetails
	LastUpdatedConfig []byte
}

var _ dashboardclient.RayDashboardClientInterface = (*FakeRayDashboardClient)(nil)

func (r *FakeRayDashboardClient) InitClient(_ *http.Client, _ string) {
}

func (r *FakeRayDashboardClient) UpdateDeployments(_ context.Context, configJson []byte) error {
	r.LastUpdatedConfig = configJson
	fmt.Print("UpdateDeployments fake succeeds.")
	return nil
}

func (r *FakeRayDashboardClient) GetMultiApplicationStatus(_ context.Context) (map[string]*utiltypes.ServeApplicationStatus, error) {
	return r.multiAppStatuses, nil
}

func (r *FakeRayDashboardClient) GetServeDetails(_ context.Context) (*utiltypes.ServeDetails, error) {
	return &r.serveDetails, nil
}

func (r *FakeRayDashboardClient) SetMultiApplicationStatuses(statuses map[string]*utiltypes.ServeApplicationStatus) {
	r.multiAppStatuses = statuses
}

func (r *FakeRayDashboardClient) GetJobInfo(ctx context.Context, jobId string) (*utiltypes.RayJobInfo, error) {
	if mock := r.GetJobInfoMock.Load(); mock != nil {
		return (*mock)(ctx, jobId)
	}
	return &utiltypes.RayJobInfo{JobStatus: rayv1.JobStatusRunning}, nil
}

func (r *FakeRayDashboardClient) ListJobs(ctx context.Context) (*[]utiltypes.RayJobInfo, error) {
	if mock := r.GetJobInfoMock.Load(); mock != nil {
		info, err := (*mock)(ctx, "job_id")
		if err != nil {
			return nil, err
		}
		return &[]utiltypes.RayJobInfo{*info}, nil
	}
	return nil, nil
}

func (r *FakeRayDashboardClient) SubmitJob(_ context.Context, _ *rayv1.RayJob) (jobId string, err error) {
	return "", nil
}

func (r *FakeRayDashboardClient) SubmitJobReq(_ context.Context, _ *utiltypes.RayJobRequest) (string, error) {
	return "", nil
}

func (r *FakeRayDashboardClient) GetJobLog(_ context.Context, _ string) (*string, error) {
	lg := "log"
	return &lg, nil
}

func (r *FakeRayDashboardClient) StopJob(_ context.Context, _ string) (err error) {
	return nil
}

func (r *FakeRayDashboardClient) DeleteJob(_ context.Context, _ string) error {
	return nil
}
