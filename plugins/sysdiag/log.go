package sysdiag

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/cprobe/catpaw/digcore/diagnose"
)

const (
	defaultTailLines = 50
	maxTailLines     = 500
	defaultGrepMax   = 20
	maxGrepMax       = 200
	maxLineBytes     = 4096
)

func registerLog(registry *diagnose.ToolRegistry) {
	registry.RegisterCategory("sysdiag_log", "sysdiag:log",
		"Log diagnostic tools (tail, grep with pattern clustering).",
		diagnose.ToolScopeLocal)

	registry.Register("sysdiag_log", diagnose.DiagnoseTool{
		Name:        "log_tail",
		Description: "Read the last N lines of a log file. Optionally filter by regex pattern.",
		Scope:       diagnose.ToolScopeLocal,
		Parameters: []diagnose.ToolParam{
			{Name: "file", Type: "string", Description: "Path to log file", Required: true},
			{Name: "lines", Type: "string", Description: fmt.Sprintf("Number of lines to read from tail (default %d, max %d)", defaultTailLines, maxTailLines)},
			{Name: "pattern", Type: "string", Description: "Optional regex filter pattern"},
		},
		Execute: execLogTail,
	})

	registry.Register("sysdiag_log", diagnose.DiagnoseTool{
		Name:        "log_grep",
		Description: "Search a log file for a pattern and return matched lines with counts. Scans from end of file for efficiency.",
		Scope:       diagnose.ToolScopeLocal,
		Parameters: []diagnose.ToolParam{
			{Name: "file", Type: "string", Description: "Path to log file", Required: true},
			{Name: "pattern", Type: "string", Description: "Regex pattern to search for", Required: true},
			{Name: "max_matches", Type: "string", Description: fmt.Sprintf("Max matching lines to return (default %d, max %d)", defaultGrepMax, maxGrepMax)},
		},
		Execute: execLogGrep,
	})
}

func execLogTail(ctx context.Context, args map[string]string) (string, error) {
	filePath, err := sanitizeLogPath(args["file"])
	if err != nil {
		return "", err
	}

	lines := defaultTailLines
	if s := args["lines"]; s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n <= 0 {
			return "", fmt.Errorf("invalid lines %q: must be a positive integer", s)
		}
		if n > maxTailLines {
			n = maxTailLines
		}
		lines = n
	}

	var filterRe *regexp.Regexp
	if p := args["pattern"]; p != "" {
		var err error
		filterRe, err = regexp.Compile(p)
		if err != nil {
			return "", fmt.Errorf("invalid pattern %q: %w", p, err)
		}
	}

	result, err := tailFile(ctx, filePath, lines, filterRe)
	if err != nil {
		return "", err
	}
	return result, nil
}

func execLogGrep(ctx context.Context, args map[string]string) (string, error) {
	filePath, err := sanitizeLogPath(args["file"])
	if err != nil {
		return "", err
	}

	pattern := args["pattern"]
	if pattern == "" {
		return "", fmt.Errorf("pattern parameter is required")
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", fmt.Errorf("invalid pattern %q: %w", pattern, err)
	}

	maxMatches := defaultGrepMax
	if s := args["max_matches"]; s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n <= 0 {
			return "", fmt.Errorf("invalid max_matches %q: must be a positive integer", s)
		}
		if n > maxGrepMax {
			n = maxGrepMax
		}
		maxMatches = n
	}

	return grepFile(ctx, filePath, re, maxMatches)
}

// tailFile reads the last N lines from a file, optionally filtering by regex.
// Uses a ring buffer to avoid loading the entire file into memory.
func tailFile(ctx context.Context, path string, n int, filter *regexp.Regexp) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	ring := make([]string, n)
	pos := 0
	matched := 0
	totalScanned := 0

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, maxLineBytes), maxLineBytes)

	for scanner.Scan() {
		if totalScanned%1024 == 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			default:
			}
		}

		line := scanner.Text()
		totalScanned++

		if filter != nil && !filter.MatchString(line) {
			continue
		}

		ring[pos%n] = line
		pos++
		matched++
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("scan %s: %w", path, err)
	}

	if matched == 0 {
		if filter != nil {
			return fmt.Sprintf("No lines matching pattern in %s (scanned %d lines).", path, totalScanned), nil
		}
		return fmt.Sprintf("File %s is empty.", path), nil
	}

	count := matched
	if count > n {
		count = n
	}

	var b strings.Builder
	if filter != nil {
		fmt.Fprintf(&b, "File: %s (scanned %d lines, showing last %d of %d matching)\n\n", path, totalScanned, count, matched)
	} else {
		fmt.Fprintf(&b, "File: %s (last %d of %d lines)\n\n", path, count, totalScanned)
	}

	start := pos - count
	for i := start; i < pos; i++ {
		b.WriteString(ring[i%n])
		b.WriteByte('\n')
	}
	return b.String(), nil
}

// grepFile scans a file and returns matching lines.
// For large files (>10MB), seeks to the last 10MB to limit scan scope.
func grepFile(ctx context.Context, path string, re *regexp.Regexp, maxMatches int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("stat %s: %w", path, err)
	}

	const seekThreshold = 10 * 1024 * 1024
	seeked := false
	var reader io.Reader = f

	if info.Size() > seekThreshold {
		if _, err := f.Seek(-seekThreshold, io.SeekEnd); err == nil {
			seeked = true
			br := bufio.NewReader(f)
			_, _ = br.ReadBytes('\n') // skip partial first line
			reader = br
		}
	}

	return grepFromReader(ctx, reader, path, re, maxMatches, seeked)
}

func grepFromReader(ctx context.Context, reader io.Reader, path string, re *regexp.Regexp, maxMatches int, seeked bool) (string, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, maxLineBytes), maxLineBytes)

	var matches []string
	totalScanned := 0
	totalMatched := 0

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}

		totalScanned++
		line := scanner.Text()
		if re.MatchString(line) {
			totalMatched++
			if len(matches) < maxMatches {
				matches = append(matches, line)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("scan %s: %w", path, err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "File: %s\n", path)
	fmt.Fprintf(&b, "Pattern: %s\n", re.String())
	if seeked {
		b.WriteString("Scope: last ~10MB of file\n")
	}
	fmt.Fprintf(&b, "Lines scanned: %d, matched: %d", totalScanned, totalMatched)
	if totalMatched > maxMatches {
		fmt.Fprintf(&b, " (showing first %d)", maxMatches)
	}
	b.WriteString("\n\n")

	for _, line := range matches {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String(), nil
}

func sanitizeLogPath(raw string) (string, error) {
	p := strings.TrimSpace(raw)
	if p == "" {
		return "", fmt.Errorf("file parameter is required")
	}
	if strings.Contains(p, "..") {
		return "", fmt.Errorf("file path must not contain '..'")
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("invalid file path: %w", err)
	}
	return abs, nil
}
