package handlers

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lbenicio/goreportcard/storage"
	"github.com/redis/go-redis/v9"
)

const (
	analysisQueueKey      = "goreportcard:analysis:queue:v1"
	analysisProcessingKey = "goreportcard:analysis:processing:v1"
	analysisActiveKey     = "goreportcard:analysis:active:v1"
	analysisWorkerLockKey = "goreportcard:analysis:worker-lock:v1"
	analysisJobKeyPrefix  = "goreportcard:analysis:job:v1:"
	analysisWorkerLease   = time.Minute
	analysisJobPollDelay  = time.Second
	analysisStatusTTL     = 30 * time.Minute
)

var (
	enqueueAnalysisScript = redis.NewScript(`
local state = redis.call("HGET", KEYS[1], "state")
if state == "queued" or state == "running" then
	return 0
end
if state and ARGV[1] == "false" then
	return 0
end
if redis.call("ZCARD", KEYS[3]) >= tonumber(ARGV[2]) then
	return -1
end
redis.call("DEL", KEYS[1])
redis.call("HSET", KEYS[1],
	"repo", ARGV[3],
	"state", "queued",
	"force_refresh", ARGV[4],
	"updated_at", ARGV[5])
redis.call("ZADD", KEYS[3], ARGV[5], ARGV[3])
redis.call("LPUSH", KEYS[2], ARGV[3])
return 1
`)
	markAnalysisRunningScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) ~= ARGV[1] then
	return 0
end
if redis.call("EXISTS", KEYS[2]) == 0 then
	return -1
end
local state = redis.call("HGET", KEYS[2], "state")
if state == "done" or state == "failed" then
	return 2
end
redis.call("HSET", KEYS[2], "state", "running", "updated_at", ARGV[2])
return 1
`)
	completeAnalysisScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) ~= ARGV[1] then
	return 0
end
redis.call("HSET", KEYS[2],
	"state", ARGV[2],
	"report", ARGV[3],
	"error", ARGV[4],
	"completed_at", ARGV[5],
	"updated_at", ARGV[5])
redis.call("EXPIRE", KEYS[2], ARGV[6])
redis.call("ZREM", KEYS[3], ARGV[7])
redis.call("LREM", KEYS[4], 1, ARGV[7])
redis.call("DEL", KEYS[1])
return 1
`)
	renewAnalysisLeaseScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) ~= ARGV[1] then
	return 0
end
return redis.call("PEXPIRE", KEYS[1], ARGV[2])
`)
	releaseAnalysisLeaseScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
	return redis.call("DEL", KEYS[1])
end
return 0
`)
	cleanupAnalysisItemScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) ~= ARGV[1] then
	return 0
