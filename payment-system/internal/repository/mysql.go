package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"github.com/zfqiang/payment-system/internal/model"
)

var ErrNotFound = errors.New("payment not found")

// Store 是服务层和 Worker 依赖的持久化端口，统一支付及 outbox 操作。
type Store interface {
	CreatePayment(context.Context, *model.Payment) (*model.Payment, error)
	GetPayment(context.Context, string) (*model.Payment, error)
	ApplyCallback(context.Context, string, string, string, time.Time, string) (*model.Payment, error)
	ClaimOutbox(context.Context) (*model.OutboxMessage, error)
	MarkOutboxQueued(context.Context, *model.OutboxMessage) error
	MarkOutboxDead(context.Context, string, error) error
	ReleaseOutbox(context.Context, *model.OutboxMessage, error) error
}

// MySQL 封装数据库连接池，保证业务代码不直接处理 SQL 驱动细节。
type MySQL struct {
	db *sql.DB
}

func OpenMySQL(ctx context.Context, dsn string) (*MySQL, error) {
	// 配置连接池并 PingContext 确认启动时数据库已可用。
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}
	db.SetMaxOpenConns(30)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(5 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping mysql: %w", err)
	}
	return &MySQL{db: db}, nil
}

func (m *MySQL) Close() error { return m.db.Close() }

