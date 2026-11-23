package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hibiken/asynq"
	"github.com/zeromicro/go-zero/core/logx"
	"github.com/zfqiang/payment-system/internal/model"
	"github.com/zfqiang/payment-system/internal/repository"
)

const (
	// 正常任务最多由 Asynq 重试，重试耗尽后转入独立死信任务。
	OrderPaidTask = "payment:order_paid"
	DeadTask      = "payment:order_paid:dead"
)

// OrderNotifier 抽象订单服务通知能力，便于替换或测试不同传输实现。
type OrderNotifier interface {
	NotifyOrderPaid(context.Context, model.OrderPaidEvent) error
}

// HTTPOrderNotifier 使用 5 秒超时的 HTTP 客户端通知订单服务。
type HTTPOrderNotifier struct {
	baseURL string
	client  *http.Client
}

func NewHTTPOrderNotifier(baseURL string) *HTTPOrderNotifier {
	return &HTTPOrderNotifier{
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  &http.Client{Timeout: 5 * time.Second},
	}
}

func (n *HTTPOrderNotifier) NotifyOrderPaid(ctx context.Context, event model.OrderPaidEvent) error {
	// 幂等键使用持久化事件 ID，trace ID 用于串联支付回调、任务和订单服务日志。
	body, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode order payment notification: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		n.baseURL+"/internal/orders/payment-success", strings.NewReader(string(body)))
	if err != nil {
		return fmt.Errorf("create order notification request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Idempotency-Key", event.EventID)
	if event.TraceID != "" {
		req.Header.Set("X-Trace-ID", event.TraceID)
	}
	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("call order service: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("order service returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func NewTask(message *model.OutboxMessage) *asynq.Task {
	// 将 outbox 的原始 JSON payload 原样封装为 Asynq 正常任务。
	return asynq.NewTask(OrderPaidTask, message.Payload)
}

// RunOutboxDispatcher 每秒 claim 一条消息并入队，至少一次投递语义由幂等键兜底。
func RunOutboxDispatcher(ctx context.Context, store repository.Store, client *asynq.Client) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if err := dispatchOne(ctx, store, client); err != nil {
			logx.Errorf("dispatch payment outbox: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func dispatchOne(ctx context.Context, store repository.Store, client *asynq.Client) error {
	// 入队成功后标记 queued；入队失败且不是任务 ID 冲突时释放租约以允许重试。
	message, err := store.ClaimOutbox(ctx)
	if err != nil || message == nil {
		return err
	}
	task := NewTask(message)
	_, enqueueErr := client.EnqueueContext(ctx, task,
		asynq.Queue("default"), asynq.TaskID(message.EventKey),
		asynq.MaxRetry(5), asynq.Timeout(10*time.Second))
	if enqueueErr != nil && !errors.Is(enqueueErr, asynq.ErrTaskIDConflict) {
		if releaseErr := store.ReleaseOutbox(ctx, message, enqueueErr); releaseErr != nil {
			return errors.Join(enqueueErr, releaseErr)
		}
		return fmt.Errorf("enqueue order notification: %w", enqueueErr)
	}
	if err := store.MarkOutboxQueued(ctx, message); err != nil {
		return err
	}
	return nil
}

type paidTaskHandler struct {
	notifier OrderNotifier
	client   *asynq.Client
	store    repository.Store
}

func NewMux(notifier OrderNotifier, client *asynq.Client, store repository.Store) *asynq.ServeMux {
	// 为正常任务和死信任务分别注册处理函数。
	mux := asynq.NewServeMux()
	handler := &paidTaskHandler{notifier: notifier, client: client, store: store}
	mux.HandleFunc(OrderPaidTask, handler.handlePaid)
	mux.HandleFunc(DeadTask, handler.handleDead)
	return mux
}

func (h *paidTaskHandler) handlePaid(ctx context.Context, task *asynq.Task) error {
	// 校验业务必填字段后通知订单服务；达到最大重试次数时落库并送入死信队列。
	var event model.OrderPaidEvent
	if err := json.Unmarshal(task.Payload(), &event); err != nil {
		return fmt.Errorf("decode order paid task: %w", err)
	}
	if event.EventID == "" || event.OrderID == "" || event.PaymentNo == "" {
		return errors.New("order paid task is missing required identifiers")
	}
	if err := h.notifier.NotifyOrderPaid(ctx, event); err != nil {
		retries, ok := asynq.GetRetryCount(ctx)
		if !ok {
			return errors.Join(err, errors.New("retry count is unavailable"))
		}
		maxRetries, ok := asynq.GetMaxRetry(ctx)
		if !ok {
			return errors.Join(err, errors.New("maximum retry count is unavailable"))
		}
		if retries >= maxRetries {
			deadTask := asynq.NewTask(DeadTask, task.Payload())
			_, enqueueErr := h.client.EnqueueContext(ctx, deadTask,
				asynq.Queue("dead"), asynq.TaskID(event.EventID+":dead"), asynq.MaxRetry(0))
			if enqueueErr != nil && !errors.Is(enqueueErr, asynq.ErrTaskIDConflict) {
				return errors.Join(err, fmt.Errorf("enqueue dead letter: %w", enqueueErr))
			}
			if markErr := h.store.MarkOutboxDead(ctx, event.EventID, err); markErr != nil {
				return errors.Join(err, markErr)
			}
			logx.Errorf("order notification moved to dead queue event_id=%s order_id=%s error=%v",
				event.EventID, event.OrderID, err)
			return nil
		}
		return fmt.Errorf("notify order service: %w", err)
	}
	logx.Infof("order notification delivered event_id=%s order_id=%s trace_id=%s",
		event.EventID, event.OrderID, event.TraceID)
	return nil
}

func (h *paidTaskHandler) handleDead(_ context.Context, task *asynq.Task) error {
	var event model.OrderPaidEvent
	if err := json.Unmarshal(task.Payload(), &event); err != nil {
		return fmt.Errorf("decode dead-letter task: %w", err)
	}
	logx.Errorf("dead-letter order notification event_id=%s order_id=%s payment_no=%s",
		event.EventID, event.OrderID, event.PaymentNo)
	return nil
}