end
redis.call("LREM", KEYS[2], 1, ARGV[2])
redis.call("ZREM", KEYS[3], ARGV[2])
redis.call("DEL", KEYS[1])
return 1
`)
)

type redisAnalysisManager struct {
	client      *redis.Client
	store       storage.Store
	analyze     func(context.Context, storage.Store, string, bool) (checksResp, error)
	workerToken func() (string, error)
	workerLease time.Duration
	pollDelay   time.Duration
	ctx         context.Context
	cancel      context.CancelFunc
	workers     sync.WaitGroup
}

type redisAnalysisManagerOptions struct {
	analyze     func(context.Context, storage.Store, string, bool) (checksResp, error)
	workerToken func() (string, error)
	workerLease time.Duration
	pollDelay   time.Duration
	startWorker bool
}

// ConfigureRedisAnalysisJobs starts a single globally coordinated analysis worker
// and stores job state in Redis so any app instance can serve poll requests.
func ConfigureRedisAnalysisJobs(ctx context.Context, redisURL string, store storage.Store) (func() error, error) {
	manager, err := newRedisAnalysisManager(ctx, redisURL, store)
	if err != nil {
		return nil, err
	}
	previous := activeAnalysisJobs
	activeAnalysisJobs = manager
	return func() error {
		if activeAnalysisJobs == manager {
			activeAnalysisJobs = previous
		}
		return manager.Close()
	}, nil
}

// UseInMemoryAnalysisJobs is intended for a single local application process
// when REDIS_URL is not configured.
func UseInMemoryAnalysisJobs() {
	activeAnalysisJobs = repositoryAnalysisJobs
}

func newRedisAnalysisManager(ctx context.Context, redisURL string, store storage.Store) (*redisAnalysisManager, error) {
	return newRedisAnalysisManagerWithOptions(ctx, redisURL, store, redisAnalysisManagerOptions{
		analyze:     runRepositoryAnalysisContext,
		workerLease: analysisWorkerLease,
		pollDelay:   analysisJobPollDelay,
		startWorker: true,
	})
}

func newRedisAnalysisManagerWithOptions(ctx context.Context, redisURL string, store storage.Store, options redisAnalysisManagerOptions) (*redisAnalysisManager, error) {
	if strings.TrimSpace(redisURL) == "" {
		return nil, errors.New("REDIS_URL is required for shared analysis jobs")
	}
	clientOptions, err := redis.ParseURL(strings.TrimSpace(redisURL))
	if err != nil {
		return nil, fmt.Errorf("parse REDIS_URL for analysis jobs: %w", err)
	}
	clientOptions.DialTimeout = 2 * time.Second
	clientOptions.ReadTimeout = 3 * time.Second
	clientOptions.WriteTimeout = 3 * time.Second
	clientOptions.PoolTimeout = 3 * time.Second
	clientOptions.MaxRetries = -1

	client := redis.NewClient(clientOptions)
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("connect to Redis for analysis jobs: %w", err)
	}
	workerCtx, cancel := context.WithCancel(context.Background())
	if options.analyze == nil {
		options.analyze = runRepositoryAnalysisContext
	}
	if options.workerToken == nil {
		options.workerToken = analysisWorkerToken
	}
	if options.workerLease <= 0 {
		options.workerLease = analysisWorkerLease
	}
	if options.pollDelay <= 0 {
		options.pollDelay = analysisJobPollDelay
	}
	manager := &redisAnalysisManager{
		client:      client,
		store:       store,
		analyze:     options.analyze,
		workerToken: options.workerToken,
		workerLease: options.workerLease,
		pollDelay:   options.pollDelay,
		ctx:         workerCtx,
		cancel:      cancel,
	}
	if options.startWorker {
		manager.startWorker()
	}
	return manager, nil
}

func (m *redisAnalysisManager) startWorker() {
	m.workers.Add(1)
	go m.runWorker()
}

func (m *redisAnalysisManager) Close() error {
	m.cancel()
	finished := make(chan struct{})
	go func() {
		m.workers.Wait()
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		log.Printf("WARNING: analysis worker did not stop within five seconds")
	}
	return m.client.Close()
}

func (m *redisAnalysisManager) startRequest(ctx context.Context, repo string, restartCompleted, forceRefresh bool, _ func() (checksResp, error)) error {
	key := analysisJobKey(repo)
	result, err := enqueueAnalysisScript.Run(ctx, m.client, []string{key, analysisQueueKey, analysisActiveKey},
		strconv.FormatBool(restartCompleted), maxActiveAnalyses, repo, strconv.FormatBool(forceRefresh), time.Now().UnixMilli()).Int()
	if err != nil {
		return fmt.Errorf("enqueue repository analysis: %w", err)
	}
	if result < 0 {
		return errAnalysisQueueFull
	}
	return nil
}

func (m *redisAnalysisManager) statusRequest(ctx context.Context, repo string) (analysisJobSnapshot, bool, error) {
	values, err := m.client.HGetAll(ctx, analysisJobKey(repo)).Result()
	if err != nil {
		return analysisJobSnapshot{}, false, fmt.Errorf("read repository analysis status: %w", err)
	}
	if len(values) == 0 {
		return analysisJobSnapshot{}, false, nil
	}
	snapshot := analysisJobSnapshot{done: values["state"] == "done" || values["state"] == "failed"}
	if encoded := values["report"]; encoded != "" {
		if err := json.Unmarshal([]byte(encoded), &snapshot.report); err != nil {
			return analysisJobSnapshot{}, false, fmt.Errorf("decode repository analysis result: %w", err)
		}
	}
	if message := values["error"]; message != "" {
		snapshot.err = errors.New(message)
	}
	return snapshot, true, nil
}

func (m *redisAnalysisManager) runWorker() {
	defer m.workers.Done()
	for m.ctx.Err() == nil {
		token, err := m.workerToken()
		if err != nil {
			log.Printf("ERROR: could not create analysis worker token: %v", err)
			m.waitForWork()
			continue
		}
		acquired, err := m.client.SetNX(m.ctx, analysisWorkerLockKey, token, m.workerLease).Result()
		if err != nil {
			if m.ctx.Err() == nil {
				log.Printf("WARNING: could not acquire analysis worker lease: %v", err)
			}
			m.waitForWork()
			continue
		}
		if !acquired {
			m.waitForWork()
			continue
		}
		processed, err := m.processOne(token)
		if err != nil && m.ctx.Err() == nil {
			log.Printf("WARNING: could not process queued repository analysis: %v", err)
		}
		if !processed {
			m.waitForWork()
		}
	}
}

func (m *redisAnalysisManager) processOne(token string) (bool, error) {
	finishedByScript := false
	defer func() {
		if !finishedByScript {
			m.releaseLease(token)
		}
	}()

	repo, err := m.client.LIndex(m.ctx, analysisProcessingKey, 0).Result()
	if errors.Is(err, redis.Nil) {
		repo, err = m.client.LMove(m.ctx, analysisQueueKey, analysisProcessingKey, "RIGHT", "LEFT").Result()
	}
	if errors.Is(err, redis.Nil) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	values, err := m.client.HGetAll(m.ctx, analysisJobKey(repo)).Result()
	if err != nil {
		return false, err
	}
	if len(values) == 0 || values["state"] == "done" || values["state"] == "failed" {
		_, err = cleanupAnalysisItemScript.Run(m.ctx, m.client, []string{analysisWorkerLockKey, analysisProcessingKey, analysisActiveKey}, token, repo).Result()
		finishedByScript = err == nil
		return true, err
	}

	marked, err := markAnalysisRunningScript.Run(m.ctx, m.client, []string{analysisWorkerLockKey, analysisJobKey(repo)}, token, time.Now().UnixMilli()).Int()
	if err != nil {
		return false, err
	}
	if marked != 1 {
		if marked == 2 || marked == -1 {
			_, err = cleanupAnalysisItemScript.Run(m.ctx, m.client, []string{analysisWorkerLockKey, analysisProcessingKey, analysisActiveKey}, token, repo).Result()
			finishedByScript = err == nil
			return true, err
		}
		return false, errors.New("analysis worker lease was lost before starting a job")
	}

	forceRefresh, _ := strconv.ParseBool(values["force_refresh"])
	workCtx, cancelWork := context.WithCancel(m.ctx)
	heartbeatDone := make(chan struct{})
	go m.renewLease(workCtx, cancelWork, token, heartbeatDone)
	report, runErr := runAnalysisSafely(workCtx, m.analyze, m.store, repo, forceRefresh)
	cancelWork()
	<-heartbeatDone
	if m.ctx.Err() != nil {
		return false, nil
	}

	state := "done"
	encodedReport := ""
	errorText := ""
	if runErr != nil {
		state = "failed"
		errorText = runErr.Error()
	} else {
		encoded, err := json.Marshal(report)
		if err != nil {
			state = "failed"
			errorText = fmt.Sprintf("encode repository analysis result: %v", err)
		} else {
			encodedReport = string(encoded)
		}
	}
	completeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	completed, err := completeAnalysisScript.Run(completeCtx, m.client,
		[]string{analysisWorkerLockKey, analysisJobKey(repo), analysisActiveKey, analysisProcessingKey},
		token, state, encodedReport, errorText, time.Now().UnixMilli(), int(analysisStatusTTL.Seconds()), repo).Int()
	if err != nil {
		return false, err
	}
	if completed != 1 {
		return false, errors.New("analysis worker lease was lost before saving the result")
	}
	finishedByScript = true
	return true, nil
}

func (m *redisAnalysisManager) renewLease(ctx context.Context, cancel context.CancelFunc, token string, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(m.workerLease / 3)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			renewCtx, stop := context.WithTimeout(ctx, 5*time.Second)
			renewed, err := renewAnalysisLeaseScript.Run(renewCtx, m.client, []string{analysisWorkerLockKey}, token, m.workerLease.Milliseconds()).Int()
			stop()
			if err != nil {
				log.Printf("WARNING: could not renew analysis worker lease: %v", err)
				continue
			}
			if renewed != 1 {
				log.Printf("WARNING: analysis worker lease expired while processing a job")
				cancel()
				return
			}
		}
	}
}

func (m *redisAnalysisManager) releaseLease(token string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := releaseAnalysisLeaseScript.Run(ctx, m.client, []string{analysisWorkerLockKey}, token).Err(); err != nil && !errors.Is(err, redis.ErrClosed) {
		log.Printf("WARNING: could not release analysis worker lease: %v", err)
	}
}

func (m *redisAnalysisManager) waitForWork() {
	timer := time.NewTimer(m.pollDelay)
	defer timer.Stop()
	select {
	case <-m.ctx.Done():
	case <-timer.C:
	}
}

func runAnalysisSafely(ctx context.Context, analyze func(context.Context, storage.Store, string, bool) (checksResp, error), store storage.Store, repo string, forceRefresh bool) (report checksResp, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("analysis failed unexpectedly: %v", recovered)
		}
	}()
	return analyze(ctx, store, repo, forceRefresh)
}

func analysisJobKey(repo string) string {
	sum := sha256.Sum256([]byte(repo))
	return analysisJobKeyPrefix + hex.EncodeToString(sum[:])
}

func analysisWorkerToken() (string, error) {
	return analysisWorkerTokenFrom(rand.Reader)
}

func analysisWorkerTokenFrom(reader io.Reader) (string, error) {
	var token [16]byte
	if _, err := io.ReadFull(reader, token[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(token[:]), nil
}
