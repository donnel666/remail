package main

import "testing"

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
