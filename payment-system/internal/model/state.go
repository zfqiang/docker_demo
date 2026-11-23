package model

import "errors"

var ErrInvalidTransition = errors.New("invalid payment status transition")

// CanTransition 实施支付状态机规则：
// pending 可变为 success、failed 或 closed；终态只允许同态重放。
func CanTransition(from, to string) bool {
	if from == to {
		return true
	}
	return from == StatusPending &&
		(to == StatusSuccess || to == StatusFailed || to == StatusClosed)
}
