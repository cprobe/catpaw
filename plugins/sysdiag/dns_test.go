package sysdiag

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestExecDnsResolve(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := execDnsResolve(ctx, map[string]string{"domain": "localhost"})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if !strings.Contains(result, "localhost") {
		t.Fatalf("expected localhost in output: %s", result)
	}
}

func TestExecDnsResolveMissingDomain(t *testing.T) {
	_, err := execDnsResolve(context.Background(), map[string]string{})
	if err == nil {
		t.Fatal("expected error for missing domain")
	}
}

func TestExecDnsResolveNonexistent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := execDnsResolve(ctx, map[string]string{"domain": "this-domain-does-not-exist-xyz123.invalid"})
	if err != nil {
		t.Fatalf("should not return error for DNS failure: %v", err)
	}
	if !strings.Contains(strings.ToLower(result), "error") {
		t.Logf("result: %s", result)
	}
}
