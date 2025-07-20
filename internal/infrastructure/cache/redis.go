// Package cache provides a Redis-based cache implementation for the application.
// Best practices: Uses context for cancellation, error wrapping for diagnostics, and TTL for expiration.
package cache

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

type RedisCache struct {
	client *redis.Client
}

func NewRedisCache(client *redis.Client) *RedisCache {
	return &RedisCache{client: client}
}

// CacheSentMessage caches the sent message ID with its sent time, using a 24-hour TTL.
func (c *RedisCache) CacheSentMessage(ctx context.Context, messageID string, sentTime time.Time) error {
	err := c.client.Set(ctx, messageID, sentTime.Format(time.RFC3339), 24*time.Hour).Err()
	if err != nil {
		return fmt.Errorf("failed to cache sent message %s: %w", messageID, err)
	}
	return nil
}

// Set stores a key-value pair in Redis with optional expiration (0 for no expiration).
// Uses RFC3339 for time values to ensure consistency.
// Returns a wrapped error on failure.
func (c *RedisCache) Set(ctx context.Context, key string, value interface{}, expiration time.Duration) error {
	var val string
	switch v := value.(type) {
	case time.Time:
		val = v.Format(time.RFC3339) // Standardize time serialization
	default:
		val = fmt.Sprintf("%v", v)
	}
	err := c.client.Set(ctx, key, val, expiration).Err()
	if err != nil {
		return fmt.Errorf("failed to set cache key %s: %w", key, err)
	}
	return nil
}

func (c *RedisCache) Get(ctx context.Context, key string) (string, error) {
	val, err := c.client.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("failed to get cache key %s: %w", key, err)
	}
	return val, nil
}
