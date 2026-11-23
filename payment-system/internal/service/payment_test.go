package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zfqiang/payment-system/internal/channel"
	"github.com/zfqiang/payment-system/internal/model"
)

// fakeLocker 模拟两种 Redis 分布式锁状态，并记录是否发生了无权释放。
type fakeLocker struct {
	acquired bool
	released bool
}

func (l *fakeLocker) Acquire(context.Context, string, string, time.Duration) (bool, error) {
	return l.acquired, nil
}

func (l *fakeLocker) Release(context.Context, string, string) error {
	l.released = true
	return nil
}

// fakeStore 只实现回调落库计数，用于验证锁边界是否阻止或允许仓储调用。
type fakeStore struct {
	callbackCalls int
}

func (s *fakeStore) CreatePayment(context.Context, *model.Payment) (*model.Payment, error) {
	return nil, errors.New("not used")
}
func (s *fakeStore) GetPayment(context.Context, string) (*model.Payment, error) {
	return nil, errors.New("not used")
}
func (s *fakeStore) ApplyCallback(_ context.Context, paymentNo, _, status string, at time.Time, _ string) (*model.Payment, error) {
	s.callbackCalls++
	return &model.Payment{PaymentNo: paymentNo, Status: status, CallbackAt: at}, nil
}
func (s *fakeStore) ClaimOutbox(context.Context) (*model.OutboxMessage, error) {
	return nil, errors.New("not used")
}
func (s *fakeStore) MarkOutboxQueued(context.Context, *model.OutboxMessage) error {
	return errors.New("not used")
}
func (s *fakeStore) MarkOutboxDead(context.Context, string, error) error {
	return errors.New("not used")
}
func (s *fakeStore) ReleaseOutbox(context.Context, *model.OutboxMessage, error) error {
	return errors.New("not used")
}

func TestCallbackDoesNotApplyWhenDistributedLockIsHeld(t *testing.T) {
	// 锁竞争失败时不得落库，也不得释放一个并非由当前调用持有的锁。
	store := &fakeStore{}
	locker := &fakeLocker{acquired: false}
	service := NewPaymentService(store, locker, channel.NewLocalProvider("secret"))

	_, err := service.Callback(context.Background(), "P-1", "T-1", model.StatusSuccess, "trace-1")
	if !errors.Is(err, ErrLockBusy) {
		t.Fatalf("Callback() error = %v, want %v", err, ErrLockBusy)
	}
	if store.callbackCalls != 0 {
		t.Fatalf("callback applied %d times while lock was held", store.callbackCalls)
	}
	if locker.released {
		t.Fatal("must not release a lock that was not acquired")
	}
}

func TestCallbackReleasesAcquiredLock(t *testing.T) {
	// 获得锁并完成回调后必须释放锁，避免 15 秒租期结束前重复回调仍被阻塞。
	store := &fakeStore{}
	locker := &fakeLocker{acquired: true}
	service := NewPaymentService(store, locker, channel.NewLocalProvider("secret"))

	if _, err := service.Callback(context.Background(), "P-1", "T-1", model.StatusSuccess, "trace-1"); err != nil {
		t.Fatal(err)
	}
	if store.callbackCalls != 1 || !locker.released {
		t.Fatalf("callback calls=%d lock released=%t", store.callbackCalls, locker.released)
	}
}
