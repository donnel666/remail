package domain

import "testing"

func TestProtoStatusConstants(t *testing.T) {
	if ResourceType != "proto" || StatusPending != "pending" || StatusNormal != "normal" || StatusDeleted != "deleted" {
		t.Fatal("unexpected proto status contract")
	}
}
