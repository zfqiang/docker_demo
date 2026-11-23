package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/zeromicro/go-zero/core/logx"
	"github.com/zeromicro/go-zero/rest"
	"github.com/zfqiang/payment-system/internal/auth"
	"github.com/zfqiang/payment-system/internal/channel"
	"github.com/zfqiang/payment-system/internal/model"
	"github.com/zfqiang/payment-system/internal/repository"
	"github.com/zfqiang/payment-system/internal/service"
)

// PaymentService 定义 HTTP 层依赖的业务能力，便于替换实现并进行单元测试。
type PaymentService interface {
	Create(context.Context, string, string, int64, string, string) (*model.Payment, string, error)
	Get(context.Context, string) (*model.Payment, error)
	Callback(context.Context, string, string, string, string) (*model.Payment, error)
}

// Server 保存业务依赖、安全配置和进程级请求计数器。
type Server struct {
	payments       PaymentService
	provider       channel.Provider
	jwtSecret      string
	allowedOrigins map[string]bool
	requestCount   atomic.Uint64
}

func NewServer(payments PaymentService, provider channel.Provider, jwtSecret, origins string) *Server {
	// origins 按逗号拆分并去除空格，构建白名单以避免重复解析。
	allowed := make(map[string]bool)
	for _, origin := range strings.Split(origins, ",") {
		if origin = strings.TrimSpace(origin); origin != "" {
			allowed[origin] = true
		}
	}
	return &Server{
		payments: payments, provider: provider, jwtSecret: jwtSecret, allowedOrigins: allowed,
	}
}

// Routes 注册存活探测、指标、支付创建/查询和渠道回调路由。
func (s *Server) Routes() []rest.Route {
	return []rest.Route{
		{Method: http.MethodGet, Path: "/healthz", Handler: s.middleware(s.health, false)},
		{Method: http.MethodGet, Path: "/metrics", Handler: s.middleware(s.metrics, false)},
		{Method: http.MethodPost, Path: "/v1/payments", Handler: s.middleware(s.createPayment, true)},
		{Method: http.MethodGet, Path: "/v1/payments/:paymentNo", Handler: s.middleware(s.getPayment, true)},
		{Method: http.MethodPost, Path: "/v1/callbacks/:channel", Handler: s.middleware(s.callback, false)},
	}
}

type envelope struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
	Data    any       `json:"data,omitempty"`
}

// createPaymentRequest 定义创建支付接口的 JSON 输入；用户 ID 不由客户端提交。
type createPaymentRequest struct {
	OrderID  string `json:"orderId"`
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
	Method   string `json:"method"`
}

type callbackRequest struct {
	PaymentNo string `json:"paymentNo"`
	TradeNo   string `json:"tradeNo"`
	Result    string `json:"result"`
}

