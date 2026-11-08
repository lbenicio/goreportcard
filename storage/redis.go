package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	reportCacheKeyPrefix = "goreportcard:report:v1:"
	reportCacheTTL       = 5 * time.Minute
)

var errReportCacheMiss = errors.New("report cache miss")

type reportCache interface {
	Get(context.Context, string) (string, error)
	Set(context.Context, string, string, time.Duration) error
	Delete(context.Context, string) error
	Close() error
}

type redisReportCache struct {
	client *redis.Client
}

func (c *redisReportCache) Get(ctx context.Context, key string) (string, error) {
	value, err := c.client.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return "", errReportCacheMiss
	}
	return value, err
}

func (c *redisReportCache) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	return c.client.Set(ctx, key, value, ttl).Err()
}

func (c *redisReportCache) Delete(ctx context.Context, key string) error {
	return c.client.Del(ctx, key).Err()
}

func (c *redisReportCache) Close() error {
	return c.client.Close()
}

// RedisStore adds a best-effort Redis cache for reports while keeping Store as the source of truth.
type RedisStore struct {
	Store
	cache reportCache
}

// OpenRedisCache wraps store with a Redis report cache. Redis outages do not prevent the
// application from using the underlying store.
func OpenRedisCache(ctx context.Context, store Store, redisURL string) (*RedisStore, error) {
	if strings.TrimSpace(redisURL) == "" {
		return nil, errors.New("REDIS_URL is required")
	}

	options, err := redis.ParseURL(strings.TrimSpace(redisURL))
	if err != nil {
		return nil, fmt.Errorf("parse REDIS_URL: %w", err)
	}
	options.DialTimeout = 2 * time.Second
	options.ReadTimeout = time.Second
	options.WriteTimeout = time.Second
	options.PoolTimeout = time.Second
	options.MaxRetries = -1

	client := redis.NewClient(options)
	cached := &RedisStore{Store: store, cache: &redisReportCache{client: client}}
	if err := client.Ping(ctx).Err(); err != nil {
		log.Printf("WARNING: Redis is unavailable at startup; report reads will fall back to PostgreSQL: %v", err)
	}
	return cached, nil
}

func (s *RedisStore) GetReport(ctx context.Context, repo string) (Report, error) {
	key := reportCacheKeyPrefix + repo
	value, err := s.cache.Get(ctx, key)
	if err == nil {
		var report Report
		if json.Unmarshal([]byte(value), &report) == nil && report.Repo == repo {
			return report, nil
		}
		log.Printf("WARNING: invalid Redis report cache entry; loading report from PostgreSQL")
	} else if !errors.Is(err, errReportCacheMiss) {
		log.Printf("WARNING: Redis report cache read failed; loading report from PostgreSQL: %v", err)
	}

	report, err := s.Store.GetReport(ctx, repo)
	if err != nil {
		return Report{}, err
	}

	encoded, err := json.Marshal(report)
	if err != nil {
		log.Printf("WARNING: could not encode report for Redis cache: %v", err)
		return report, nil
	}
	if err := s.cache.Set(ctx, key, string(encoded), reportCacheTTL); err != nil {
		log.Printf("WARNING: could not write report to Redis cache: %v", err)
	}
	return report, nil
}

func (s *RedisStore) SaveReport(ctx context.Context, report Report) error {
	if err := s.Store.SaveReport(ctx, report); err != nil {
		return err
	}
	if err := s.cache.Delete(ctx, reportCacheKeyPrefix+report.Repo); err != nil {
		log.Printf("WARNING: could not invalidate Redis report cache: %v", err)
	}
	return nil
}

// Close closes the Redis client owned by the report cache.
func (s *RedisStore) Close() error {
	return s.cache.Close()
}
