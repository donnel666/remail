package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestBatchRange(t *testing.T) {
	tests := []struct {
		index      int
		wantStart  int
		wantFinish int
	}{
		{index: 0, wantStart: 0, wantFinish: 100000},
		{index: 1, wantStart: 100000, wantFinish: 200000},
		{index: 2, wantStart: 200000, wantFinish: 300000},
		{index: 3, wantStart: 300000, wantFinish: 345708},
	}
	for _, test := range tests {
		start, finish := batchRange(345708, 100000, test.index)
		if start != test.wantStart || finish != test.wantFinish {
			t.Fatalf("batch %d = [%d,%d), want [%d,%d)", test.index, start, finish, test.wantStart, test.wantFinish)
		}
	}
	if count := batchCount(345708, 100000); count != 4 {
		t.Fatalf("batchCount = %d, want 4", count)
	}
}

func TestRunRequiresManifestForAbnormalRecovery(t *testing.T) {
	err := run(context.Background(), config{
		batchSize: 1, chunkSize: 1, pollInterval: time.Second, restoreAbnormal: true,
	})
	if err == nil || !strings.Contains(err.Error(), "--restore-abnormal requires --manifest") {
		t.Fatalf("run() error = %v, want manifest requirement", err)
	}
}