// paymentResponse 屏蔽仓储敏感字段，只向客户端暴露必要的支付信息。
type paymentResponse struct {
	PaymentNo string    `json:"paymentNo"`
	OrderID   string    `json:"orderId"`
	Amount    int64     `json:"amount"`
	Currency  string    `json:"currency"`
	Method    string    `json:"method"`
	Status    string    `json:"status"`
	PayURL    string    `json:"payUrl,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	// 存活接口不依赖数据库或 Redis，只用于负载均衡器判断进程是否响应。
	writeJSON(w, http.StatusOK, envelope{Code: CodeOK, Message: "ready"})
}

func (s *Server) metrics(w http.ResponseWriter, _ *http.Request) {
	// 返回最小 Prometheus 文本格式指标，计数值为进程启动后的累计 HTTP 请求数。
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = fmt.Fprintf(w, "# HELP payment_http_requests_total Total HTTP requests handled by payment service.\n# TYPE payment_http_requests_total counter\npayment_http_requests_total %d\n",
		s.requestCount.Load())
}

func (s *Server) createPayment(w http.ResponseWriter, r *http.Request) {
	// 严格解析单一 JSON 对象，并把已验签用户 ID 注入业务调用而不是信任请求体。
	var req createPaymentRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, err.Error())
		return
	}
	payment, payURL, err := s.payments.Create(r.Context(), auth.UserID(r.Context()),
		req.OrderID, req.Amount, req.Currency, req.Method)
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	logx.Infof("payment created trace_id=%s payment_no=%s order_id=%s user_id=%s",
		traceID(r), payment.PaymentNo, payment.OrderID, payment.UserID)
	writeJSON(w, http.StatusCreated, envelope{
		Code: CodeOK, Message: "success",
		Data: toResponse(payment, payURL),
	})
}

func (s *Server) getPayment(w http.ResponseWriter, r *http.Request) {
	// 同时兼容 REST 路径参数和部分 go-zero/net/http 下游无法直接提供参数的回退解析。
	paymentNo := r.PathValue("paymentNo")
	if paymentNo == "" {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) != 3 {
			writeError(w, http.StatusBadRequest, CodeInvalidArgument, "payment number is required")
			return
		}
		paymentNo = parts[2]
	}
	payment, err := s.payments.Get(r.Context(), paymentNo)
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	if payment.UserID != auth.UserID(r.Context()) {
		writeError(w, http.StatusForbidden, CodeForbidden, "payment does not belong to current user")
		return
	}
	writeJSON(w, http.StatusOK, envelope{Code: CodeOK, Message: "success", Data: toResponse(payment, "")})
}

func (s *Server) callback(w http.ResponseWriter, r *http.Request) {
	// 回调必须先校验渠道白名单，再读取受限 body 并基于原始字节进行 HMAC 验签。
	channelName := r.PathValue("channel")
	if channelName == "" {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) != 3 {
			writeError(w, http.StatusBadRequest, CodeInvalidArgument, "channel is required")
			return
		}
		channelName = parts[2]
	}
	if channelName != "alipay" && channelName != "wechat" {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "unsupported payment channel")
		return
	}
	payload, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "invalid callback body")
		return
	}
	if err := s.provider.VerifyCallback(payload, r.Header.Get("X-Callback-Signature")); err != nil {
		writeError(w, http.StatusUnauthorized, CodeInvalidSignature, err.Error())
		return
	}
	var req callbackRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "invalid callback JSON")
		return
	}
	target := model.StatusFailed
	if req.Result == "success" {
		target = model.StatusSuccess
	} else if req.Result != "failed" {
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, "result must be success or failed")
		return
	}
	payment, err := s.payments.Callback(r.Context(), req.PaymentNo, req.TradeNo, target, traceID(r))
	if err != nil {
		s.writeServiceError(w, err)
		return
	}
	logx.Infof("payment callback trace_id=%s payment_no=%s status=%s", traceID(r), payment.PaymentNo, payment.Status)
	writeJSON(w, http.StatusOK, envelope{Code: CodeOK, Message: "success", Data: toResponse(payment, "")})
}

func (s *Server) middleware(next http.HandlerFunc, requireAuth bool) http.HandlerFunc {
	// 中间件统一处理请求计数、CORS、trace ID 和可选的 Bearer JWT 鉴权。
	return func(w http.ResponseWriter, r *http.Request) {
		s.requestCount.Add(1)
		origin := r.Header.Get("Origin")
		if s.allowedOrigins[origin] {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Callback-Signature, X-Trace-ID")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		trace := r.Header.Get("X-Trace-ID")
		if trace == "" || len(trace) > 128 {
			trace = newTraceID()
		}
		w.Header().Set("X-Trace-ID", trace)
		r = r.WithContext(context.WithValue(r.Context(), traceContextKey{}, trace))
		if requireAuth {
			header := strings.Fields(r.Header.Get("Authorization"))
			if len(header) != 2 || !strings.EqualFold(header[0], "Bearer") {
				writeError(w, http.StatusUnauthorized, CodeUnauthorized, "bearer token is required")
				return
			}
			claims, err := auth.Parse(s.jwtSecret, header[1], time.Now())
			if err != nil {
				writeError(w, http.StatusUnauthorized, CodeUnauthorized, "invalid or expired token")
				return
			}
			r = r.WithContext(auth.WithUserID(r.Context(), claims.UserID))
		}
		next(w, r)
	}
}

type traceContextKey struct{}

// traceID 从请求上下文读取调用方传入或服务生成的链路标识。
func traceID(r *http.Request) string {
	trace, _ := r.Context().Value(traceContextKey{}).(string)
	return trace
}

func (s *Server) writeServiceError(w http.ResponseWriter, err error) {
	// 根据领域错误映射稳定的 HTTP 状态码和业务码，未知错误只返回通用消息。
	switch {
	case errors.Is(err, repository.ErrNotFound):
		writeError(w, http.StatusNotFound, CodeNotFound, err.Error())
	case errors.Is(err, service.ErrInvalidPayment), errors.Is(err, service.ErrInvalidTransition):
		writeError(w, http.StatusBadRequest, CodeInvalidArgument, err.Error())
	case errors.Is(err, service.ErrLockBusy):
		writeError(w, http.StatusConflict, CodeRetry, err.Error())
	default:
		logx.Errorf("payment request failed: %v", err)
		writeError(w, http.StatusInternalServerError, CodeInternal, "internal server error")
	}
}

func toResponse(payment *model.Payment, payURL string) paymentResponse {
	return paymentResponse{
		PaymentNo: payment.PaymentNo, OrderID: payment.OrderID, Amount: payment.Amount,
		Currency: payment.Currency, Method: payment.Method, Status: payment.Status,
		PayURL: payURL, CreatedAt: payment.CreatedAt,
	}
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	// 限制请求体为 1 MiB、禁止未知字段，并拒绝尾随多个 JSON 对象。
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request must contain a single JSON object")
	}
	return nil
}

func writeError(w http.ResponseWriter, status int, code ErrorCode, message string) {
	writeJSON(w, status, envelope{Code: code, Message: message})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		logx.Errorf("write HTTP response: %v", err)
	}
}

func newTraceID() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}
