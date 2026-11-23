package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/zfqiang/payment-system/internal/model"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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

// MySQL 封装 GORM 数据库连接，供支付业务和 Outbox Worker 复用。
type MySQL struct {
	db *gorm.DB
}

// paymentRow 将数据库列映射为 GORM 持久化模型，与对外 API 模型分离。
type paymentRow struct {
	ID              uint64     `gorm:"column:id;primaryKey"`
	PaymentNo       string     `gorm:"column:payment_no"`
	OrderID         string     `gorm:"column:order_id"`
	UserID          string     `gorm:"column:user_id"`
	Amount          int64      `gorm:"column:amount"`
	Currency        string     `gorm:"column:currency"`
	Method          string     `gorm:"column:method"`
	Status          string     `gorm:"column:status"`
	ProviderTradeNo *string    `gorm:"column:provider_trade_no"`
	CallbackAt      *time.Time `gorm:"column:callback_at"`
	CreatedAt       time.Time  `gorm:"column:created_at"`
}

func (paymentRow) TableName() string { return "payments" }

// outboxRow 映射支付成功事件表；Payload 对应 MySQL JSON 列。
type outboxRow struct {
	ID           uint64     `gorm:"column:id;primaryKey"`
	EventKey     string     `gorm:"column:event_key"`
	PaymentNo    string     `gorm:"column:payment_no"`
	Payload      []byte     `gorm:"column:payload"`
	Status       string     `gorm:"column:status"`
	Attempts     uint32     `gorm:"column:attempts"`
	ClaimToken   *string    `gorm:"column:claim_token"`
	ClaimedUntil *time.Time `gorm:"column:claimed_until"`
	LastError    *string    `gorm:"column:last_error"`
	CreatedAt    time.Time  `gorm:"column:created_at"`
	QueuedAt     *time.Time `gorm:"column:queued_at"`
}

func (outboxRow) TableName() string { return "payment_outbox" }

// OpenMySQL 使用 GORM MySQL Dialector 建立连接，并在返回前检查数据库可用性。
func OpenMySQL(ctx context.Context, dsn string) (*MySQL, error) {
	fmt.Println(dsn)
	db, err := gorm.Open(gormmysql.New(gormmysql.Config{
		DSN:                       dsn,
		SkipInitializeWithVersion: true,
	}), &gorm.Config{
		DisableAutomaticPing: true,
		TranslateError:       true,
	})
	if err != nil {
		return nil, fmt.Errorf("open mysql with gorm: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("get mysql connection pool: %w", err)
	}
	sqlDB.SetMaxOpenConns(30)
	sqlDB.SetMaxIdleConns(10)
	sqlDB.SetConnMaxLifetime(5 * time.Minute)
	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("ping mysql: %w", err)
	}
	return &MySQL{db: db}, nil
}

// Close 关闭 GORM 底层的 database/sql 连接池。
func (m *MySQL) Close() error {
	db, err := m.db.DB()
	if err != nil {
		return fmt.Errorf("get mysql connection pool: %w", err)
	}
	return db.Close()
}

func (m *MySQL) CreatePayment(ctx context.Context, p *model.Payment) (*model.Payment, error) {
	// 订单号唯一约束充当幂等键；重复请求只在支付参数完全一致时复用原支付单。
	row := paymentRow{
		PaymentNo: p.PaymentNo,
		OrderID:   p.OrderID,
		UserID:    p.UserID,
		Amount:    p.Amount,
		Currency:  p.Currency,
		Method:    p.Method,
		Status:    model.StatusPending,
	}
	if err := m.db.WithContext(ctx).Create(&row).Error; err == nil {
		return m.GetPayment(ctx, p.PaymentNo)
	} else if !errors.Is(err, gorm.ErrDuplicatedKey) {
		return nil, fmt.Errorf("insert payment: %w", err)
	}

	existing, err := m.getPaymentByOrder(ctx, p.OrderID)
	if err != nil {
		return nil, fmt.Errorf("insert payment: %w", err)
	}
	if existing.UserID != p.UserID || existing.Amount != p.Amount ||
		existing.Currency != p.Currency || existing.Method != p.Method {
		return nil, errors.New("idempotency key already used with different payment details")
	}
	return existing, nil
}

func (m *MySQL) GetPayment(ctx context.Context, paymentNo string) (*model.Payment, error) {
	var row paymentRow
	err := m.db.WithContext(ctx).Where("payment_no = ?", paymentNo).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("query payment: %w", err)
	}
	return row.toModel(), nil
}

func (m *MySQL) getPaymentByOrder(ctx context.Context, orderID string) (*model.Payment, error) {
	var row paymentRow
	err := m.db.WithContext(ctx).Where("order_id = ?", orderID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("query payment by order: %w", err)
	}
	return row.toModel(), nil
}

func (r paymentRow) toModel() *model.Payment {
	payment := &model.Payment{
		PaymentNo: r.PaymentNo,
		OrderID:   r.OrderID,
		UserID:    r.UserID,
		Amount:    r.Amount,
		Currency:  r.Currency,
		Method:    r.Method,
		Status:    r.Status,
		CreatedAt: r.CreatedAt,
	}
	if r.ProviderTradeNo != nil {
		payment.ProviderTradeNo = *r.ProviderTradeNo
	}
	if r.CallbackAt != nil {
		payment.CallbackAt = *r.CallbackAt
	}
	return payment
}

