package queue

import (
	"encoding/json"
	"testing"

	"github.com/zfqiang/payment-system/internal/model"
)

func TestNewTaskPreservesEventAndTraceFields(t *testing.T) {
	// 验证序列化出的 Asynq task 保留事件 ID、trace ID 和订单通知所需字段。
	event := model.OrderPaidEvent{
		EventID: "P-1:paid", PaymentNo: "P-1", OrderID: "O-1",
		UserID: "U-1", Amount: 1299, Currency: "CNY", TraceID: "trace-1",
	}
	payload, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	task := NewTask(&model.OutboxMessage{EventKey: event.EventID, Payload: payload})
	if task.Type() != OrderPaidTask {
		t.Fatalf("task type = %q, want %q", task.Type(), OrderPaidTask)
	}
	var decoded model.OrderPaidEvent
	if err := json.Unmarshal(task.Payload(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.EventID != event.EventID || decoded.TraceID != event.TraceID ||
		decoded.OrderID != event.OrderID || decoded.Amount != event.Amount {
		t.Fatalf("decoded event = %+v, want %+v", decoded, event)
	}
}
