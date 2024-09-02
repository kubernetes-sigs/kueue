package utils

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"

	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
)

type FakeRayDashboardClient struct {
	multiAppStatuses map[string]*ServeApplicationStatus
	GetJobInfoMock   atomic.Pointer[func(context.Context, string) (*RayJobInfo, error)]
	BaseDashboardClient
	serveDetails ServeDetails
}

var _ RayDashboardClientInterface = (*FakeRayDashboardClient)(nil)

func (r *FakeRayDashboardClient) InitClient(_ context.Context, url string, _ *rayv1.RayCluster) error {
	r.client = &http.Client{}
	r.dashboardURL = "http://" + url
	return nil
}

func (r *FakeRayDashboardClient) UpdateDeployments(_ context.Context, _ []byte) error {
	fmt.Print("UpdateDeployments fake succeeds.")
	return nil
}

func (r *FakeRayDashboardClient) GetMultiApplicationStatus(_ context.Context) (map[string]*ServeApplicationStatus, error) {
	return r.multiAppStatuses, nil
}

func (r *FakeRayDashboardClient) GetServeDetails(_ context.Context) (*ServeDetails, error) {
	return &r.serveDetails, nil
}

func (r *FakeRayDashboardClient) SetMultiApplicationStatuses(statuses map[string]*ServeApplicationStatus) {
	r.multiAppStatuses = statuses
}

func (r *FakeRayDashboardClient) GetJobInfo(ctx context.Context, jobId string) (*RayJobInfo, error) {
	if mock := r.GetJobInfoMock.Load(); mock != nil {
		return (*mock)(ctx, jobId)
	}
	return &RayJobInfo{JobStatus: rayv1.JobStatusRunning}, nil
}

func (r *FakeRayDashboardClient) ListJobs(ctx context.Context) (*[]RayJobInfo, error) {
	if mock := r.GetJobInfoMock.Load(); mock != nil {
		info, err := (*mock)(ctx, "job_id")
		if err != nil {
			return nil, err
		}
		return &[]RayJobInfo{*info}, nil
	}
	return nil, nil
}

func (r *FakeRayDashboardClient) SubmitJob(_ context.Context, _ *rayv1.RayJob) (jobId string, err error) {
	return "", nil
}

func (r *FakeRayDashboardClient) SubmitJobReq(_ context.Context, _ *RayJobRequest, _ *string) (string, error) {
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
