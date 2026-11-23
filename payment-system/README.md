# go-zero 支付微服务示例

面向本地开发的可运行支付服务骨架：go-zero HTTP API、MySQL 支付与事务 outbox、Redis 回调锁、Asynq 订单通知 Worker。渠道使用本地模拟实现，不连接真实支付平台；订单通知通过 HTTP RPC 适配器调用订单服务。项目只负责支付域，订单服务仍需提供被通知的接口。

## 架构与边界

```text
Vue ──JWT──> payment-api (go-zero REST)
                  ├── payment service ──> MySQL payments + payment_outbox
                  ├── provider interface ──> Alipay/WeChat adapter (local mock included)
                  └── Redis SET NX + token-checked Lua unlock (callback idempotency)
                                           │ transactional outbox dispatcher
                                           ▼
                                     Redis / Asynq
                                           │ retries, then dead queue
                                           ▼
                                  order-service notification
```

| 模块                                 | 职责                                            | Go 面试高频点                              |
| ------------------------------------ | ----------------------------------------------- | ------------------------------------------ |
| `api/`、`internal/handler`           | REST DTO、JWT、CORS、统一错误响应               | HTTP 中间件、鉴权与资源归属校验、API 设计  |
| `internal/service`                   | 支付用例、状态机、回调锁                        | 状态机、不变量、幂等、分布式锁租约与误删锁 |
| `internal/repository`、`migrations/` | GORM 数据访问、SQL 迁移、事务、唯一约束、outbox | 事务隔离、行锁、索引、事务消息/Outbox      |
| `internal/queue`、`cmd/worker`       | Outbox 投递、Asynq 重试、死信队列               | 至少一次投递、幂等消费、退避与死信         |
| `internal/channel`                   | 支付渠道适配和回调 HMAC 验签                    | 接口抽象、常量时间比较、外部系统边界       |
| `rpc/payment.proto`                  | 支付服务对订单/用户等服务暴露的 RPC 契约        | Protobuf、服务边界、向后兼容               |

`rpc/payment.proto` 是支付服务的 go-zero zrpc 契约，`rpc/order.proto` 定义下单校验和订单支付成功通知的订单服务边界；服务 API 的 goctl 定义见 `api/payment.api`。用户身份由用户/认证服务签发 JWT，支付服务验签并从 token 获取用户 ID，不信任请求体中的用户 ID。当前本地可执行闭环以 REST + Worker 为主，避免依赖未生成的 protobuf 代码和外部订单服务。生产接入时分别用 goctl 生成支付与订单 RPC 桩，并将创建支付前的订单归属/金额核验接入 `GetPayableOrder`。

支付金额为最小货币单位的整数（人民币分），不使用浮点数。订单号唯一约束充当创建支付的幂等键；相同订单重复请求仅在金额、币种、用户和支付方式完全相同时复用原支付单。状态仅允许 `pending -> success|failed|closed`，终态只允许同态重复回调。回调先获取 Redis 有租期互斥锁，再由 MySQL 行锁保证并发安全；成功状态与 outbox 事件在同一事务提交。Outbox claim 使用 `SKIP LOCKED`，即使进程在入队后崩溃，重复消息也由事件 ID 和订单服务幂等键兜底。

### 表和状态

建表脚本：`migrations/001_init.sql`。`payments` 保存支付流水、关联订单/用户、金额/币种、渠道、状态、渠道流水号和回调时间；`payment_outbox` 是支付成功后的持久化事件。Redis 锁只是并发协调，不能替代数据库事务、唯一索引和订单服务的幂等处理。

## 本地启动

需要 Docker Desktop、Docker Compose 和 Go 1.23+。

```powershell
Set-Location D:\allStudy\docker\payment-system
Copy-Item .env.example .env
docker compose up --build
```

服务地址：API `http://localhost:8080`，MySQL `localhost:3307`（容器内 3306），Redis `localhost:6379`。Compose 首次启动时初始化数据库；已有 `mysql_data` 卷不会重新执行初始化 SQL。若需重置本项目本地数据，先确认数据可丢弃，再执行 `docker compose down -v`。

不使用 Docker 运行 Go 程序时，先启动依赖：

```powershell
docker compose up -d mysql redis
$env:MYSQL_DSN = "payment:payment@tcp(localhost:3307)/payment?parseTime=true&charset=utf8mb4&loc=UTC"
$env:REDIS_ADDR = "localhost:6379"
go run .\cmd\api
go run .\cmd\worker
```

Docker Desktop 在 Windows 上通常使用 Linux 容器。Compose 的相对挂载（如 `./migrations/001_init.sql`）由 Compose 根据 `docker-compose.yml` 所在目录解析；不要将 Windows 反斜杠路径写进 YAML，也不要把 `D:\...` 作为 Linux 容器内路径。遇到挂载失败时检查 Docker Desktop 的目录共享权限，且避免将项目放在权限受限或同步软件实时锁定的目录。开发热重载可再挂载源码，但不要挂载宿主机 `vendor` 或 Windows 构建出的可执行文件覆盖 Linux 容器文件。

## API 使用

完整 OpenAPI 3.1 接口规范位于 [`openapi.yaml`](openapi.yaml)，可导入 Swagger UI、Postman 或其他兼容工具。规范包含请求参数、JWT 鉴权、回调 HMAC 签名、统一响应和各业务错误码。

项目已通过 Docker Compose 集成 Swagger UI：

```powershell
docker compose up --build
```

访问 `http://localhost:8082/docs` 查看接口文档并调用 API。Swagger UI 挂载项目根目录的 [`openapi.yaml`](openapi.yaml)，修改规范文件后刷新页面即可生效，不需要维护生成副本。

