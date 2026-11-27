package storage

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/lbenicio/goreportcard/src/check"
)

type memoryReportCache struct {
	values    map[string]string
	getErr    error
	setErr    error
	deleteErr error
	getCalls  int
	setCalls  int
	delCalls  int
	ttl       time.Duration
}

func (c *memoryReportCache) Get(_ context.Context, key string) (string, error) {
	c.getCalls++
	if c.getErr != nil {
		return "", c.getErr
	}
	value, ok := c.values[key]
	if !ok {
		return "", errCacheMiss
	}
	return value, nil
}

func (c *memoryReportCache) Set(_ context.Context, key, value string, ttl time.Duration) error {
	c.setCalls++
	c.ttl = ttl
	if c.setErr != nil {
		return c.setErr
	}
	if c.values == nil {
		c.values = make(map[string]string)
	}
	c.values[key] = value
	return nil
}

func (c *memoryReportCache) Delete(_ context.Context, key string) error {
	c.delCalls++
	if c.deleteErr != nil {
		return c.deleteErr
	}
	delete(c.values, key)
	return nil
}

func (*memoryReportCache) Close() error { return nil }

type memoryReportStore struct {
	report         Report
	getErr         error
	saveErr        error
	addRecentErr   error
	recentErr      error
	recent         []string
	getCalls       int
	saveCalls      int
	addRecentCalls int
	recentCalls    int
}

func (s *memoryReportStore) GetReport(_ context.Context, repo string) (Report, error) {
	s.getCalls++
	if s.getErr != nil {
		return Report{}, s.getErr
	}
	if s.report.Repo != repo {
		return Report{}, ErrReportNotFound
	}
	return s.report, nil
}

func (s *memoryReportStore) SaveReport(_ context.Context, report Report) error {
	s.saveCalls++
	if s.saveErr != nil {
		return s.saveErr
	}
	s.report = report
	return nil
}

func (s *memoryReportStore) AddRecent(context.Context, string) error {
	s.addRecentCalls++
	return s.addRecentErr
}
func (s *memoryReportStore) RecentRepos(context.Context) ([]string, error) {
	s.recentCalls++
	if s.recentErr != nil {
		return nil, s.recentErr
	}
	return append([]string(nil), s.recent...), nil
}
func (*memoryReportStore) HighScores(context.Context) (int64, []HighScore, error) {
	return 0, nil, nil
}

func TestRedisStoreGetReportUsesCachedReport(t *testing.T) {
	want := redisTestReport()
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	cache := &memoryReportCache{values: map[string]string{
		reportCacheKeyPrefix + want.Repo: string(encoded),
	}}
	base := &memoryReportStore{}
	store := &RedisStore{Store: base, cache: cache}

	got, err := store.GetReport(context.Background(), want.Repo)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GetReport() = %#v, want %#v", got, want)
	}
	if base.getCalls != 0 {
		t.Fatalf("PostgreSQL GetReport called %d times, want 0", base.getCalls)
	}
}

func TestRedisStoreCachesPostgresReport(t *testing.T) {
	want := redisTestReport()
	cache := &memoryReportCache{values: make(map[string]string)}
	base := &memoryReportStore{report: want}
	store := &RedisStore{Store: base, cache: cache}

	got, err := store.GetReport(context.Background(), want.Repo)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GetReport() = %#v, want %#v", got, want)
	}
	if base.getCalls != 1 {
		t.Fatalf("PostgreSQL GetReport called %d times, want 1", base.getCalls)
	}
	if cache.ttl != reportCacheTTL {
		t.Fatalf("cache TTL = %s, want %s", cache.ttl, reportCacheTTL)
	}
	var cached Report
	if err := json.Unmarshal([]byte(cache.values[reportCacheKeyPrefix+want.Repo]), &cached); err != nil {
		t.Fatalf("cached value is invalid JSON: %v", err)
	}
	if !reflect.DeepEqual(cached, want) {
		t.Fatalf("cached report = %#v, want %#v", cached, want)
	}
}

func TestRedisStoreFallsBackToPostgresWhenRedisReadFails(t *testing.T) {
	want := redisTestReport()
	cache := &memoryReportCache{getErr: errors.New("Redis unavailable")}
	base := &memoryReportStore{report: want}
	store := &RedisStore{Store: base, cache: cache}

	got, err := store.GetReport(context.Background(), want.Repo)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GetReport() = %#v, want %#v", got, want)
	}
	if base.getCalls != 1 {
		t.Fatalf("PostgreSQL GetReport called %d times, want 1", base.getCalls)
	}
}

