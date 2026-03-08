package main

import (
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

type blockingHandler struct {
	entered chan struct{}
	release chan struct{}
	done    chan struct{}
	once    sync.Once
}

func newBlockingHandler() *blockingHandler {
	return &blockingHandler{
		entered: make(chan struct{}),
		release: make(chan struct{}),
		done:    make(chan struct{}),
	}
}

func (h *blockingHandler) Shutdown() {
	h.once.Do(func() {
		close(h.entered)
	})
	<-h.release
	close(h.done)
}

type observingClient struct {
	stopCalled chan struct{}
	once       sync.Once
}

func newObservingClient() *observingClient {
	return &observingClient{stopCalled: make(chan struct{})}
}

func (c *observingClient) Stop() error {
	c.once.Do(func() {
		close(c.stopCalled)
	})
	return nil
}

func TestOrchestrateShutdown_StopsClientBeforeWaitingForHandler(t *testing.T) {
	handler := newBlockingHandler()
	client := newObservingClient()

	orchestrateDone := make(chan struct{})
	go func() {
		orchestrateShutdown(handler, client)
		close(orchestrateDone)
	}()

	select {
	case <-handler.entered:
	case <-time.After(time.Second):
		t.Fatal("shutdown handler did not start")
	}

	select {
	case <-client.stopCalled:
	case <-time.After(time.Second):
		t.Fatal("client stop was not called while shutdown handler was blocked")
	}

	select {
	case <-orchestrateDone:
		t.Fatal("orchestration returned before shutdown handler unblocked")
	default:
	}

	close(handler.release)

	select {
	case <-handler.done:
	case <-time.After(time.Second):
		t.Fatal("shutdown handler did not finish after release")
	}

	select {
	case <-orchestrateDone:
	case <-time.After(time.Second):
		t.Fatal("orchestration did not finish after shutdown handler completed")
	}
}

func TestRunMainLoop_WaitsForShutdownAfterSignal(t *testing.T) {
	idleRelease := make(chan struct{})
	shutdownEntered := make(chan struct{})
	shutdownRelease := make(chan struct{})

	sigCh := make(chan os.Signal, 1)
	runDone := make(chan struct{})

	go func() {
		runMainLoop(func() {
			<-idleRelease
		}, sigCh, func() {
			close(shutdownEntered)
			<-shutdownRelease
		})
		close(runDone)
	}()

	sigCh <- syscall.SIGTERM

	select {
	case <-shutdownEntered:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not start after signal")
	}

	close(idleRelease)

	select {
	case <-runDone:
		t.Fatal("run loop returned before shutdown completed")
	default:
	}

	close(shutdownRelease)

	select {
	case <-runDone:
	case <-time.After(time.Second):
		t.Fatal("run loop did not return after shutdown completed")
	}
}

func TestRunMainLoop_NoSignalNoShutdown(t *testing.T) {
	var shutdownCalls atomic.Int32
	sigCh := make(chan os.Signal, 1)
	runDone := make(chan struct{})

	go func() {
		runMainLoop(func() {}, sigCh, func() {
			shutdownCalls.Add(1)
		})
		close(runDone)
	}()

	select {
	case <-runDone:
	case <-time.After(time.Second):
		t.Fatal("run loop did not return after idle completed")
	}

	if got := shutdownCalls.Load(); got != 0 {
		t.Fatalf("expected no shutdown calls without signal, got %d", got)
	}
}
