package dashboardclient

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/smallnest/chanx"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	utiltypes "github.com/ray-project/kuberay/ray-operator/controllers/ray/utils/types"
)

// ErrAgain EAGAIN means "there is no data available right now, try again later"
// https://stackoverflow.com/questions/4058368/what-does-eagain-mean
var ErrAgain = errors.New("EAGAIN")

const (
	// TODO: make worker size configurable.
	workerSize = 8

	queryInterval = 3 * time.Second

	// TODO: consider a proper size for accommodating the all live job info
	cacheSize   = 10000
	cacheExpiry = 10 * time.Minute

	initBufferSize = 128
)

var (
	// singleton
	initWorkPool sync.Once
	pool         workerPool

	// singleton
	initCacheStorage sync.Once
	cacheStorage     *lru.Cache[string, *JobInfoCache]
	rwLock           sync.RWMutex
)

type (
	// Task defines a unit of work for the worker pool and the return value indicate if it should re-queue or not.
	Task         func(taskCTX context.Context) bool
	JobInfoCache struct {
		JobInfo   *utiltypes.RayJobInfo
		Err       error
		UpdatedAt *time.Time
	}

	workerPool struct {
		taskQueue *chanx.UnboundedChan[Task]
	}
)

func (w *workerPool) start(ctx context.Context, numWorkers int, requeueDelay time.Duration) {
	logger := ctrl.LoggerFrom(ctx).WithName("WorkerPool")
	w.taskQueue = chanx.NewUnboundedChanSize[Task](ctx, 0, 0, initBufferSize)

	for i := range numWorkers {
		go func(workerID int) {
			for {
				select {
				case <-ctx.Done():
					logger.Info("worker exiting...", "workerID", workerID)
					return
				case task, ok := <-w.taskQueue.Out:
					if !ok {
						logger.Info("worker exiting from a closed channel", "workerID", workerID)
						return
					}
					shouldRequeue := task(ctx)

					if shouldRequeue && ctx.Err() == nil {
						time.AfterFunc(requeueDelay, func() {
							select {
							case <-ctx.Done():
								return
							case w.taskQueue.In <- task:
							}
						})
					}
				}
			}
		}(i)
	}
	logger.Info(fmt.Sprintf("Initialize a worker pool with %d goroutines and requeueDelay is %v.", numWorkers, requeueDelay))
}

func (w *workerPool) AddTask(task Task) {
	w.taskQueue.In <- task
}

var _ RayDashboardClientInterface = (*RayDashboardCacheClient)(nil)

type RayDashboardCacheClient struct {
	client         RayDashboardClientInterface
	namespacedName types.NamespacedName
}

func (r *RayDashboardCacheClient) InitClient(ctx context.Context, namespacedName types.NamespacedName, client RayDashboardClientInterface) {
	logger := ctrl.LoggerFrom(ctx).WithName("RayDashboardCacheClient")

	r.namespacedName = namespacedName

	initWorkPool.Do(func() {
		pool.start(ctx, workerSize, queryInterval)
	})

	initCacheStorage.Do(func() {
		// The NewWithEvict() returns error only if the cacheSize is less than or equal to zero.
		// While we set cacheSize as constant, we can ignore the error here.
		cacheStorage, _ = lru.NewWithEvict[string, *JobInfoCache](cacheSize, func(key string, _ *JobInfoCache) {
			logger.WithName("cacheStorage").Info("Evict cache for key.", "key", key)
		})

		// expiry cache cleanup
		go func() {
			ticker := time.NewTicker(queryInterval * 10)
			defer ticker.Stop()

			loggerForGC := logger.WithName("CacheCleanup")
			loggerForGC.Info(fmt.Sprintf("Initialize a cache cleanup goroutine with interval %v.", queryInterval*10))

			for {
				select {
				case <-ctx.Done():
					loggerForGC.Info("clean up goroutine exiting...")
					return
				case t := <-ticker.C:
					rwLock.RLock()
					keys := cacheStorage.Keys()
					rwLock.RUnlock()

					expiredThreshold := time.Now().Add(-cacheExpiry)
					loggerForGC.Info(fmt.Sprintf("Found %d keys to verify,", len(keys)), "expiredThreshold", expiredThreshold, "tick at", t)

					// zero allocate filtering
					removed := keys[:0]
					for _, key := range keys {
						rwLock.Lock()
						if cached, ok := cacheStorage.Peek(key); ok {
							if cached.UpdatedAt.Before(expiredThreshold) {
								cacheStorage.Remove(key)
								removed = append(removed, key)
							}
						}
						rwLock.Unlock()
					}
					loggerForGC.Info(fmt.Sprintf("clean up %d cache.", len(removed)), "expiredThreshold", expiredThreshold, "removed keys", removed)
				}
			}
		}()
	})

	r.client = client
}

func (r *RayDashboardCacheClient) UpdateDeployments(ctx context.Context, configJson []byte) error {
	return r.client.UpdateDeployments(ctx, configJson)
}

func (r *RayDashboardCacheClient) GetServeDetails(ctx context.Context) (*utiltypes.ServeDetails, error) {
	return r.client.GetServeDetails(ctx)
}

