package procfd

import (
	"runtime"
	"testing"
)

func skipIfNotLinux(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("procfd is linux-only")
	}
}

// --- parseNofileLimit tests (platform-independent) ---

func TestParseNofileLimitNormal(t *testing.T) {
	data := `Limit                     Soft Limit           Hard Limit           Units
Max cpu time              unlimited            unlimited            seconds
Max file size             unlimited            unlimited            bytes
Max data size             unlimited            unlimited            bytes
Max stack size            8388608              unlimited            bytes
Max core file size        0                    unlimited            bytes
Max resident set          unlimited            unlimited            bytes
Max processes             30592                30592                processes
Max open files            1024                 1048576              files
Max locked memory         65536                65536                bytes
Max address space         unlimited            unlimited            bytes
Max file locks            unlimited            unlimited            files
Max pending signals       30592                30592                signals
Max msgqueue size         819200               819200               bytes
Max nice priority         0                    0
Max realtime priority     0                    0
Max realtime timeout      unlimited            unlimited            us
`
	soft, hard, err := parseNofileLimit(data, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if soft != 1024 {
		t.Fatalf("expected soft=1024, got %d", soft)
	}
	if hard != 1048576 {
		t.Fatalf("expected hard=1048576, got %d", hard)
	}
}

func TestParseNofileLimitUnlimited(t *testing.T) {
	data := `Limit                     Soft Limit           Hard Limit           Units
Max open files            unlimited            unlimited            files
`
	soft, hard, err := parseNofileLimit(data, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if soft != 0 {
		t.Fatalf("expected soft=0 (unlimited), got %d", soft)
	}
	if hard != 0 {
		t.Fatalf("expected hard=0 (unlimited), got %d", hard)
	}
}

func TestParseNofileLimitSoftUnlimitedHardNumeric(t *testing.T) {
	data := `Limit                     Soft Limit           Hard Limit           Units
Max open files            unlimited            1048576              files
`
	soft, hard, err := parseNofileLimit(data, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if soft != 0 {
		t.Fatalf("expected soft=0 (unlimited), got %d", soft)
	}
	if hard != 1048576 {
		t.Fatalf("expected hard=1048576, got %d", hard)
	}
}

func TestParseNofileLimitHighValues(t *testing.T) {
	data := `Limit                     Soft Limit           Hard Limit           Units
Max open files            65535                65535                files
`
	soft, hard, err := parseNofileLimit(data, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if soft != 65535 {
		t.Fatalf("expected soft=65535, got %d", soft)
	}
	if hard != 65535 {
		t.Fatalf("expected hard=65535, got %d", hard)
	}
}

func TestParseNofileLimitMissing(t *testing.T) {
	data := `Limit                     Soft Limit           Hard Limit           Units
Max cpu time              unlimited            unlimited            seconds
Max file size             unlimited            unlimited            bytes
`
	_, _, err := parseNofileLimit(data, 42)
	if err == nil {
		t.Fatal("expected error for missing Max open files")
	}
}

func TestParseNofileLimitEmpty(t *testing.T) {
	_, _, err := parseNofileLimit("", 1)
	if err == nil {
		t.Fatal("expected error for empty data")
	}
}

func TestParseNofileLimitMalformedShortLine(t *testing.T) {
	data := "Max open files\n"
	_, _, err := parseNofileLimit(data, 1)
	if err == nil {
		t.Fatal("expected error for short line")
	}
}

// --- buildSearchLabel tests (platform-independent) ---

func TestBuildSearchLabel(t *testing.T) {
	cases := []struct {
		name     string
		ins      Instance
		expected string
	}{
		{
			name:     "exec only",
			ins:      Instance{SearchExecName: "nginx"},
			expected: "nginx",
		},
		{
			name:     "cmdline only",
			ins:      Instance{SearchCmdline: "myapp.jar"},
			expected: "myapp.jar",
		},
		{
			name:     "pid_file",
			ins:      Instance{SearchPidFile: "/var/run/nginx.pid"},
			expected: "/var/run/nginx.pid",
		},
		{
			name:     "exec + cmdline",
			ins:      Instance{SearchExecName: "java", SearchCmdline: "myapp.jar"},
			expected: "java && myapp.jar",
		},
		{
			name:     "exec + user",
			ins:      Instance{SearchExecName: "nginx", SearchUser: "www-data"},
			expected: "nginx && user:www-data",
		},
		{
			name:     "all three",
			ins:      Instance{SearchExecName: "java", SearchCmdline: "myapp", SearchUser: "tomcat"},
			expected: "java && myapp && user:tomcat",
		},
		{
			name:     "user only",
			ins:      Instance{SearchUser: "root"},
			expected: "user:root",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.ins.buildSearchLabel()
			if got != tc.expected {
				t.Fatalf("expected %q, got %q", tc.expected, got)
			}
		})
	}
}

// --- Init tests (linux-only, since Init rejects non-linux first) ---

func TestInitPlatformGuard(t *testing.T) {
	ins := &Instance{
		SearchExecName: "nginx",
		FdUsage:        FdUsageCheck{WarnGe: 80, CriticalGe: 90},
	}
	err := ins.Init()
	if runtime.GOOS == "linux" && err != nil {
		t.Fatalf("should accept on Linux: %v", err)
	}
	if runtime.GOOS != "linux" && err == nil {
		t.Fatal("should reject on non-Linux")
	}
}

func TestInitRejectsEmptySearch(t *testing.T) {
	skipIfNotLinux(t)
	ins := &Instance{
		FdUsage: FdUsageCheck{WarnGe: 80, CriticalGe: 90},
	}
	if err := ins.Init(); err == nil {
		t.Fatal("should reject empty search config")
	}
}

func TestInitRejectsWhitespaceOnlySearch(t *testing.T) {
	skipIfNotLinux(t)
	ins := &Instance{
		SearchExecName: "   ",
		FdUsage:        FdUsageCheck{WarnGe: 80, CriticalGe: 90},
	}
	if err := ins.Init(); err == nil {
		t.Fatal("should reject whitespace-only search config")
	}
}

func TestInitRejectsMixedModes(t *testing.T) {
	skipIfNotLinux(t)
	ins := &Instance{
		SearchExecName: "nginx",
		SearchPidFile:  "/var/run/nginx.pid",
		FdUsage:        FdUsageCheck{WarnGe: 80, CriticalGe: 90},
	}
	if err := ins.Init(); err == nil {
		t.Fatal("should reject mixed search modes")
	}
}

func TestInitRejectsNoThresholds(t *testing.T) {
	skipIfNotLinux(t)
	ins := &Instance{
		SearchExecName: "nginx",
		FdUsage:        FdUsageCheck{},
	}
	if err := ins.Init(); err == nil {
		t.Fatal("should reject zero thresholds")
	}
}

func TestInitRejectsInvalidThresholds(t *testing.T) {
	skipIfNotLinux(t)
	cases := []struct {
		name string
		fd   FdUsageCheck
	}{
		{
			name: "warn negative",
			fd:   FdUsageCheck{WarnGe: -1, CriticalGe: 90},
		},
		{
			name: "critical over 100",
			fd:   FdUsageCheck{WarnGe: 80, CriticalGe: 101},
		},
		{
			name: "warn >= critical",
			fd:   FdUsageCheck{WarnGe: 90, CriticalGe: 90},
		},
		{
			name: "warn > critical",
			fd:   FdUsageCheck{WarnGe: 95, CriticalGe: 90},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ins := &Instance{
				SearchExecName: "nginx",
				FdUsage:        tc.fd,
			}
			if err := ins.Init(); err == nil {
				t.Fatalf("should reject invalid thresholds: %+v", tc.fd)
			}
		})
	}
}

func TestInitAcceptsValidConfig(t *testing.T) {
	skipIfNotLinux(t)
	cases := []struct {
		name string
		ins  Instance
	}{
		{
			name: "both thresholds",
			ins: Instance{
				SearchExecName: "nginx",
				FdUsage:        FdUsageCheck{WarnGe: 80, CriticalGe: 90},
			},
		},
		{
			name: "warn only",
			ins: Instance{
				SearchExecName: "nginx",
				FdUsage:        FdUsageCheck{WarnGe: 80},
			},
		},
		{
			name: "critical only",
			ins: Instance{
				SearchExecName: "nginx",
				FdUsage:        FdUsageCheck{CriticalGe: 90},
			},
		},
		{
			name: "pid file mode",
			ins: Instance{
				SearchPidFile: "/var/run/nginx.pid",
				FdUsage:       FdUsageCheck{WarnGe: 80, CriticalGe: 90},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ins := tc.ins
			if err := ins.Init(); err != nil {
				t.Fatalf("should accept valid config: %v", err)
			}
		})
	}
}

func TestInitDefaultConcurrency(t *testing.T) {
	skipIfNotLinux(t)
	ins := &Instance{
		SearchExecName: "nginx",
		FdUsage:        FdUsageCheck{WarnGe: 80, CriticalGe: 90},
	}
	if err := ins.Init(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ins.Concurrency != 10 {
		t.Fatalf("expected default concurrency=10, got %d", ins.Concurrency)
	}
}

func TestInitTrimSpaceAndSetLabel(t *testing.T) {
	skipIfNotLinux(t)
	ins := &Instance{
		SearchCmdline: "  nginx -g daemon off;  ",
		FdUsage:       FdUsageCheck{WarnGe: 80, CriticalGe: 90},
	}
	if err := ins.Init(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ins.searchLabel != "nginx -g daemon off;" {
		t.Fatalf("searchLabel not trimmed, got: %q", ins.searchLabel)
	}
}
