package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/zfqiang/payment-system/internal/channel"
	"github.com/zfqiang/payment-system/internal/model"
	"github.com/zfqiang/payment-system/internal/repository"
)

var (
	ErrInvalidPayment = errors.New("invalid payment request")
	ErrLockBusy       = errors.New("payment callback is being processed")
)

// PaymentService 编排仓储、分布式锁和支付渠道，是核心业务用例入口。
type PaymentService struct {
	store    repository.Store
	locker   CallbackLocker
	provider channel.Provider
}

func NewPaymentService(store repository.Store, locker CallbackLocker, provider channel.Provider) *PaymentService {
	return &PaymentService{store: store, locker: locker, provider: provider}
}

// Create 校验用户、金额、币种和支付方式，创建幂等支付单后向渠道生成支付地址。
func (s *PaymentService) Create(ctx context.Context, userID, orderID string, amount int64, currency, method string) (*model.Payment, string, error) {
	if userID == "" || orderID == "" || amount <= 0 ||
		(method != "alipay" && method != "wechat") {
		return nil, "", ErrInvalidPayment
	}
	if currency == "" {
		currency = "CNY"
	}
	if len(currency) != 3 {
		return nil, "", ErrInvalidPayment
	}
	paymentNo, err := newPaymentNo()
	if err != nil {
		return nil, "", fmt.Errorf("create payment number: %w", err)
	}
	payment, err := s.store.CreatePayment(ctx, &model.Payment{
		PaymentNo: paymentNo,
		OrderID:   orderID,
		UserID:    userID,
		Amount:    amount,
		Currency:  currency,
		Method:    method,
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		return nil, "", err
	}
	payURL, err := s.provider.CreatePayment(channel.PaymentRequest{
		PaymentNo: payment.PaymentNo,
		Amount:    payment.Amount,
		Currency:  payment.Currency,
		Method:    payment.Method,
	})
	if err != nil {
		return nil, "", fmt.Errorf("create channel payment: %w", err)
	}
	return payment, payURL, nil
}

func (s *PaymentService) Get(ctx context.Context, paymentNo string) (*model.Payment, error) {
	// 查询逻辑由仓储层负责，业务层在此保留清晰的领域入口。
	return s.store.GetPayment(ctx, paymentNo)
}

// Callback 先以随机令牌取得短租约 Redis 锁，再由数据库行锁保证状态与 outbox 事务安全。
func (s *PaymentService) Callback(ctx context.Context, paymentNo, tradeNo, target, traceID string) (*model.Payment, error) {
	if target != model.StatusSuccess && target != model.StatusFailed {
		return nil, ErrInvalidPayment
	}
	token, err := randomHex(16)
	if err != nil {
		return nil, fmt.Errorf("create callback lock token: %w", err)
	}
	lockKey := "payment:callback:" + paymentNo
	acquired, err := s.locker.Acquire(ctx, lockKey, token, 15*time.Second)
	if err != nil {
		return nil, fmt.Errorf("acquire callback lock: %w", err)
	}
	if !acquired {
		return nil, ErrLockBusy
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := s.locker.Release(releaseCtx, lockKey, token); err != nil {
			log.Printf("release payment callback lock key=%s: %v", lockKey, err)
		}
	}()
	return s.store.ApplyCallback(ctx, paymentNo, tradeNo, target, time.Now().UTC(), traceID)
}

func newPaymentNo() (string, error) {
	random, err := randomHex(12)
	if err != nil {
		return "", err
	}
	return "P" + time.Now().UTC().Format("20060102150405") + random, nil
}

func randomHex(bytes int) (string, error) {
	value := make([]byte, bytes)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}
