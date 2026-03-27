package watcher

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestShutdown_CallsAllStopFunctions(t *testing.T) {
	sm := NewShutdownManager(5*time.Second, testLogger())

	var called1, called2, called3 atomic.Bool

	sm.RegisterComponent("retention", func(_ context.Context) error {
		called1.Store(true)
		return nil
	})
	sm.RegisterComponent("lifecycle", func(_ context.Context) error {
		called2.Store(true)
		return nil
	})
	sm.RegisterComponent("trigger", func(_ context.Context) error {
		called3.Store(true)
		return nil
	})

	err := sm.Shutdown(context.Background())
	if err != nil {
		t.Fatalf("Shutdown returned error: %v", err)
	}

	if !called1.Load() {
		t.Error("retention stop function was not called")
	}
	if !called2.Load() {
		t.Error("lifecycle stop function was not called")
	}
	if !called3.Load() {
		t.Error("trigger stop function was not called")
	}
}

func TestShutdown_PreservesRegistrationOrder(t *testing.T) {
	sm := NewShutdownManager(5*time.Second, testLogger())

	var order []string
	ch := make(chan string, 3)

	sm.RegisterComponent("first", func(_ context.Context) error {
		ch <- "first"
		return nil
	})
	sm.RegisterComponent("second", func(_ context.Context) error {
		ch <- "second"
		return nil
	})
	sm.RegisterComponent("third", func(_ context.Context) error {
		ch <- "third"
		return nil
	})

	err := sm.Shutdown(context.Background())
	if err != nil {
		t.Fatalf("Shutdown returned error: %v", err)
	}

	close(ch)
	for name := range ch {
		order = append(order, name)
	}

	if len(order) != 3 {
		t.Fatalf("expected 3 components called, got %d", len(order))
	}
	if order[0] != "first" || order[1] != "second" || order[2] != "third" {
		t.Errorf("expected order [first second third], got %v", order)
	}
}

func TestShutdown_ReportsComponentErrors(t *testing.T) {
	sm := NewShutdownManager(5*time.Second, testLogger())

	sm.RegisterComponent("good", func(_ context.Context) error {
		return nil
	})
	sm.RegisterComponent("bad", func(_ context.Context) error {
		return fmt.Errorf("cleanup failed")
	})
	sm.RegisterComponent("also-good", func(_ context.Context) error {
		return nil
	})

	err := sm.Shutdown(context.Background())
	if err == nil {
		t.Fatal("expected error from Shutdown, got nil")
	}
	if !strings.Contains(err.Error(), "bad") {
		t.Errorf("error should mention 'bad' component: %v", err)
	}
	if !strings.Contains(err.Error(), "cleanup failed") {
		t.Errorf("error should contain component error message: %v", err)
	}
}

func TestShutdown_WithTimeout_ForcesExit(t *testing.T) {
	// Use a very short timeout so the test completes quickly.
	sm := NewShutdownManager(100*time.Millisecond, testLogger())

	var slowCalled atomic.Bool

	sm.RegisterComponent("slow", func(ctx context.Context) error {
		slowCalled.Store(true)
		// Simulate a component that takes too long.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
			return nil
		}
	})
	sm.RegisterComponent("never-reached", func(_ context.Context) error {
		t.Error("this component should not be called after timeout")
		return nil
	})

	err := sm.Shutdown(context.Background())
	if err == nil {
		t.Fatal("expected error from Shutdown due to timeout, got nil")
	}

	if !slowCalled.Load() {
		t.Error("slow component was not called")
	}

	if !strings.Contains(err.Error(), "shutdown") {
		t.Errorf("error should mention shutdown: %v", err)
	}
}

func TestShutdown_NoComponents_ReturnsNil(t *testing.T) {
	sm := NewShutdownManager(5*time.Second, testLogger())

	err := sm.Shutdown(context.Background())
	if err != nil {
		t.Fatalf("expected nil error with no components, got: %v", err)
	}
}
