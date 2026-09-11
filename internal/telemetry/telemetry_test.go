package telemetry

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecordQueuesAndShutdownDrains(t *testing.T) {
	resetTelemetry(t)
	root := t.TempDir()
	Record(root, "launch.started", map[string]any{"safeMode": true})
	Record(root, "launch.completed", map[string]any{"exitCode": 0})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := Shutdown(ctx); err != nil {
		t.Fatal(err)
	}

	file, err := os.Open(filepath.Join(root, "state", "launcher.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	var types []string
	for scanner.Scan() {
		var event Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatal(err)
		}
		types = append(types, event.Type)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(types) != 2 || types[0] != "launch.started" || types[1] != "launch.completed" {
		t.Fatalf("event types = %#v", types)
	}
}

func TestRecordDropsWhenBoundedQueueIsFull(t *testing.T) {
	resetTelemetry(t)
	started := make(chan struct{})
	release := make(chan struct{})
	originalWrite := writeRecord
	writeRecord = func(record) {
		select {
		case <-started:
		default:
			close(started)
		}
		<-release
	}
	t.Cleanup(func() { writeRecord = originalWrite })

	Record(t.TempDir(), "blocked", nil)
	<-started
	begin := time.Now()
	for i := 0; i < defaultQueueLength*4; i++ {
		Record("unused", "overflow", nil)
	}
	if elapsed := time.Since(begin); elapsed > 250*time.Millisecond {
		t.Fatalf("bounded enqueue took %s", elapsed)
	}
	close(release)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentShutdownIsSafe(t *testing.T) {
	resetTelemetry(t)
	Record(t.TempDir(), "launch.started", nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	results := make(chan error, 2)
	go func() { results <- Shutdown(ctx) }()
	go func() { results <- Shutdown(ctx) }()
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
}

func TestShutdownHonorsDeadline(t *testing.T) {
	resetTelemetry(t)
	started := make(chan struct{})
	release := make(chan struct{})
	originalWrite := writeRecord
	writeRecord = func(record) {
		close(started)
		<-release
	}
	t.Cleanup(func() { writeRecord = originalWrite })

	Record(t.TempDir(), "launch.completed", nil)
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := Shutdown(ctx); err != context.DeadlineExceeded {
		t.Fatalf("Shutdown error = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 250*time.Millisecond {
		t.Fatalf("Shutdown exceeded bounded deadline: %s", elapsed)
	}
	close(release)
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

func resetTelemetry(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}