func (m *MySQL) CreatePayment(ctx context.Context, p *model.Payment) (*model.Payment, error) {
	// 订单号唯一约束充当幂等键；重复请求只在支付参数完全一致时复用原支付单。
	_, err := m.db.ExecContext(ctx, `INSERT INTO payments
		(payment_no, order_id, user_id, amount, currency, method, status)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		p.PaymentNo, p.OrderID, p.UserID, p.Amount, p.Currency, p.Method, model.StatusPending)
	if err == nil {
		return m.GetPayment(ctx, p.PaymentNo)
	}
	var mysqlErr *mysql.MySQLError
	if !errors.As(err, &mysqlErr) || mysqlErr.Number != 1062 {
		return nil, fmt.Errorf("insert payment: %w", err)
	}
	existing, getErr := m.getPaymentByOrder(ctx, p.OrderID)
	if getErr != nil {
		return nil, fmt.Errorf("insert payment: %w", err)
	}
	if existing.UserID != p.UserID || existing.Amount != p.Amount ||
		existing.Currency != p.Currency || existing.Method != p.Method {
		return nil, errors.New("idempotency key already used with different payment details")
	}
	return existing, nil
}

func (m *MySQL) GetPayment(ctx context.Context, paymentNo string) (*model.Payment, error) {
	p := new(model.Payment)
	var tradeNo sql.NullString
	var callbackAt sql.NullTime
	err := m.db.QueryRowContext(ctx, `SELECT payment_no, order_id, user_id, amount,
		currency, method, status, provider_trade_no, callback_at, created_at
		FROM payments WHERE payment_no = ?`, paymentNo).
		Scan(&p.PaymentNo, &p.OrderID, &p.UserID, &p.Amount, &p.Currency, &p.Method,
			&p.Status, &tradeNo, &callbackAt, &p.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("query payment: %w", err)
	}
	if tradeNo.Valid {
		p.ProviderTradeNo = tradeNo.String
	}
	if callbackAt.Valid {
		p.CallbackAt = callbackAt.Time
	}
	return p, nil
}

func (m *MySQL) getPaymentByOrder(ctx context.Context, orderID string) (*model.Payment, error) {
	var paymentNo string
	err := m.db.QueryRowContext(ctx, "SELECT payment_no FROM payments WHERE order_id = ?", orderID).
		Scan(&paymentNo)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("query payment by order: %w", err)
	}
	return m.GetPayment(ctx, paymentNo)
}

func (m *MySQL) ApplyCallback(ctx context.Context, paymentNo, tradeNo, target string, at time.Time, traceID string) (*model.Payment, error) {
	// 在同一事务中用行锁读取状态、校验转换、更新支付并插入成功事件，提交时原子生效。
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin callback transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	p := new(model.Payment)
	err = tx.QueryRowContext(ctx, `SELECT payment_no, order_id, user_id, amount, currency,
		method, status FROM payments WHERE payment_no = ? FOR UPDATE`, paymentNo).
		Scan(&p.PaymentNo, &p.OrderID, &p.UserID, &p.Amount, &p.Currency, &p.Method, &p.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lock payment: %w", err)
	}
	if !model.CanTransition(p.Status, target) {
		return nil, model.ErrInvalidTransition
	}
	if p.Status != target {
		_, err = tx.ExecContext(ctx, `UPDATE payments SET status = ?, provider_trade_no = ?,
			callback_at = ? WHERE payment_no = ?`, target, tradeNo, at, paymentNo)
		if err != nil {
			return nil, fmt.Errorf("update payment callback: %w", err)
		}
		if target == model.StatusSuccess {
			event := model.OrderPaidEvent{
				EventID: paymentNo + ":paid", PaymentNo: paymentNo, OrderID: p.OrderID,
				UserID: p.UserID, Amount: p.Amount, Currency: p.Currency, PaidAt: at, TraceID: traceID,
			}
			payload, marshalErr := json.Marshal(event)
			if marshalErr != nil {
				return nil, fmt.Errorf("encode paid event: %w", marshalErr)
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO payment_outbox (event_key, payment_no, payload)
				VALUES (?, ?, ?)`, event.EventID, paymentNo, payload)
			if err != nil {
				return nil, fmt.Errorf("insert payment outbox: %w", err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit callback transaction: %w", err)
	}
	return m.GetPayment(ctx, paymentNo)
}

func (m *MySQL) ClaimOutbox(ctx context.Context) (*model.OutboxMessage, error) {
	// SKIP LOCKED 跳过其他 Worker 正在处理的行，并用随机 claim token 防止旧租约修改新租约。
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin outbox claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	message := new(model.OutboxMessage)
	err = tx.QueryRowContext(ctx, `SELECT id, event_key, payload FROM payment_outbox
		WHERE status = 'pending' OR (status = 'processing' AND claimed_until < UTC_TIMESTAMP(3))
		ORDER BY id LIMIT 1 FOR UPDATE SKIP LOCKED`).
		Scan(&message.ID, &message.EventKey, &message.Payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("select outbox event: %w", err)
	}
	message.ClaimToken = strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = tx.ExecContext(ctx, `UPDATE payment_outbox SET status = 'processing',
		claim_token = ?, claimed_until = UTC_TIMESTAMP(3) + INTERVAL 30 SECOND WHERE id = ?`,
		message.ClaimToken, message.ID)
	if err != nil {
		return nil, fmt.Errorf("claim outbox event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit outbox claim: %w", err)
	}
	return message, nil
}

func (m *MySQL) MarkOutboxQueued(ctx context.Context, message *model.OutboxMessage) error {
	// 只有持有当前 claim token 的 Worker 可以把租约记录标记为 queued。
	result, err := m.db.ExecContext(ctx, `UPDATE payment_outbox SET status = 'queued',
		queued_at = UTC_TIMESTAMP(3), claimed_until = NULL WHERE id = ? AND claim_token = ?`,
		message.ID, message.ClaimToken)
	if err != nil {
		return fmt.Errorf("mark outbox queued: %w", err)
	}
	return requireAffectedRow(result, "mark outbox queued")
}

func (m *MySQL) MarkOutboxDead(ctx context.Context, eventKey string, cause error) error {
	// 重试耗尽后保留失败原因并进入 dead 状态，等待人工核对后补偿重放。
	reason := cause.Error()
	if len(reason) > 500 {
		reason = reason[:500]
	}
	result, err := m.db.ExecContext(ctx, `UPDATE payment_outbox SET status = 'dead',
		attempts = attempts + 1, last_error = ?, claimed_until = NULL WHERE event_key = ?`,
		reason, eventKey)
	if err != nil {
		return fmt.Errorf("mark outbox event dead: %w", err)
	}
	return requireAffectedRow(result, "mark outbox event dead")
}

func (m *MySQL) ReleaseOutbox(ctx context.Context, message *model.OutboxMessage, cause error) error {
	// 入队失败时回滚 outbox 租约并保留截断后的错误原因，供再次 claim。
	reason := cause.Error()
	if len(reason) > 500 {
		reason = reason[:500]
	}
	result, err := m.db.ExecContext(ctx, `UPDATE payment_outbox SET status = 'pending',
		attempts = attempts + 1, last_error = ?, claimed_until = NULL WHERE id = ? AND claim_token = ?`,
		reason, message.ID, message.ClaimToken)
	if err != nil {
		return fmt.Errorf("release outbox event: %w", err)
	}
	return requireAffectedRow(result, "release outbox event")
}

func requireAffectedRow(result sql.Result, operation string) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s affected rows: %w", operation, err)
	}
	if rows != 1 {
		return fmt.Errorf("%s: expected one row, updated %d", operation, rows)
	}
	return nil
}