仅启动文档工具时，也可使用：

```powershell
docker run --rm -p 8082:8080 -e SWAGGER_JSON=/spec/openapi.yaml -v "${PWD}/openapi.yaml:/spec/openapi.yaml:ro" swaggerapi/swagger-ui
```

默认开发密钥仅用于本机。Compose 中务必替换 `JWT_SECRET` 和 `CALLBACK_SECRET`，勿将其用于线上。生成 24 小时开发 JWT：

```powershell
$env:JWT_SECRET = "local-development-secret-change-me"
go run .\cmd\token demo-user
```

创建支付单（金额 1299 表示 ¥12.99）：

```powershell
curl.exe -X POST http://localhost:8080/v1/payments `
  -H "Authorization: Bearer <JWT>" -H "Content-Type: application/json" `
  -d "{\"orderId\":\"order-1001\",\"amount\":1299,\"method\":\"alipay\"}"
```

查询：`GET /v1/payments/{paymentNo}`，只允许支付单所属用户读取。渠道回调 `POST /v1/callbacks/alipay` 或 `/v1/callbacks/wechat`，必须携带 `X-Callback-Signature`，值为对原始请求 body 用 `CALLBACK_SECRET` 计算的 HMAC-SHA256 十六进制值。回调 JSON 为 `{"paymentNo":"...","tradeNo":"...","result":"success"}`；本地模拟器不提供真实支付 UI。示例签名可用 PowerShell：

```powershell
$secret = "local-callback-secret-change-me"
$body = '{"paymentNo":"<PAYMENT_NO>","tradeNo":"mock-trade-1","result":"success"}'
$hmac = [System.Security.Cryptography.HMACSHA256]::new([Text.Encoding]::UTF8.GetBytes($secret))
$signature = [Convert]::ToHexString($hmac.ComputeHash([Text.Encoding]::UTF8.GetBytes($body))).ToLower()
curl.exe -X POST http://localhost:8080/v1/callbacks/alipay `
  -H "Content-Type: application/json" -H "X-Callback-Signature: $signature" -d $body
```

成功后 Worker 异步 POST 到订单服务 `ORDER_SERVICE_URL/internal/orders/payment-success`，请求带 `X-Idempotency-Key` 与 `X-Trace-ID`。订单服务未启动时任务按最多 5 次重试后转入 Asynq `dead` 队列，同时把完整事件和失败原因保存在 `payment_outbox`（`status=dead`）。补偿前先核对订单状态，再针对性执行 `UPDATE payment_outbox SET status='pending', claim_token=NULL, claimed_until=NULL, last_error=NULL WHERE event_key='<event-id>' AND status='dead';` 使其重新投递；不可直接将所有失败事件盲目重复执行。

`/healthz` 是存活检查，`/metrics` 暴露 Prometheus 格式请求计数；go-zero REST Server 亦可与项目级 Prometheus 采集集成。所有请求返回 `X-Trace-ID`，可由调用方传入以串联日志和通知事件。JWT 用户 ID 来自已验签 token，不能由请求体覆盖。CORS 仅允许 `WEB_ORIGINS` 配置的来源；生产环境须使用 HTTPS、密钥管理系统、真实渠道验签/证书轮换以及订单金额核验。

## goctl 定义、代码生成和模板

```powershell
go install github.com/zeromicro/go-zero/tools/goctl@latest
goctl api go -api .\api\payment.api -dir .\generated\api
goctl rpc protoc .\rpc\payment.proto --go_out=.\generated\rpc --go-grpc_out=.\generated\rpc --zrpc_out=.\generated\rpc
```

`templates/api/response.tpl` 是响应结构统一模板，响应格式为 `{code,message,data}`。goctl 模板覆盖版本需与所安装的 goctl 一致：先执行 `goctl template init` 获取该版本完整 API 模板，将仓库的响应结构并入对应的 handler 模板并覆盖该版本 goctl home 下的模板文件（goctl 对自定义模板文件名/目录结构有版本耦合，升级 goctl 时应重新 diff 模板）。生成代码应调用业务层并复用本项目的响应码语义，不要将生成目录与手写 `internal/` 逻辑混在一起。

## TDD 与验证

```powershell
go test ./...
go test -race ./...
go vet ./...
```

测试规范：先写失败用例，使用表驱动覆盖 `pending -> success/failed/closed`、同态重放、终态冲突；用锁接口替身验证锁竞争时绝不落库、获得锁后一定释放，并在 Redis 集成测试中验证租期和 compare-and-delete；用 `httptest.Server` 替代订单服务，验证 Asynq payload 的事件/trace 字段、重试次数、死信路由和重复通知幂等键。集成测试使用独立测试数据库/Redis，不能对开发数据执行清理。

当前单元测试覆盖状态机、JWT 和回调分布式锁边界。进一步的 Asynq 重试/死信端到端测试应在带 Redis 的集成测试环境运行；订单服务应按事件 ID 幂等消费。

## 成度描述

- 设计支付状态机和 MySQL 行级事务，使用订单唯一索引、支付单幂等语义及 Redis token 锁防并发回调造成重复入账。
- 以事务 outbox + Asynq 实现成功事件可靠投递、失败重试与死信隔离，体现最终一致性和异步削峰。
- 采用 HMAC 回调验签、JWT 用户身份校验、支付单资源归属校验、金额整数化及 trace ID 日志串联，覆盖支付资损防控基础。
- 使用 go-zero REST 服务、MySQL、Redis、Docker Compose，按 API / service / repository / worker 分层，并提供 proto RPC 契约。
