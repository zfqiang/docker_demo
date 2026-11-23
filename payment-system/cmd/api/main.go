package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"
	"github.com/zeromicro/go-zero/core/logx"
	"github.com/zeromicro/go-zero/rest"
	"github.com/zfqiang/payment-system/internal/channel"
	"github.com/zfqiang/payment-system/internal/config"
	"github.com/zfqiang/payment-system/internal/handler"
	"github.com/zfqiang/payment-system/internal/repository"
	"github.com/zfqiang/payment-system/internal/service"
)

func main() {

	// 容器环境由 Compose 注入；本地运行时可选加载工作目录中的 .env 文件。
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		log.Fatalf("load .env file: %v", err)
	}

	// Load 从环境变量读取运行配置；开发环境会使用 config.go 中的安全性较低的默认值。
	cfg := config.Load()
	// startup 上下文只用于启动阶段的依赖探测，超时后会同时取消其中的数据库和 Redis 操作。
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// 打开 MySQL 并保持连接池；store 被复用给支付服务及后续仓储操作。
	store, err := repository.OpenMySQL(ctx, cfg.MySQLDSN)
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()
	// 初始化 Redis 客户端并执行 Ping，确保回调分布式锁所依赖的中间件可用。
	redisClient := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr, Password: cfg.RedisPassword})
	if err := redisClient.Ping(ctx).Err(); err != nil {
		log.Fatalf("connect redis: %v", err)
	}
	defer redisClient.Close()

	// 本地渠道实现仅用于演示；provider 接口保证未来可替换为真实支付渠道适配器。
	provider := channel.NewLocalProvider(cfg.CallbackSecret)
	// 将存储、Redis 分布式锁和渠道适配器组合成支付业务服务。
	paymentService := service.NewPaymentService(store, service.NewRedisLocker(redisClient), provider)
	// HTTP 层负责请求校验、JWT 鉴权、CORS、统一响应和资源归属检查。
	api := handler.NewServer(paymentService, provider, cfg.JWTSecret, cfg.WebOrigins)
	// 将 HTTP_ADDR 分解为 go-zero Server 所需的监听主机和端口。
	host, port, err := listenAddress(cfg.HTTPAddr)
	if err != nil {
		log.Fatal(err)
	}
	// rest.Server 包装底层 HTTP 服务，并由 AddRoutes 注册本项目定义的路由。
	server, err := rest.NewServer(rest.RestConf{Host: host, Port: port})
	if err != nil {
		log.Fatalf("create go-zero HTTP server: %v", err)
	}
	defer server.Stop()
	server.AddRoutes(api.Routes())
	logx.Infof("payment API listening address=%s", net.JoinHostPort(host, strconv.Itoa(port)))
	// Start 会阻塞运行 HTTP 服务，直到进程收到关闭信号或发生内部退出。
	server.Start()
}

// listenAddress 解析 HTTP_ADDR，并对空主机和非法端口做显式校验，
// 避免 go-zero 在端口错误时产生难以定位的启动失败。
func listenAddress(address string) (string, int, error) {
	if !strings.Contains(address, ":") {
		return "", 0, fmt.Errorf("HTTP_ADDR must include a port: %q", address)
	}
	host, rawPort, err := net.SplitHostPort(address)
	if err != nil {
		return "", 0, fmt.Errorf("parse HTTP_ADDR: %w", err)
	}
	if host == "" {
		host = "0.0.0.0"
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("invalid HTTP port in HTTP_ADDR: %q", address)
	}
	return host, port, nil
}
