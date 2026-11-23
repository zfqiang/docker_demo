package config

import "os"

// Config 汇总支付 API 和 Worker 所需的外部连接、密钥及服务地址配置。
type Config struct {
	HTTPAddr        string
	MySQLDSN        string
	RedisAddr       string
	RedisPassword   string
	JWTSecret       string
	CallbackSecret  string
	WebOrigins      string
	OrderServiceURL string
}

// Load 优先读取进程环境变量；未设置的键使用本地开发默认值。
func Load() Config {
	return Config{
		HTTPAddr:        getEnv("HTTP_ADDR", ":8080"),
		MySQLDSN:        getEnv("MYSQL_DSN", "payment:payment@tcp(localhost:3307)/payment?parseTime=true&charset=utf8mb4&loc=UTC"),
		RedisAddr:       getEnv("REDIS_ADDR", "localhost:6379"),
		RedisPassword:   os.Getenv("REDIS_PASSWORD"),
		JWTSecret:       getEnv("JWT_SECRET", "local-development-secret-change-me"),
		CallbackSecret:  getEnv("CALLBACK_SECRET", "local-callback-secret-change-me"),
		WebOrigins:      getEnv("WEB_ORIGINS", "http://localhost:5173"),
		OrderServiceURL: getEnv("ORDER_SERVICE_URL", "http://localhost:8081"),
	}
}

// getEnv 在环境变量为空时使用 fallback，使单机演示和 Docker Compose 可直接启动。
func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