func (r *RayDashboardCacheClient) GetMultiApplicationStatus(ctx context.Context) (map[string]*utiltypes.ServeApplicationStatus, error) {
	return r.client.GetMultiApplicationStatus(ctx)
}

func (r *RayDashboardCacheClient) GetJobInfo(ctx context.Context, jobId string) (*utiltypes.RayJobInfo, error) {
	logger := ctrl.LoggerFrom(ctx).WithName("RayDashboardCacheClient")

	rwLock.Lock()
	if cached, ok := cacheStorage.Get(cacheKey(r.namespacedName, jobId)); ok {
		if cached.Err != nil && !errors.Is(cached.Err, ErrAgain) {
			// Consume the error.
			// If the RayJob is still exists, the next Reconcile iteration will put the task back for updating JobInfo
			cacheStorage.Remove(cacheKey(r.namespacedName, jobId))
			logger.Info("Consume the cached error for jobId", "jobId", jobId, "error", cached.Err, "cacheKey", cacheKey(r.namespacedName, jobId))
		}
		rwLock.Unlock()
		return cached.JobInfo, cached.Err
	}
	rwLock.Unlock()

	currentTime := time.Now()
	placeholder := &JobInfoCache{Err: ErrAgain, UpdatedAt: &currentTime}

	// Put a placeholder in storage. The cache will be updated only if the placeholder exists.
	// The placeholder will be removed when StopJob or DeleteJob.
	rwLock.Lock()
	if cached, existed, _ := cacheStorage.PeekOrAdd(cacheKey(r.namespacedName, jobId), placeholder); existed {
		rwLock.Unlock()
		return cached.JobInfo, cached.Err
	}
	rwLock.Unlock()

	var task Task = func(taskCTX context.Context) bool {
		rwLock.RLock()
		if existed := cacheStorage.Contains(cacheKey(r.namespacedName, jobId)); !existed {
			logger.Info("The placeholder is removed for jobId", "jobId", jobId, "cacheKey", cacheKey(r.namespacedName, jobId))
			rwLock.RUnlock()
			return false
		}
		rwLock.RUnlock()

		jobInfo, err := r.client.GetJobInfo(taskCTX, jobId)
		currentTime := time.Now()

		// Make this cache immutable to avoid data race between pointer updates and read operations.
		newJobInfoCache := &JobInfoCache{
			JobInfo:   jobInfo,
			Err:       err,
			UpdatedAt: &currentTime,
		}

		rwLock.Lock()
		if existed := cacheStorage.Contains(cacheKey(r.namespacedName, jobId)); !existed {
			logger.Info("The placeholder is removed before updating for jobId", "jobId", jobId, "cacheKey", cacheKey(r.namespacedName, jobId))
			rwLock.Unlock()
			return false
		}
		cacheStorage.Add(cacheKey(r.namespacedName, jobId), newJobInfoCache)
		rwLock.Unlock()

		if err != nil {
			// Exits the updating loop after getting an error.
			// If the RayJob still exists, Reconcile will consume the error and put the JobId back to updating loop again.
			logger.Info("Failed to fetch job info for jobId", "jobId", jobId, "error", err)
			return false
		}
		if newJobInfoCache.JobInfo == nil {
			return true
		}
		if rayv1.IsJobTerminal(newJobInfoCache.JobInfo.JobStatus) {
			logger.Info("The job reaches terminal status for jobId", "jobId", jobId,
				"cacheKey", cacheKey(r.namespacedName, jobId),
				"status", newJobInfoCache.JobInfo.JobStatus)
			return false
		}
		return true
	}

	pool.AddTask(task)

	logger.Info("Put a task to fetch job info in background for jobId ", "jobId", jobId, "cacheKey", cacheKey(r.namespacedName, jobId))

	return nil, ErrAgain
}

func (r *RayDashboardCacheClient) ListJobs(ctx context.Context) (*[]utiltypes.RayJobInfo, error) {
	return r.client.ListJobs(ctx)
}

func (r *RayDashboardCacheClient) SubmitJob(ctx context.Context, rayJob *rayv1.RayJob) (string, error) {
	return r.client.SubmitJob(ctx, rayJob)
}

func (r *RayDashboardCacheClient) SubmitJobReq(ctx context.Context, request *utiltypes.RayJobRequest) (string, error) {
	return r.client.SubmitJobReq(ctx, request)
}

func (r *RayDashboardCacheClient) GetJobLog(ctx context.Context, jobName string) (*string, error) {
	return r.client.GetJobLog(ctx, jobName)
}

func (r *RayDashboardCacheClient) StopJob(ctx context.Context, jobName string) error {
	rwLock.Lock()
	cacheStorage.Remove(cacheKey(r.namespacedName, jobName))
	rwLock.Unlock()

	return r.client.StopJob(ctx, jobName)
}

func (r *RayDashboardCacheClient) DeleteJob(ctx context.Context, jobName string) error {
	rwLock.Lock()
	cacheStorage.Remove(cacheKey(r.namespacedName, jobName))
	rwLock.Unlock()

	return r.client.DeleteJob(ctx, jobName)
}

func cacheKey(namespacedName types.NamespacedName, jobId string) string {
	return namespacedName.String() + string(types.Separator) + jobId
}