func TestRedisStoreReturnsReportWhenRedisWriteFails(t *testing.T) {
	want := redisTestReport()
	cache := &memoryReportCache{values: make(map[string]string), setErr: errors.New("Redis unavailable")}
	base := &memoryReportStore{report: want}
	store := &RedisStore{Store: base, cache: cache}

	got, err := store.GetReport(context.Background(), want.Repo)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GetReport() = %#v, want %#v", got, want)
	}
}

func TestRedisStoreFallsBackForInvalidCachedReport(t *testing.T) {
	want := redisTestReport()
	cache := &memoryReportCache{values: map[string]string{
		reportCacheKeyPrefix + want.Repo: `{"repo":"github.com/another/repository"}`,
	}}
	base := &memoryReportStore{report: want}
	store := &RedisStore{Store: base, cache: cache}

	got, err := store.GetReport(context.Background(), want.Repo)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GetReport() = %#v, want %#v", got, want)
	}
	if base.getCalls != 1 {
		t.Fatalf("PostgreSQL GetReport called %d times, want 1", base.getCalls)
	}
}

func TestRedisStorePropagatesPostgresErrors(t *testing.T) {
	wantErr := errors.New("database unavailable")
	cache := &memoryReportCache{values: make(map[string]string)}
	base := &memoryReportStore{getErr: wantErr}
	store := &RedisStore{Store: base, cache: cache}

	_, err := store.GetReport(context.Background(), "github.com/example/repository")
	if !errors.Is(err, wantErr) {
		t.Fatalf("GetReport() error = %v, want %v", err, wantErr)
	}
	if cache.setCalls != 0 {
		t.Fatalf("Redis Set called %d times, want 0", cache.setCalls)
	}
}

func TestRedisStoreInvalidatesCacheAfterSuccessfulSave(t *testing.T) {
	report := redisTestReport()
	cache := &memoryReportCache{values: map[string]string{
		reportCacheKeyPrefix + report.Repo: "stale",
	}}
	base := &memoryReportStore{}
	store := &RedisStore{Store: base, cache: cache}

	if err := store.SaveReport(context.Background(), report); err != nil {
		t.Fatal(err)
	}
	if cache.delCalls != 1 {
		t.Fatalf("Redis Delete called %d times, want 1", cache.delCalls)
	}
	if _, ok := cache.values[reportCacheKeyPrefix+report.Repo]; ok {
		t.Fatal("stale report remained in cache")
	}
}

func TestRedisStoreDoesNotInvalidateWhenSaveFails(t *testing.T) {
	base := &memoryReportStore{saveErr: errors.New("database write failed")}
	cache := &memoryReportCache{}
	store := &RedisStore{Store: base, cache: cache}

	if err := store.SaveReport(context.Background(), redisTestReport()); err == nil {
		t.Fatal("SaveReport() error = nil, want database error")
	}
	if cache.delCalls != 0 {
		t.Fatalf("Redis Delete called %d times, want 0", cache.delCalls)
	}
}

func TestRedisStoreIgnoresCacheInvalidationFailure(t *testing.T) {
	base := &memoryReportStore{}
	cache := &memoryReportCache{deleteErr: errors.New("Redis unavailable")}
	store := &RedisStore{Store: base, cache: cache}

	if err := store.SaveReport(context.Background(), redisTestReport()); err != nil {
		t.Fatalf("SaveReport() error = %v, want nil after successful PostgreSQL write", err)
	}
}

func TestRedisStoreRecentReposUsesCachedList(t *testing.T) {
	want := []string{"github.com/example/one", "github.com/example/two"}
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	cache := &memoryReportCache{values: map[string]string{recentReposCacheKey: string(encoded)}}
	base := &memoryReportStore{}
	store := &RedisStore{Store: base, cache: cache}

	got, err := store.RecentRepos(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RecentRepos() = %v, want %v", got, want)
	}
	if base.recentCalls != 0 || cache.setCalls != 0 {
		t.Fatalf("cache hit fell through: database reads=%d Redis writes=%d, want 0", base.recentCalls, cache.setCalls)
	}
}

