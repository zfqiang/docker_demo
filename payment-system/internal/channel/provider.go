package channel

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
)

// ErrInvalidSignature 表示回调签名格式错误或与 HMAC 计算结果不一致。
var ErrInvalidSignature = errors.New("invalid provider callback signature")

// PaymentRequest 描述创建支付时传给第三方渠道适配器的标准化参数。
type PaymentRequest struct {
	PaymentNo string
	Amount    int64
	Currency  string
	Method    string
}

type Provider interface {
	// CreatePayment 向支付渠道创建支付，并返回用户可访问的支付页面或链接。
	CreatePayment(PaymentRequest) (string, error)
	// VerifyCallback 使用原始请求 body 和渠道密钥验证回调真实性。
	VerifyCallback(payload []byte, signature string) error
}

// LocalProvider keeps the project runnable without credentials from a real channel.
type LocalProvider struct {
	secret []byte
}

func NewLocalProvider(secret string) *LocalProvider {
	return &LocalProvider{secret: []byte(secret)}
}

// CreatePayment 在本地实现中不发起真实渠道请求，只校验必填参数并生成演示支付地址。
func (p *LocalProvider) CreatePayment(req PaymentRequest) (string, error) {
	if req.PaymentNo == "" || req.Amount <= 0 {
		return "", errors.New("invalid payment request")
	}
	return fmt.Sprintf("http://localhost:8080/mock-pay/%s", req.PaymentNo), nil
}

// VerifyCallback 将十六进制签名解码后与对原始 body 的 HMAC-SHA256 比较。
func (p *LocalProvider) VerifyCallback(payload []byte, signature string) error {
	provided, err := hex.DecodeString(signature)
	if err != nil {
		return ErrInvalidSignature
	}
	mac := hmac.New(sha256.New, p.secret)
	_, _ = mac.Write(payload)
	if !hmac.Equal(provided, mac.Sum(nil)) {
		return ErrInvalidSignature
	}
	return nil
}

// SignCallback 是回调签名工具；返回值可用于本地测试或模拟渠道发送回调。
func SignCallback(secret string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}
