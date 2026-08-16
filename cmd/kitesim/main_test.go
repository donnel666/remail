package main

import (
	"context"
	"strings"
	"testing"
)

func TestRunDoesNotAcceptPasswordArgument(t *testing.T) {
	t.Setenv(passwordEnvironment, "password-from-environment")
	err := run(context.Background(), []string{"owner@example.com", "password-on-argv"})
	if err == nil || !strings.Contains(err.Error(), "unexpected positional argument") {
		t.Fatalf("error = %v, want argv rejection", err)
	}
	if strings.Contains(err.Error(), "password-on-argv") {
		t.Fatal("error echoed the rejected argument")
	}
}

func TestPasswordFromEnvironment(t *testing.T) {
	password, err := passwordFromEnvironment(func(name string) (string, bool) {
		return "secret", name == passwordEnvironment
	})
	if err != nil || password != "secret" {
		t.Fatalf("password = %q, err = %v", password, err)
	}
}