func TestRedisStoreRecentReposFallsBackAndCaches(t *testing.T) {
	want := []string{"github.com/example/one", "github.com/example/two"}
	tests := []struct {
		name  string
		cache *memoryReportCache
	}{
		{name: "miss", cache: &memoryReportCache{values: make(map[string]string)}},
		{name: "read error", cache: &memoryReportCache{getErr: errors.New("Redis unavailable")}},
		{name: "invalid JSON", cache: &memoryReportCache{values: map[string]string{recentReposCacheKey: "not JSON"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := &memoryReportStore{recent: want}
			store := &RedisStore{Store: base, cache: tt.cache}

			got, err := store.RecentRepos(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("RecentRepos() = %v, want %v", got, want)
			}
			if base.recentCalls != 1 || tt.cache.setCalls != 1 {
				t.Fatalf("fallback calls: database reads=%d Redis writes=%d, want 1 each", base.recentCalls, tt.cache.setCalls)
			}
			if tt.cache.ttl != recentReposCacheTTL {
				t.Fatalf("Redis TTL = %s, want %s", tt.cache.ttl, recentReposCacheTTL)
			}
			var cached []string
			if err := json.Unmarshal([]byte(tt.cache.values[recentReposCacheKey]), &cached); err != nil {
				t.Fatalf("cached value is invalid JSON: %v", err)
			}
			if !reflect.DeepEqual(cached, want) {
				t.Fatalf("cached repositories = %v, want %v", cached, want)
			}
		})
	}
}

func TestRedisStoreRecentReposKeepsDatabaseResultOnCacheWriteFailure(t *testing.T) {
	want := []string{"github.com/example/repository"}
	base := &memoryReportStore{recent: want}
	cache := &memoryReportCache{setErr: errors.New("Redis unavailable")}
	store := &RedisStore{Store: base, cache: cache}

	got, err := store.RecentRepos(context.Background())
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("RecentRepos() = (%v, %v), want (%v, nil)", got, err, want)
	}
}

func TestRedisStoreRecentReposPropagatesDatabaseError(t *testing.T) {
	wantErr := errors.New("database unavailable")
	base := &memoryReportStore{recentErr: wantErr}
	cache := &memoryReportCache{values: make(map[string]string)}
	store := &RedisStore{Store: base, cache: cache}

	if _, err := store.RecentRepos(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("RecentRepos() error = %v, want %v", err, wantErr)
	}
	if cache.setCalls != 0 {
		t.Fatalf("Redis Set called %d times, want 0", cache.setCalls)
	}
}

func TestRedisStoreAddRecentInvalidatesCacheBestEffort(t *testing.T) {
	databaseErr := errors.New("database unavailable")
	tests := []struct {
		name        string
		addErr      error
		deleteErr   error
		wantErr     error
		wantDeletes int
	}{
		{name: "successful write", wantDeletes: 1},
		{name: "database write failure", addErr: databaseErr, wantErr: databaseErr},
		{name: "cache invalidation failure", deleteErr: errors.New("Redis unavailable"), wantDeletes: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := &memoryReportStore{addRecentErr: tt.addErr}
			cache := &memoryReportCache{
				values:    map[string]string{recentReposCacheKey: "stale"},
				deleteErr: tt.deleteErr,
			}
			store := &RedisStore{Store: base, cache: cache}

			err := store.AddRecent(context.Background(), "github.com/example/repository")
			if (err == nil) != (tt.wantErr == nil) {
				t.Fatalf("AddRecent() error = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("AddRecent() error = %v, want %v", err, tt.wantErr)
			}
			if base.addRecentCalls != 1 || cache.delCalls != tt.wantDeletes {
				t.Fatalf("calls: database writes=%d Redis deletes=%d, want 1 and %d", base.addRecentCalls, cache.delCalls, tt.wantDeletes)
			}
			if tt.deleteErr == nil && tt.addErr == nil {
				if _, ok := cache.values[recentReposCacheKey]; ok {
					t.Fatal("stale recent repositories remained in cache")
				}
			}
		})
	}
}

func redisTestReport() Report {
	return Report{
		Repo:         "github.com/example/repository",
		ResolvedRepo: "github.com/example/repository",
		Average:      0.95,
		Grade:        check.Grade("A"),
		LastRefresh:  time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC),
		Checks: []check.Score{{
			Name: "gofmt",
			FileSummaries: []check.FileSummary{{
				Filename: "main.go",
				Errors:   []check.Error{{LineNumber: 7, ErrorString: "format issue"}},
			}},
		}},
	}
}