func (m *MySQL) ApplyCallback(ctx context.Context, paymentNo, tradeNo, target string, at time.Time, traceID string) (*model.Payment, error) {
	// 在同一事务中用行锁读取状态、校验转换、更新支付并插入成功事件，提交时原子生效。
	err := m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row paymentRow
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("payment_no = ?", paymentNo).First(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock payment: %w", err)
		}
		if !model.CanTransition(row.Status, target) {
			return model.ErrInvalidTransition
		}
		if row.Status == target {
			return nil
		}

		updates := map[string]interface{}{
			"status":            target,
			"provider_trade_no": tradeNo,
			"callback_at":       at,
		}
		if err := tx.Model(&paymentRow{}).Where("payment_no = ?", paymentNo).Updates(updates).Error; err != nil {
			return fmt.Errorf("update payment callback: %w", err)
		}
		if target == model.StatusSuccess {
			event := model.OrderPaidEvent{
				EventID: paymentNo + ":paid", PaymentNo: paymentNo, OrderID: row.OrderID,
				UserID: row.UserID, Amount: row.Amount, Currency: row.Currency, PaidAt: at, TraceID: traceID,
			}
			payload, err := json.Marshal(event)
			if err != nil {
				return fmt.Errorf("encode paid event: %w", err)
			}
			outbox := outboxRow{
				EventKey: event.EventID, PaymentNo: paymentNo,
				Payload: payload, Status: "pending",
			}
			if err := tx.Create(&outbox).Error; err != nil {
				return fmt.Errorf("insert payment outbox: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("apply payment callback: %w", err)
	}
	return m.GetPayment(ctx, paymentNo)
}

func (m *MySQL) ClaimOutbox(ctx context.Context) (*model.OutboxMessage, error) {
	// SKIP LOCKED 跳过其他 Worker 正在处理的行，并用随机 claim token 防止旧租约修改新租约。
	var message model.OutboxMessage
	err := m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row outboxRow
		err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("status = ? OR (status = ? AND claimed_until < ?)",
				"pending", "processing", gorm.Expr("UTC_TIMESTAMP(3)")).
			Order("id ASC").First(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("select outbox event: %w", err)
		}

		message = model.OutboxMessage{
			ID: row.ID, EventKey: row.EventKey, Payload: row.Payload,
			ClaimToken: strings.ReplaceAll(uuid.NewString(), "-", ""),
		}
		result := tx.Model(&outboxRow{}).Where("id = ?", row.ID).Updates(map[string]interface{}{
			"status":        "processing",
			"claim_token":   message.ClaimToken,
			"claimed_until": gorm.Expr("UTC_TIMESTAMP(3) + INTERVAL 30 SECOND"),
		})
		if result.Error != nil {
			return fmt.Errorf("claim outbox event: %w", result.Error)
		}
		return requireAffectedRow(result, "claim outbox event")
	})
	if err != nil {
		return nil, fmt.Errorf("claim outbox message: %w", err)
	}
	if message.ID == 0 {
		return nil, nil
	}
	return &message, nil
}

func (m *MySQL) MarkOutboxQueued(ctx context.Context, message *model.OutboxMessage) error {
	// 只有持有当前 claim token 的 Worker 可以把租约记录标记为 queued。
	result := m.db.WithContext(ctx).Model(&outboxRow{}).
		Where("id = ? AND claim_token = ?", message.ID, message.ClaimToken).
		Updates(map[string]interface{}{
			"status":        "queued",
			"queued_at":     gorm.Expr("UTC_TIMESTAMP(3)"),
			"claimed_until": nil,
		})
	if result.Error != nil {
		return fmt.Errorf("mark outbox queued: %w", result.Error)
	}
	return requireAffectedRow(result, "mark outbox queued")
}

func (m *MySQL) MarkOutboxDead(ctx context.Context, eventKey string, cause error) error {
	// 重试耗尽后保留失败原因并进入 dead 状态，等待人工核对后补偿重放。
	reason := truncateError(cause)
	result := m.db.WithContext(ctx).Model(&outboxRow{}).Where("event_key = ?", eventKey).
		Updates(map[string]interface{}{
			"status":        "dead",
			"attempts":      gorm.Expr("attempts + ?", 1),
			"last_error":    reason,
			"claimed_until": nil,
		})
	if result.Error != nil {
		return fmt.Errorf("mark outbox event dead: %w", result.Error)
	}
	return requireAffectedRow(result, "mark outbox event dead")
}

func (m *MySQL) ReleaseOutbox(ctx context.Context, message *model.OutboxMessage, cause error) error {
	// 入队失败时回滚 outbox 租约并保留截断后的错误原因，供再次 claim。
	result := m.db.WithContext(ctx).Model(&outboxRow{}).
		Where("id = ? AND claim_token = ?", message.ID, message.ClaimToken).
		Updates(map[string]interface{}{
			"status":        "pending",
			"attempts":      gorm.Expr("attempts + ?", 1),
			"last_error":    truncateError(cause),
			"claimed_until": nil,
		})
	if result.Error != nil {
		return fmt.Errorf("release outbox event: %w", result.Error)
	}
	return requireAffectedRow(result, "release outbox event")
}

func truncateError(cause error) string {
	reason := cause.Error()
	if len(reason) > 500 {
		return reason[:500]
	}
	return reason
}

func requireAffectedRow(result *gorm.DB, operation string) error {
	if result.RowsAffected != 1 {
		return fmt.Errorf("%s: expected one row, updated %d", operation, result.RowsAffected)
	}
	return nil
}
