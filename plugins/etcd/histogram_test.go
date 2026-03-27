package etcd

import (
	"math"
	"strings"
	"testing"
)

func TestHistogramQuantile(t *testing.T) {
	h := &histogram{
		Buckets: []bucket{
			{UpperBound: 0.001, Count: 0},
			{UpperBound: 0.002, Count: 0},
			{UpperBound: 0.004, Count: 0},
			{UpperBound: 0.008, Count: 0},
			{UpperBound: 0.016, Count: 4},
			{UpperBound: 0.032, Count: 5},
			{UpperBound: 0.064, Count: 6},
			{UpperBound: 0.128, Count: 7},
			{UpperBound: 0.256, Count: 8},
			{UpperBound: 0.512, Count: 9},
			{UpperBound: 1.024, Count: 10},
			{UpperBound: math.Inf(1), Count: 10},
		},
		Sum:   0.250,
		Count: 10,
	}

	tests := []struct {
		phi  float64
		want float64
	}{
		{0.5, 0.032},
		{0.9, 0.512},
		{0.99, 0.9728},
	}

	for _, tc := range tests {
		got, err := histogramQuantile(tc.phi, h)
		if err != nil {
			t.Fatalf("phi=%v: %v", tc.phi, err)
		}
		if math.Abs(got-tc.want) > 0.001 {
			t.Errorf("phi=%v: got=%v, want=%v", tc.phi, got, tc.want)
		}
	}
}

func TestHistogramQuantile_EmptyBuckets(t *testing.T) {
	h := &histogram{}
	_, err := histogramQuantile(0.5, h)
	if err == nil {
		t.Error("expected error for empty buckets")
	}
}

func TestHistogramQuantile_ZeroCount(t *testing.T) {
	h := &histogram{
		Buckets: []bucket{
			{UpperBound: 0.01, Count: 0},
			{UpperBound: math.Inf(1), Count: 0},
		},
	}
	got, err := histogramQuantile(0.5, h)
	if err != nil {
		t.Fatal(err)
	}
	if !math.IsNaN(got) {
		t.Errorf("expected NaN for zero count, got %v", got)
	}
}

func TestParseHistogram(t *testing.T) {
	const metrics = `# HELP etcd_disk_backend_commit_duration_seconds The latency distributions of commit called by backend.
# TYPE etcd_disk_backend_commit_duration_seconds histogram
etcd_disk_backend_commit_duration_seconds_bucket{le="0.001"} 0
etcd_disk_backend_commit_duration_seconds_bucket{le="0.002"} 0
etcd_disk_backend_commit_duration_seconds_bucket{le="0.004"} 0
etcd_disk_backend_commit_duration_seconds_bucket{le="0.008"} 0
etcd_disk_backend_commit_duration_seconds_bucket{le="0.016"} 150
etcd_disk_backend_commit_duration_seconds_bucket{le="0.032"} 500
etcd_disk_backend_commit_duration_seconds_bucket{le="0.064"} 800
etcd_disk_backend_commit_duration_seconds_bucket{le="0.128"} 950
etcd_disk_backend_commit_duration_seconds_bucket{le="0.256"} 990
etcd_disk_backend_commit_duration_seconds_bucket{le="0.512"} 999
etcd_disk_backend_commit_duration_seconds_bucket{le="1.024"} 1000
etcd_disk_backend_commit_duration_seconds_bucket{le="+Inf"} 1000
etcd_disk_backend_commit_duration_seconds_sum 23.456
etcd_disk_backend_commit_duration_seconds_count 1000
# some other metric
etcd_server_proposals_applied_total 12345
`

	h, err := parseHistogram(strings.NewReader(metrics), "etcd_disk_backend_commit_duration_seconds", 8<<20)
	if err != nil {
		t.Fatal(err)
	}

	if len(h.Buckets) != 12 {
		t.Fatalf("expected 12 buckets, got %d", len(h.Buckets))
	}
	if h.Sum != 23.456 {
		t.Errorf("sum: got %v, want 23.456", h.Sum)
	}
	if h.Count != 1000 {
		t.Errorf("count: got %v, want 1000", h.Count)
	}

	p99, err := histogramQuantile(0.99, h)
	if err != nil {
		t.Fatal(err)
	}
	if p99 < 0.1 || p99 > 1.0 {
		t.Errorf("p99=%v out of expected range [0.1, 1.0]", p99)
	}
}

func TestParseHistogram_NotFound(t *testing.T) {
	_, err := parseHistogram(strings.NewReader("some_other_metric 42\n"), "etcd_disk_backend_commit_duration_seconds", 8<<20)
	if err == nil {
		t.Error("expected error for missing metric")
	}
}

func TestQuantileCheckLabel(t *testing.T) {
	tests := []struct {
		phi  float64
		want string
	}{
		{0.50, "p50"},
		{0.90, "p90"},
		{0.95, "p95"},
		{0.99, "p99"},
		{0.999, "p999"},
	}
	for _, tc := range tests {
		got := quantileCheckLabel(tc.phi)
		if got != tc.want {
			t.Errorf("quantileCheckLabel(%v): got %q, want %q", tc.phi, got, tc.want)
		}
	}
}
