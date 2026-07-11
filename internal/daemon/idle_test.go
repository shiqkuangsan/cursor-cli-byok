package daemon

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestIdleMonitorFiresAfterTimeout(t *testing.T) {
	monitor, err := NewIdleMonitor(40 * time.Millisecond)
	if err != nil {
		t.Fatalf("NewIdleMonitor() error = %v", err)
	}
	start := time.Now()
	if err := monitor.Wait(context.Background()); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if elapsed := time.Since(start); elapsed < 30*time.Millisecond {
		t.Fatalf("Wait() elapsed = %s, fired too early", elapsed)
	}
}

func TestIdleMonitorActiveLeaseDefersAndResetsTimeout(t *testing.T) {
	monitor, err := NewIdleMonitor(50 * time.Millisecond)
	if err != nil {
		t.Fatalf("NewIdleMonitor() error = %v", err)
	}
	end := monitor.Begin()
	result := make(chan error, 1)
	go func() {
		result <- monitor.Wait(context.Background())
	}()

	time.Sleep(80 * time.Millisecond)
	select {
	case err := <-result:
		t.Fatalf("Wait() returned during active lease: %v", err)
	default:
	}
	endedAt := time.Now()
	end()
	end()

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Wait() error = %v", err)
		}
		if elapsed := time.Since(endedAt); elapsed < 35*time.Millisecond {
			t.Fatalf("Wait() elapsed after End = %s, want reset timeout", elapsed)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Wait() did not fire after lease ended")
	}
}

func TestIdleMonitorTouchResetsTimeoutAndWaitHonorsCancellation(t *testing.T) {
	monitor, err := NewIdleMonitor(60 * time.Millisecond)
	if err != nil {
		t.Fatalf("NewIdleMonitor() error = %v", err)
	}
	result := make(chan error, 1)
	go func() {
		result <- monitor.Wait(context.Background())
	}()
	time.Sleep(40 * time.Millisecond)
	touchedAt := time.Now()
	monitor.Touch()
	select {
	case err := <-result:
		t.Fatalf("Wait() returned before reset timeout: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Wait() error = %v", err)
		}
		if elapsed := time.Since(touchedAt); elapsed < 50*time.Millisecond {
			t.Fatalf("Wait() elapsed after Touch = %s, want reset timeout", elapsed)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Wait() did not fire after Touch timeout")
	}

	cancelMonitor, err := NewIdleMonitor(time.Second)
	if err != nil {
		t.Fatalf("NewIdleMonitor(cancel) error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := cancelMonitor.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait(canceled) error = %v, want context.Canceled", err)
	}
}
