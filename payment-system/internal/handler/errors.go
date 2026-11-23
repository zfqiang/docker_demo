package handler

type ErrorCode string

// 统一响应码：业务含义与 HTTP 状态码解耦，便于前端稳定处理。
const (
	CodeOK               ErrorCode = "OK"
	CodeInvalidArgument  ErrorCode = "INVALID_ARGUMENT"
	CodeInvalidSignature ErrorCode = "INVALID_SIGNATURE"
	CodeUnauthorized     ErrorCode = "UNAUTHORIZED"
	CodeForbidden        ErrorCode = "FORBIDDEN"
	CodeNotFound         ErrorCode = "NOT_FOUND"
	CodeRetry            ErrorCode = "RETRY"
	CodeInternal         ErrorCode = "INTERNAL"
)
