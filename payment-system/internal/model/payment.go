package model

import "time"

const (
	// 状态仅允许由 payment status state machine 显式控制，不能由客户端自由指定。
	StatusPending = "pending"
	StatusSuccess = "success"
	StatusFailed  = "failed"
	StatusClosed  = "closed"
)

// Payment 是支付领域聚合根对应的数据模型，同时用于仓储和 HTTP 响应转换。
type Payment struct {
	PaymentNo       string    `json:"paymentNo"`
	OrderID         string    `json:"orderId"`
	UserID          string    `json:"userId"`
	Amount          int64     `json:"amount"`
	Currency        string    `json:"currency"`
	Method          string    `json:"method"`
	Status          string    `json:"status"`
	ProviderTradeNo string    `json:"providerTradeNo,omitempty"`
	CallbackAt      time.Time `json:"callbackAt,omitempty"`
	CreatedAt       time.Time `json:"createdAt"`
}

// OrderPaidEvent 是支付成功后投递给订单服务的持久化业务事件。
type OrderPaidEvent struct {
	EventID   string    `json:"eventId"`
	PaymentNo string    `json:"paymentNo"`
	OrderID   string    `json:"orderId"`
	UserID    string    `json:"userId"`
	Amount    int64     `json:"amount"`
	Currency  string    `json:"currency"`
	PaidAt    time.Time `json:"paidAt"`
	TraceID   string    `json:"traceId"`
}

// OutboxMessage 是从 payment_outbox 表取出的待投递消息及其租约令牌。
type OutboxMessage struct {
	ID         uint64
	EventKey   string
	Payload    []byte
	ClaimToken string
}
