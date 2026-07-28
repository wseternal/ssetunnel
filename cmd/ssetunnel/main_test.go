package main

import "testing"

func TestClampAgentFlags(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		batchSize       int
		concurrency     int
		wantBatchSize   int
		wantConcurrency int
	}{
		{"defaults pass through", 16384, 1, 16384, 1},
		{"tuned values pass through", 65536, 4, 65536, 4},
		{"batch below min clamps up", 512, 1, 1024, 1},
		{"batch above max clamps down", 2 << 20, 1, 1 << 20, 1},
		{"concurrency below min clamps up", 16384, 0, 16384, 1},
		{"concurrency above max clamps down", 16384, 8, 16384, 4},
		{"both clamp", 1, 99, 1024, 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotBatch, gotConc := clampAgentFlags(tt.batchSize, tt.concurrency)
			if gotBatch != tt.wantBatchSize || gotConc != tt.wantConcurrency {
				t.Fatalf("clampAgentFlags(%d, %d) = (%d, %d), want (%d, %d)",
					tt.batchSize, tt.concurrency, gotBatch, gotConc, tt.wantBatchSize, tt.wantConcurrency)
			}
		})
	}
}
