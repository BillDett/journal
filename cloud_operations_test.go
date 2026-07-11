package main

import (
	"context"
	"testing"
	"time"
)

func TestCloudOperationRunnerCoalescesAndCancels(t *testing.T) {
	runner := NewCloudOperationRunner()
	started := make(chan struct{})
	status, startedNew := runner.Start(context.Background(), "journal", CloudOperationSync, func(ctx context.Context) error { close(started); <-ctx.Done(); return ctx.Err() })
	if !startedNew || status.State != "running" {
		t.Fatalf("start: %#v", status)
	}
	<-started
	_, startedNew = runner.Start(context.Background(), "journal", CloudOperationSync, func(context.Context) error { return nil })
	if startedNew {
		t.Fatal("duplicate operation should join")
	}
	if !runner.Cancel("journal") {
		t.Fatal("cancel should find operation")
	}
	time.Sleep(10 * time.Millisecond)
	if _, ok := runner.Status("journal"); ok {
		t.Fatal("canceled operation should be released")
	}
}

func TestDiagnosticsRedactSecrets(t *testing.T) {
	data, err := BuildCloudDiagnostics(CloudJournalMountRecord{CloudJournalID: "id", CachePath: "/private/cache", LastSyncError: "authorization token secret"}, VaultProvider{ID: "filesystem"}, VaultCapabilities{})
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, needle := range []string{"/private/cache", "token secret"} {
		if contains(text, needle) {
			t.Fatalf("diagnostic leaked %q: %s", needle, text)
		}
	}
}
