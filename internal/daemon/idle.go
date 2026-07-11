package daemon

import (
	"context"
	"errors"
	"sync"
	"time"
)

const DefaultIdleTimeout = 10 * time.Minute

type IdleMonitor struct {
	timeout time.Duration

	mu           sync.Mutex
	active       int
	lastActivity time.Time
	wake         chan struct{}
}

func NewIdleMonitor(timeout time.Duration) (*IdleMonitor, error) {
	if timeout <= 0 {
		return nil, errors.New("create idle monitor: positive timeout is required")
	}
	return &IdleMonitor{
		timeout:      timeout,
		lastActivity: time.Now(),
		wake:         make(chan struct{}, 1),
	}, nil
}

func (m *IdleMonitor) Begin() func() {
	m.mu.Lock()
	m.active++
	m.lastActivity = time.Now()
	m.mu.Unlock()
	m.signal()

	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			if m.active > 0 {
				m.active--
			}
			m.lastActivity = time.Now()
			m.mu.Unlock()
			m.signal()
		})
	}
}

func (m *IdleMonitor) Touch() {
	m.mu.Lock()
	m.lastActivity = time.Now()
	m.mu.Unlock()
	m.signal()
}

func (m *IdleMonitor) Wait(ctx context.Context) error {
	if ctx == nil {
		return errors.New("wait for idle timeout: context is required")
	}
	for {
		m.mu.Lock()
		active := m.active
		remaining := m.timeout - time.Since(m.lastActivity)
		if active == 0 && remaining <= 0 {
			m.mu.Unlock()
			return nil
		}
		m.mu.Unlock()

		if active > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-m.wake:
				continue
			}
		}

		timer := time.NewTimer(remaining)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return ctx.Err()
		case <-m.wake:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
		}
	}
}

func (m *IdleMonitor) signal() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}
