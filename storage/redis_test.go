package storage

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/gojp/goreportcard/check"
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
		return "", errReportCacheMiss
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
	report    Report
	getErr    error
	saveErr   error
	getCalls  int
	saveCalls int
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

func (*memoryReportStore) AddRecent(context.Context, string) error { return nil }
func (*memoryReportStore) RecentRepos(context.Context) ([]string, error) {
	return nil, nil
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
