package service

import (
	"github.com/zfqiang/payment-system/internal/model"
)

var ErrInvalidTransition = model.ErrInvalidTransition

// CanTransition 暴露模型层状态机规则供其他包复用。
func CanTransition(from, to string) bool {
	return model.CanTransition(from, to)
}
