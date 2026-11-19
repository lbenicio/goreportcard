package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lbenicio/goreportcard/storage"
	"github.com/redis/go-redis/v9"
)

type analysisJobTestStore struct{ storage.Store }

func newTestRedisAnalysisManager(t *testing.T, options redisAnalysisManagerOptions) *redisAnalysisManager {
	t.Helper()
	redisURL := os.Getenv("TEST_REDIS_URL")
	if redisURL == "" {
		t.Skip("TEST_REDIS_URL is not configured")
	}
	parsed, err := redis.ParseURL(redisURL)
	if err != nil {
		t.Fatalf("parse TEST_REDIS_URL: %v", err)
	}
	setupClient := redis.NewClient(parsed)
	if err := setupClient.FlushDB(context.Background()).Err(); err != nil {
		_ = setupClient.Close()
		t.Fatalf("flush test Redis database: %v", err)
	}
	if err := setupClient.Close(); err != nil {
		t.Fatalf("close test Redis setup client: %v", err)
	}
	manager, err := newRedisAnalysisManagerWithOptions(context.Background(), redisURL, &analysisJobTestStore{}, options)
	if err != nil {
		t.Fatalf("open Redis analysis manager: %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	return manager
}

func testAnalysisResult(ctx context.Context, _ storage.Store, repo string, force bool) (checksResp, error) {
	if err := ctx.Err(); err != nil {
		return checksResp{}, err
	}
	files := 1
	if force {
		files = 2
	}
	return checksResp{Repo: repo, Files: files, Grade: "A"}, nil
}

func acquireAnalysisTestLease(t *testing.T, manager *redisAnalysisManager, token string) {
	t.Helper()
	if err := manager.client.Set(context.Background(), analysisWorkerLockKey, token, manager.workerLease).Err(); err != nil {
		t.Fatalf("set worker lease: %v", err)
	}
}

func waitForRedisAnalysisStatus(t *testing.T, manager *redisAnalysisManager, repo string) analysisJobSnapshot {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		job, ok, err := manager.statusRequest(context.Background(), repo)
		if err != nil {
			t.Fatalf("read analysis status: %v", err)
		}
		if ok && job.done {
			return job
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("analysis %q did not finish", repo)
	return analysisJobSnapshot{}
}

func TestRedisAnalysisManagerConfigurationAndWorkerLifecycle(t *testing.T) {
	t.Run("configuration errors", func(t *testing.T) {
		if _, err := newRedisAnalysisManager(context.Background(), "", &analysisJobTestStore{}); err == nil || !strings.Contains(err.Error(), "REDIS_URL is required") {
			t.Fatalf("missing URL error = %v", err)
		}
		if _, err := newRedisAnalysisManager(context.Background(), "not-a-redis-url", &analysisJobTestStore{}); err == nil || !strings.Contains(err.Error(), "parse REDIS_URL") {
			t.Fatalf("invalid URL error = %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if _, err := newRedisAnalysisManager(ctx, "redis://127.0.0.1:1/0", &analysisJobTestStore{}); err == nil || !strings.Contains(err.Error(), "connect to Redis") {
			t.Fatalf("unreachable Redis error = %v", err)
		}
		if _, err := ConfigureRedisAnalysisJobs(ctx, "not-a-redis-url", &analysisJobTestStore{}); err == nil || !strings.Contains(err.Error(), "parse REDIS_URL") {
			t.Fatalf("configuration error = %v, want invalid URL error", err)
		}
	})

	manager := newTestRedisAnalysisManager(t, redisAnalysisManagerOptions{analyze: testAnalysisResult, pollDelay: 5 * time.Millisecond, workerLease: 200 * time.Millisecond, startWorker: true})
	previous := activeAnalysisJobs
	closeManager, err := ConfigureRedisAnalysisJobs(context.Background(), os.Getenv("TEST_REDIS_URL"), &analysisJobTestStore{})
	if err != nil {
		t.Fatalf("configure Redis analysis jobs: %v", err)
	}
	if _, ok := activeAnalysisJobs.(*redisAnalysisManager); !ok {
		t.Fatal("ConfigureRedisAnalysisJobs did not install the Redis backend")
	}
	if err := closeManager(); err != nil {
		t.Fatalf("close configured Redis jobs: %v", err)
	}
	if activeAnalysisJobs != previous {
		t.Fatal("closing configured Redis jobs did not restore the prior backend")
	}
	closeManager, err = ConfigureRedisAnalysisJobs(context.Background(), os.Getenv("TEST_REDIS_URL"), &analysisJobTestStore{})
	if err != nil {
		t.Fatalf("configure Redis jobs for replacement case: %v", err)
	}
	activeAnalysisJobs = repositoryAnalysisJobs
	if err := closeManager(); err != nil {
		t.Fatalf("close replaced Redis jobs: %v", err)
	}
	if activeAnalysisJobs != repositoryAnalysisJobs {
		t.Fatal("closing replaced Redis jobs overwrote the active backend")
	}
	UseInMemoryAnalysisJobs()
	if activeAnalysisJobs != repositoryAnalysisJobs {
		t.Fatal("UseInMemoryAnalysisJobs did not restore the in-memory backend")
	}

	const repo = "github.com/example/redis-worker"
	if err := manager.startRequest(context.Background(), repo, true, true, nil); err != nil {
		t.Fatalf("enqueue analysis: %v", err)
	}
	job := waitForRedisAnalysisStatus(t, manager, repo)
	if job.err != nil || job.report.Repo != repo || job.report.Files != 2 {
		t.Fatalf("completed job = (%+v, %v)", job.report, job.err)
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("close worker manager: %v", err)
	}
}

func TestRedisAnalysisEnqueueAndStatusCases(t *testing.T) {
	t.Run("active deduplication and capacity", func(t *testing.T) {
		manager := newTestRedisAnalysisManager(t, redisAnalysisManagerOptions{startWorker: false})
		ctx := context.Background()
		if err := manager.startRequest(ctx, "repo-one", true, true, nil); err != nil {
			t.Fatal(err)
		}
		if err := manager.startRequest(ctx, "repo-one", true, true, nil); err != nil {
			t.Fatalf("duplicate enqueue: %v", err)
		}
		if got, _ := manager.client.LLen(ctx, analysisQueueKey).Result(); got != 1 {
			t.Fatalf("queued duplicates = %d, want 1", got)
		}
		for i := 1; i < maxActiveAnalyses; i++ {
			if err := manager.client.ZAdd(ctx, analysisActiveKey, redis.Z{Score: float64(i), Member: "filler" + string(rune('a'+i))}).Err(); err != nil {
				t.Fatal(err)
			}
		}
		if err := manager.startRequest(ctx, "repo-full", true, true, nil); !errors.Is(err, errAnalysisQueueFull) {
			t.Fatalf("full queue error = %v, want queue-full error", err)
		}
	})

	t.Run("existing completed status is reused unless a POST restarts it", func(t *testing.T) {
		manager := newTestRedisAnalysisManager(t, redisAnalysisManagerOptions{startWorker: false})
		ctx := context.Background()
		const repo = "repo-completed"
		encoded, _ := json.Marshal(checksResp{Repo: repo, Files: 7})
		if err := manager.client.HSet(ctx, analysisJobKey(repo), "state", "done", "report", string(encoded)).Err(); err != nil {
			t.Fatal(err)
		}
		if err := manager.startRequest(ctx, repo, false, true, nil); err != nil {
			t.Fatal(err)
		}
		if got, _ := manager.client.LLen(ctx, analysisQueueKey).Result(); got != 0 {
			t.Fatalf("completed job was unexpectedly queued: %d", got)
		}
		job, ok, err := manager.statusRequest(ctx, repo)
		if err != nil || !ok || !job.done || job.report.Files != 7 {
			t.Fatalf("completed status = (%+v, %t, %v)", job, ok, err)
		}
		if err := manager.startRequest(ctx, repo, true, true, nil); err != nil {
			t.Fatalf("restart completed job: %v", err)
		}
		force, err := manager.client.HGet(ctx, analysisJobKey(repo), "force_refresh").Result()
		if err != nil || force != "true" {
			t.Fatalf("force_refresh = (%q, %v), want true", force, err)
		}
	})

	t.Run("missing, failed, malformed, and Redis error statuses", func(t *testing.T) {
		manager := newTestRedisAnalysisManager(t, redisAnalysisManagerOptions{startWorker: false})
		ctx := context.Background()
		if _, ok, err := manager.statusRequest(ctx, "missing"); err != nil || ok {
			t.Fatalf("missing status = (%t, %v)", ok, err)
		}
		if err := manager.client.HSet(ctx, analysisJobKey("failed"), "state", "failed", "error", "analysis failed").Err(); err != nil {
			t.Fatal(err)
		}
		failed, ok, err := manager.statusRequest(ctx, "failed")
		if err != nil || !ok || !failed.done || failed.err == nil || failed.err.Error() != "analysis failed" {
			t.Fatalf("failed status = (%+v, %t, %v)", failed, ok, err)
		}
		if err := manager.client.HSet(ctx, analysisJobKey("malformed"), "state", "done", "report", "not-json").Err(); err != nil {
			t.Fatal(err)
		}
		if _, _, err := manager.statusRequest(ctx, "malformed"); err == nil || !strings.Contains(err.Error(), "decode repository analysis result") {
			t.Fatalf("malformed report error = %v", err)
		}
		if err := manager.client.Close(); err != nil {
			t.Fatal(err)
		}
		if err := manager.startRequest(ctx, "closed", true, true, nil); err == nil || !strings.Contains(err.Error(), "enqueue repository analysis") {
			t.Fatalf("enqueue Redis error = %v", err)
		}
		if _, _, err := manager.statusRequest(ctx, "closed"); err == nil || !strings.Contains(err.Error(), "read repository analysis status") {
			t.Fatalf("status Redis error = %v", err)
		}
	})
}

func TestRedisAnalysisProcessOneRecoveryAndTerminalStates(t *testing.T) {
	ctx := context.Background()
	t.Run("no queued work releases the lease", func(t *testing.T) {
		manager := newTestRedisAnalysisManager(t, redisAnalysisManagerOptions{startWorker: false})
		acquireAnalysisTestLease(t, manager, "empty-worker")
		processed, err := manager.processOne("empty-worker")
		if err != nil || processed {
			t.Fatalf("process empty queue = (%t, %v)", processed, err)
		}
		if _, err := manager.client.Get(ctx, analysisWorkerLockKey).Result(); !errors.Is(err, redis.Nil) {
			t.Fatalf("empty queue retained lease: %v", err)
		}
	})

	for _, state := range []string{"missing", "done", "failed"} {
		t.Run("stale processing entry "+state, func(t *testing.T) {
			manager := newTestRedisAnalysisManager(t, redisAnalysisManagerOptions{startWorker: false})
			repo := "stale-" + state
			if err := manager.client.LPush(ctx, analysisProcessingKey, repo).Err(); err != nil {
				t.Fatal(err)
			}
			if err := manager.client.ZAdd(ctx, analysisActiveKey, redis.Z{Score: 1, Member: repo}).Err(); err != nil {
				t.Fatal(err)
			}
			if state != "missing" {
				if err := manager.client.HSet(ctx, analysisJobKey(repo), "state", state).Err(); err != nil {
					t.Fatal(err)
				}
			}
			acquireAnalysisTestLease(t, manager, "cleanup-worker")
			processed, err := manager.processOne("cleanup-worker")
			if err != nil || !processed {
				t.Fatalf("cleanup stale item = (%t, %v)", processed, err)
			}
			if got, _ := manager.client.LLen(ctx, analysisProcessingKey).Result(); got != 0 {
				t.Fatalf("processing list length = %d, want 0", got)
			}
		})
	}

	t.Run("interrupted running work is retried and completed", func(t *testing.T) {
		manager := newTestRedisAnalysisManager(t, redisAnalysisManagerOptions{analyze: testAnalysisResult, startWorker: false})
		const repo = "repo-recovered"
		if err := manager.client.HSet(ctx, analysisJobKey(repo), "state", "running", "force_refresh", "true").Err(); err != nil {
			t.Fatal(err)
		}
		if err := manager.client.LPush(ctx, analysisProcessingKey, repo).Err(); err != nil {
			t.Fatal(err)
		}
		if err := manager.client.ZAdd(ctx, analysisActiveKey, redis.Z{Score: 1, Member: repo}).Err(); err != nil {
			t.Fatal(err)
		}
		acquireAnalysisTestLease(t, manager, "recovery-worker")
		processed, err := manager.processOne("recovery-worker")
		if err != nil || !processed {
			t.Fatalf("recover job = (%t, %v)", processed, err)
		}
		job, ok, err := manager.statusRequest(ctx, repo)
		if err != nil || !ok || !job.done || job.report.Files != 2 {
			t.Fatalf("recovered status = (%+v, %t, %v)", job, ok, err)
		}
	})
}

func TestRedisAnalysisProcessOneFailuresAndLeaseOwnership(t *testing.T) {
	ctx := context.Background()
	setup := func(t *testing.T, analyze func(context.Context, storage.Store, string, bool) (checksResp, error)) (*redisAnalysisManager, string) {
		t.Helper()
		manager := newTestRedisAnalysisManager(t, redisAnalysisManagerOptions{analyze: analyze, startWorker: false})
		repo := "repo-error"
		if err := manager.startRequest(ctx, repo, true, true, nil); err != nil {
			t.Fatal(err)
		}
		acquireAnalysisTestLease(t, manager, "error-worker")
		return manager, repo
	}

	t.Run("analysis error is stored", func(t *testing.T) {
		wantErr := errors.New("analysis unavailable")
		manager, repo := setup(t, func(context.Context, storage.Store, string, bool) (checksResp, error) { return checksResp{}, wantErr })
		processed, err := manager.processOne("error-worker")
		if err != nil || !processed {
			t.Fatalf("failed analysis processing = (%t, %v)", processed, err)
		}
		job, ok, err := manager.statusRequest(ctx, repo)
		if err != nil || !ok || !job.done || job.err == nil || job.err.Error() != wantErr.Error() {
			t.Fatalf("failed analysis status = (%+v, %t, %v)", job, ok, err)
		}
	})

	t.Run("panic is converted to a failed job", func(t *testing.T) {
		manager, repo := setup(t, func(context.Context, storage.Store, string, bool) (checksResp, error) { panic("runner panic") })
		processed, err := manager.processOne("error-worker")
		if err != nil || !processed {
			t.Fatalf("panic processing = (%t, %v)", processed, err)
		}
		job, ok, err := manager.statusRequest(ctx, repo)
		if err != nil || !ok || job.err == nil || !strings.Contains(job.err.Error(), "analysis failed unexpectedly: runner panic") {
			t.Fatalf("panic status = (%+v, %t, %v)", job, ok, err)
		}
	})

	t.Run("unmarshalable result is stored as failure", func(t *testing.T) {
		manager, repo := setup(t, func(context.Context, storage.Store, string, bool) (checksResp, error) {
			return checksResp{LastRefresh: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}, nil
		})
		processed, err := manager.processOne("error-worker")
		if err != nil || !processed {
			t.Fatalf("unmarshalable result processing = (%t, %v)", processed, err)
		}
		job, ok, err := manager.statusRequest(ctx, repo)
		if err != nil || !ok || job.err == nil || !strings.Contains(job.err.Error(), "encode repository analysis result") {
			t.Fatalf("marshal failure status = (%+v, %t, %v)", job, ok, err)
		}
	})

	t.Run("lost lease cannot overwrite a new worker", func(t *testing.T) {
		var manager *redisAnalysisManager
		manager, _ = setup(t, func(context.Context, storage.Store, string, bool) (checksResp, error) {
			if err := manager.client.Set(ctx, analysisWorkerLockKey, "new-worker", time.Minute).Err(); err != nil {
				return checksResp{}, err
			}
			return checksResp{Repo: "repo-error"}, nil
		})
		_, err := manager.processOne("error-worker")
		if err == nil || !strings.Contains(err.Error(), "lease was lost") {
			t.Fatalf("lost lease error = %v", err)
		}
		if got, _ := manager.client.Get(ctx, analysisWorkerLockKey).Result(); got != "new-worker" {
			t.Fatalf("new worker lease = %q, want to remain owned", got)
		}
	})

	t.Run("canceled manager leaves processing entry for recovery", func(t *testing.T) {
		var manager *redisAnalysisManager
		manager, repo := setup(t, func(context.Context, storage.Store, string, bool) (checksResp, error) {
			manager.cancel()
			return checksResp{}, context.Canceled
		})
		processed, err := manager.processOne("error-worker")
		if err != nil || processed {
			t.Fatalf("canceled process = (%t, %v)", processed, err)
		}
		if got, _ := manager.client.LLen(ctx, analysisProcessingKey).Result(); got != 1 {
			t.Fatalf("interrupted item count = %d, want 1", got)
		}
		if got, err := manager.client.LIndex(ctx, analysisProcessingKey, 0).Result(); err != nil || got != repo {
			t.Fatalf("interrupted processing item = (%q, %v), want %q", got, err, repo)
		}
	})

	t.Run("queue and job Redis errors are returned", func(t *testing.T) {
		manager := newTestRedisAnalysisManager(t, redisAnalysisManagerOptions{startWorker: false})
		if err := manager.client.Set(ctx, analysisProcessingKey, "not-a-list", 0).Err(); err != nil {
			t.Fatal(err)
		}
		if _, err := manager.processOne("queue-error-worker"); err == nil {
			t.Fatal("processOne() error = nil for a malformed processing list")
		}

		const repo = "repo-bad-job-key"
		if err := manager.client.Del(ctx, analysisProcessingKey).Err(); err != nil {
			t.Fatal(err)
		}
		if err := manager.client.LPush(ctx, analysisProcessingKey, repo).Err(); err != nil {
			t.Fatal(err)
		}
		if err := manager.client.Set(ctx, analysisJobKey(repo), "not-a-hash", 0).Err(); err != nil {
			t.Fatal(err)
		}
		if _, err := manager.processOne("job-error-worker"); err == nil {
			t.Fatal("processOne() error = nil for a malformed job record")
		}
	})

	t.Run("lease missing before starting a job", func(t *testing.T) {
		manager, _ := setup(t, testAnalysisResult)
		if _, err := manager.processOne("missing-lease"); err == nil || !strings.Contains(err.Error(), "lease was lost before starting") {
			t.Fatalf("processOne() error = %v, want lost lease", err)
		}
	})

	t.Run("Redis failure while saving a completed result is returned", func(t *testing.T) {
		var manager *redisAnalysisManager
		manager, _ = setup(t, func(context.Context, storage.Store, string, bool) (checksResp, error) {
			return checksResp{}, manager.client.Close()
		})
		if _, err := manager.processOne("error-worker"); err == nil {
			t.Fatal("processOne() error = nil after Redis closed during analysis")
		}
	})
}

func TestRedisAnalysisLeaseRenewalAndHelpers(t *testing.T) {
	t.Run("lease renews while worker is active", func(t *testing.T) {
		manager := newTestRedisAnalysisManager(t, redisAnalysisManagerOptions{startWorker: false, workerLease: 90 * time.Millisecond, pollDelay: 2 * time.Millisecond})
		acquireAnalysisTestLease(t, manager, "renew-worker")
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go manager.renewLease(ctx, cancel, "renew-worker", done)
		time.Sleep(40 * time.Millisecond)
		if ttl, err := manager.client.PTTL(context.Background(), analysisWorkerLockKey).Result(); err != nil || ttl <= 60*time.Millisecond {
			t.Fatalf("renewed lease TTL = (%s, %v), want a refreshed lease", ttl, err)
		}
		cancel()
		<-done
	})

	t.Run("lost lease cancels worker and release preserves another owner", func(t *testing.T) {
		manager := newTestRedisAnalysisManager(t, redisAnalysisManagerOptions{startWorker: false, workerLease: 30 * time.Millisecond})
		ctx, cancel := context.WithCancel(context.Background())
		acquireAnalysisTestLease(t, manager, "other-worker")
		done := make(chan struct{})
		go manager.renewLease(ctx, cancel, "stale-worker", done)
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("lost-lease renewal did not stop")
		}
		manager.releaseLease("stale-worker")
		if got, _ := manager.client.Get(context.Background(), analysisWorkerLockKey).Result(); got != "other-worker" {
			t.Fatalf("lease after stale release = %q", got)
		}
	})

	t.Run("closed Redis renewal error and canceled work wait", func(t *testing.T) {
		manager := newTestRedisAnalysisManager(t, redisAnalysisManagerOptions{startWorker: false, workerLease: 15 * time.Millisecond, pollDelay: time.Millisecond})
		ctx, cancel := context.WithCancel(context.Background())
		if err := manager.client.Close(); err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go manager.renewLease(ctx, cancel, "token", done)
		time.Sleep(25 * time.Millisecond)
		cancel()
		<-done
		manager.cancel()
		manager.waitForWork()
	})

	t.Run("release reports unexpected Redis errors", func(t *testing.T) {
		manager := newTestRedisAnalysisManager(t, redisAnalysisManagerOptions{startWorker: false})
		if err := manager.client.LPush(context.Background(), analysisWorkerLockKey, "not-a-string").Err(); err != nil {
			t.Fatal(err)
		}
		manager.releaseLease("worker-token")
	})

	key := analysisJobKey("repo")
	if key == analysisJobKey("other") || key != analysisJobKey("repo") {
		t.Fatal("analysis job key is not a stable hash of the repository")
	}
	first, err := analysisWorkerToken()
	if err != nil || len(first) != 32 {
		t.Fatalf("worker token = (%q, %v), want 32 hex chars", first, err)
	}
	second, err := analysisWorkerToken()
	if err != nil || first == second {
		t.Fatalf("worker tokens are not unique: %q, %q, %v", first, second, err)
	}
}

type failingAnalysisJobStatusBackend struct{}

func (failingAnalysisJobStatusBackend) startRequest(context.Context, string, bool, bool, func() (checksResp, error)) error {
	return nil
}

func (failingAnalysisJobStatusBackend) statusRequest(context.Context, string) (analysisJobSnapshot, bool, error) {
	return analysisJobSnapshot{}, false, errors.New("status backend unavailable")
}

func TestWriteAnalysisJobStatusBackendError(t *testing.T) {
	response := httptest.NewRecorder()
	writeAnalysisJobStatusWithBackend(response, failingAnalysisJobStatusBackend{}, context.Background(), "repo")
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "status backend unavailable") {
		t.Fatalf("status response = (%d, %q), want 503 backend error", response.Code, response.Body.String())
	}
}

type analysisTokenErrorReader struct{}

func (analysisTokenErrorReader) Read([]byte) (int, error) {
	return 0, errors.New("random source unavailable")
}

func TestAnalysisWorkerTokenReadFailure(t *testing.T) {
	if token, err := analysisWorkerTokenFrom(analysisTokenErrorReader{}); err == nil || token != "" {
		t.Fatalf("worker token = (%q, %v), want read error", token, err)
	}
}

func TestRedisAnalysisWorkerLoopErrorAndLeaseWaitPaths(t *testing.T) {
	t.Run("token generation error waits and exits on cancellation", func(t *testing.T) {
		manager := newTestRedisAnalysisManager(t, redisAnalysisManagerOptions{
			startWorker: false,
			pollDelay:   2 * time.Millisecond,
			workerToken: func() (string, error) { return analysisWorkerTokenFrom(analysisTokenErrorReader{}) },
		})
		manager.startWorker()
		time.Sleep(10 * time.Millisecond)
		manager.cancel()
		manager.workers.Wait()
	})

	t.Run("Redis acquisition error waits and exits on cancellation", func(t *testing.T) {
		manager := newTestRedisAnalysisManager(t, redisAnalysisManagerOptions{startWorker: false, pollDelay: 2 * time.Millisecond})
		if err := manager.client.Close(); err != nil {
			t.Fatal(err)
		}
		manager.startWorker()
		time.Sleep(10 * time.Millisecond)
		manager.cancel()
		manager.workers.Wait()
	})

	t.Run("another worker's lease causes a wait", func(t *testing.T) {
		manager := newTestRedisAnalysisManager(t, redisAnalysisManagerOptions{startWorker: false, pollDelay: 2 * time.Millisecond})
		acquireAnalysisTestLease(t, manager, "existing-worker")
		manager.startWorker()
		time.Sleep(10 * time.Millisecond)
		manager.cancel()
		manager.workers.Wait()
	})

	t.Run("processing error is logged and retried", func(t *testing.T) {
		manager := newTestRedisAnalysisManager(t, redisAnalysisManagerOptions{startWorker: false, pollDelay: 2 * time.Millisecond})
		if err := manager.client.Set(context.Background(), analysisProcessingKey, "not-a-list", 0).Err(); err != nil {
			t.Fatal(err)
		}
		manager.startWorker()
		time.Sleep(10 * time.Millisecond)
		manager.cancel()
		manager.workers.Wait()
	})
}
