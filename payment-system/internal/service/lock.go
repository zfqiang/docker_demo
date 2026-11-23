package service

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// CallbackLocker 抽象回调互斥锁，使业务逻辑可测试且不绑定具体 Redis 实现。
type CallbackLocker interface {
	Acquire(context.Context, string, string, time.Duration) (bool, error)
	Release(context.Context, string, string) error
}

// RedisLocker 使用 Redis SET NX 实现带租期的回调分布式锁。
type RedisLocker struct {
	client *redis.Client
}

func NewRedisLocker(client *redis.Client) *RedisLocker {
	return &RedisLocker{client: client}
}

func (l *RedisLocker) Acquire(ctx context.Context, key, token string, ttl time.Duration) (bool, error) {
	// SET NX 原子地检查锁是否存在并设置值和租期，避免竞态重复获取。
	acquired, err := l.client.SetNX(ctx, key, token, ttl).Result()
	if err != nil {
		return false, fmt.Errorf("set callback lock: %w", err)
	}
	return acquired, nil
}

func (l *RedisLocker) Release(ctx context.Context, key, token string) error {
	// Lua 比较当前锁值与持有者令牌；仅令牌匹配时删除锁，防止误删其他回调持有的锁。
	const script = `if redis.call("GET", KEYS[1]) == ARGV[1] then
		return redis.call("DEL", KEYS[1])
	end
	return 0`
	if err := l.client.Eval(ctx, script, []string{key}, token).Err(); err != nil {
		return fmt.Errorf("delete callback lock: %w", err)
	}
	return nil
}
