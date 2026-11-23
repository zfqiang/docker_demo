package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"github.com/zeromicro/go-zero/core/logx"
	"github.com/zfqiang/payment-system/internal/config"
	"github.com/zfqiang/payment-system/internal/queue"
	"github.com/zfqiang/payment-system/internal/repository"
)

func main() {
	cfg := config.Load()
	// 启动探测使用独立的短时上下文，防止数据库或 Redis 连接挂起进程。
	startup, cancelStartup := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelStartup()

	store, err := repository.OpenMySQL(startup, cfg.MySQLDSN)
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()
	redisClient := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr, Password: cfg.RedisPassword})
	if err := redisClient.Ping(startup).Err(); err != nil {
		log.Fatalf("connect redis: %v", err)
	}
	defer redisClient.Close()

	// Redis 配置同时供 Asynq 客户端、Asynq Server 和项目出的 Redis 客户端使用。
	redisOpt := asynq.RedisClientOpt{Addr: cfg.RedisAddr, Password: cfg.RedisPassword}
	asynqClient := asynq.NewClient(redisOpt)
	defer asynqClient.Close()
	// HTTP 通知器负责把订单已支付事件发送到订单服务，并携带幂等键和 trace ID。
	notifier := queue.NewHTTPOrderNotifier(cfg.OrderServiceURL)
	// Asynq Server 的并发数为 10；default 队列权重高于 dead 队列。
	server := asynq.NewServer(redisOpt, asynq.Config{
		Concurrency: 10,
		Queues:      map[string]int{"default": 5, "dead": 1},
	})
	mux := queue.NewMux(notifier, asynqClient, store)
	if err := server.Start(mux); err != nil {
		log.Fatalf("start asynq worker: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	go queue.RunOutboxDispatcher(ctx, store, asynqClient)
	logx.Info("payment worker started")
	<-ctx.Done()
	server.Shutdown()
}
