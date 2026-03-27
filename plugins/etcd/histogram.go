package etcd

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
)

// histogram holds parsed Prometheus histogram data for a single metric+label set.
type histogram struct {
	Buckets []bucket // sorted by upper bound ascending
	Sum     float64
	Count   float64
}

type bucket struct {
	UpperBound float64
	Count      float64 // cumulative
}

// histogramQuantile computes the φ-quantile from cumulative histogram buckets,
// using the same linear interpolation algorithm as Prometheus.
// It expects buckets sorted by UpperBound with a +Inf bucket present.
func histogramQuantile(phi float64, h *histogram) (float64, error) {
	if len(h.Buckets) == 0 {
		return 0, fmt.Errorf("no buckets")
	}
	if phi < 0 || phi > 1 {
		return 0, fmt.Errorf("phi must be in [0,1], got %v", phi)
	}

	buckets := ensureMonotonic(h.Buckets)

	if len(buckets) == 0 || buckets[len(buckets)-1].Count == 0 {
		return math.NaN(), nil
	}

	total := buckets[len(buckets)-1].Count
	rank := phi * total

	// Find the bucket where rank falls into.
	for i, b := range buckets {
		if b.Count >= rank {
			var (
				lower      float64
				lowerCount float64
			)
			if i > 0 {
				lower = buckets[i-1].UpperBound
				lowerCount = buckets[i-1].Count
			}
			if math.IsInf(b.UpperBound, 1) {
				// Last bucket is +Inf; approximate with previous bucket upper bound.
				if i > 0 {
					return lower, nil
				}
				return math.NaN(), nil
			}
			// Linear interpolation within the bucket.
			bucketCount := b.Count - lowerCount
			if bucketCount == 0 {
				return lower, nil
			}
			return lower + (b.UpperBound-lower)*(rank-lowerCount)/bucketCount, nil
		}
	}

	return math.NaN(), nil
}

// ensureMonotonic applies monotonicity correction to cumulative bucket counts.
// Prometheus does this to handle counter resets within scrape intervals.
func ensureMonotonic(raw []bucket) []bucket {
	if len(raw) == 0 {
		return raw
	}
	out := make([]bucket, len(raw))
	copy(out, raw)
	max := out[len(out)-1].Count
	for i := len(out) - 2; i >= 0; i-- {
		if out[i].Count > max {
			out[i].Count = max
		}
		max = out[i].Count
	}
	return out
}

// parseHistogram scans Prometheus text exposition and extracts the histogram
// for the given metric name. It collects _bucket, _sum, and _count lines.
// The reader is limited to maxBytes to prevent unbounded memory usage.
func parseHistogram(r io.Reader, metricName string, maxBytes int64) (*histogram, error) {
	lr := io.LimitReader(r, maxBytes)
	scanner := bufio.NewScanner(lr)

	bucketPrefix := metricName + "_bucket{"
	sumPrefix := metricName + "_sum"
	countPrefix := metricName + "_count"

	h := &histogram{}
	var foundBucket bool

	for scanner.Scan() {
		line := scanner.Text()
		if len(line) == 0 || line[0] == '#' {
			continue
		}

		switch {
		case strings.HasPrefix(line, bucketPrefix):
			le, count, err := parseBucketLine(line, bucketPrefix)
			if err != nil {
				continue
			}
			h.Buckets = append(h.Buckets, bucket{UpperBound: le, Count: count})
			foundBucket = true

		case strings.HasPrefix(line, sumPrefix):
			h.Sum = parseMetricValue(line)

		case strings.HasPrefix(line, countPrefix):
			h.Count = parseMetricValue(line)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan metrics: %w", err)
	}

	if !foundBucket {
		return nil, fmt.Errorf("metric %q not found or has no buckets", metricName)
	}

	sort.Slice(h.Buckets, func(i, j int) bool {
		return h.Buckets[i].UpperBound < h.Buckets[j].UpperBound
	})

	return h, nil
}

// parseBucketLine extracts (le, count) from a line like:
//
//	metric_bucket{le="0.01"} 42
func parseBucketLine(line, prefix string) (float64, float64, error) {
	rest := line[len(prefix):]
	leIdx := strings.Index(rest, "le=\"")
	if leIdx < 0 {
		return 0, 0, fmt.Errorf("no le label")
	}
	rest = rest[leIdx+4:]
	endQuote := strings.Index(rest, "\"")
	if endQuote < 0 {
		return 0, 0, fmt.Errorf("no closing quote for le")
	}
	leStr := rest[:endQuote]

	var le float64
	if leStr == "+Inf" {
		le = math.Inf(1)
	} else {
		var err error
		le, err = strconv.ParseFloat(leStr, 64)
		if err != nil {
			return 0, 0, err
		}
	}

	// Value is after the closing "}" and whitespace.
	closeBrace := strings.Index(line, "}")
	if closeBrace < 0 {
		return 0, 0, fmt.Errorf("no closing brace")
	}
	valStr := strings.TrimSpace(line[closeBrace+1:])
	count, err := strconv.ParseFloat(valStr, 64)
	if err != nil {
		return 0, 0, err
	}

	return le, count, nil
}

// parseMetricValue extracts the numeric value from "metric_name{...} VALUE" or "metric_name VALUE".
func parseMetricValue(line string) float64 {
	parts := strings.Fields(line)
	if len(parts) < 2 {
		return 0
	}
	v, _ := strconv.ParseFloat(parts[len(parts)-1], 64)
	return v
}

// quantileCheckLabel returns the check label suffix for a given phi.
// 0.90 → "p90", 0.95 → "p95", 0.999 → "p999".
func quantileCheckLabel(phi float64) string {
	scaled := phi * 100
	rounded := math.Round(scaled*10) / 10
	if rounded == math.Trunc(rounded) {
		return fmt.Sprintf("p%d", int(rounded))
	}
	s := strconv.FormatFloat(rounded, 'f', -1, 64)
	s = strings.ReplaceAll(s, ".", "")
	return "p" + s
}
