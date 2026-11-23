{{/*
Reusable response shape for goctl-generated handlers.
Call with: {{ template "paymentResponse" . }}
*/}}
{{ define "paymentResponse" -}}
type ErrorCode string

const (
	CodeOK ErrorCode = "OK"
	CodeInvalidArgument ErrorCode = "INVALID_ARGUMENT"
	CodeInvalidSignature ErrorCode = "INVALID_SIGNATURE"
	CodeUnauthorized ErrorCode = "UNAUTHORIZED"
	CodeForbidden ErrorCode = "FORBIDDEN"
	CodeNotFound ErrorCode = "NOT_FOUND"
	CodeRetry ErrorCode = "RETRY"
	CodeInternal ErrorCode = "INTERNAL"
)

type Response struct {
	Code    ErrorCode `json:"code"`
	Message string `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}
{{- end }}
