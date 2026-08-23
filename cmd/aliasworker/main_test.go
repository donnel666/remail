package main

import (
	"testing"
	"time"
)

func TestValidateConfig(t *testing.T) {
	valid := config{concurrency: 30, pollInterval: time.Second, bufferSize: 1000, shutdownTimeout: time.Second, credentialTypeBurst: 1}
	if err := validateConfig(valid); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	for name, cfg := range map[string]config{
		"zero concurrency":          config{concurrency: 0, pollInterval: time.Second, bufferSize: 1000, shutdownTimeout: time.Second},
		"zero poll interval":        config{concurrency: 30, pollInterval: 0, bufferSize: 1000, shutdownTimeout: time.Second},
		"small buffer":              config{concurrency: 30, pollInterval: time.Second, bufferSize: 10, shutdownTimeout: time.Second, credentialTypeBurst: 1},
		"negative startup interval": config{concurrency: 30, pollInterval: time.Second, bufferSize: 1000, shutdownTimeout: time.Second, credentialTypeBurst: 1, startupInterval: -time.Second},
		"invalid credential burst":  config{concurrency: 30, pollInterval: time.Second, bufferSize: 1000, shutdownTimeout: time.Second, credentialTypeBurst: 0},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateConfig(cfg); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
}

func TestApplyResult(t *testing.T) {
	assigned := map[string]struct{}{"task-1": {}}
	retryAfter := map[string]time.Time{}
	applyResult(aliasTaskResult{id: "task-1", retryAt: time.Now().Add(time.Minute)}, assigned, retryAfter)
	if _, ok := assigned["task-1"]; ok {
		t.Fatal("completed task remained assigned")
	}
	if _, ok := retryAfter["task-1"]; !ok {
		t.Fatal("retry time was not recorded")
	}
	applyResult(aliasTaskResult{id: "task-1"}, assigned, retryAfter)
	if _, ok := retryAfter["task-1"]; ok {
		t.Fatal("retry time was not cleared")
	}
}
