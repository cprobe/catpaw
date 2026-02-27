package procnum

import (
	"runtime"
	"testing"
)

func TestInitTrimSpaceAndSetLabel(t *testing.T) {
	ins := &Instance{
		SearchCmdline: "  nginx -g daemon off;  ",
		ProcessCount:  ProcessCountCheck{CriticalLt: 1},
	}
	if err := ins.Init(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ins.searchLabel != "nginx -g daemon off;" {
		t.Fatalf("searchLabel not trimmed, got: %q", ins.searchLabel)
	}
}

func TestInitRejectsEmptySearch(t *testing.T) {
	ins := &Instance{
		ProcessCount: ProcessCountCheck{CriticalLt: 1},
	}
	if err := ins.Init(); err == nil {
		t.Fatal("should reject empty search config")
	}
}

func TestInitRejectsWhitespaceOnlySearch(t *testing.T) {
	ins := &Instance{
		SearchExecName: "   ",
		ProcessCount:   ProcessCountCheck{CriticalLt: 1},
	}
	if err := ins.Init(); err == nil {
		t.Fatal("should reject whitespace-only search config")
	}
}

func TestInitAllowsAndCombination(t *testing.T) {
	cases := []struct {
		name     string
		ins      Instance
		expected string
	}{
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
			name:     "cmdline + user",
			ins:      Instance{SearchCmdline: "worker.py", SearchUser: "deploy"},
			expected: "worker.py && user:deploy",
		},
		{
			name:     "user only",
			ins:      Instance{SearchUser: "root"},
			expected: "user:root",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ins := tc.ins
			ins.ProcessCount = ProcessCountCheck{CriticalLt: 1}
			if err := ins.Init(); err != nil {
				t.Fatalf("AND combination should be allowed: %v", err)
			}
			if ins.searchLabel != tc.expected {
				t.Fatalf("expected label %q, got %q", tc.expected, ins.searchLabel)
			}
		})
	}
}

func TestInitRejectsMixedModes(t *testing.T) {
	cases := []struct {
		name string
		ins  Instance
	}{
		{
			name: "exec + pid_file",
			ins:  Instance{SearchExecName: "nginx", SearchPidFile: "/var/run/nginx.pid"},
		},
		{
			name: "cmdline + win_service",
			ins:  Instance{SearchCmdline: "myapp", SearchWinService: "W32Time"},
		},
		{
			name: "pid_file + win_service",
			ins:  Instance{SearchPidFile: "/var/run/test.pid", SearchWinService: "W32Time"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ins := tc.ins
			ins.ProcessCount = ProcessCountCheck{CriticalLt: 1}
			if err := ins.Init(); err == nil {
				t.Fatal("should reject mixed modes")
			}
		})
	}
}

func TestInitRejectsInvalidThresholds(t *testing.T) {
	cases := []struct {
		name string
		pc   ProcessCountCheck
	}{
		{
			name: "negative threshold",
			pc:   ProcessCountCheck{CriticalLt: -1},
		},
		{
			name: "all zero",
			pc:   ProcessCountCheck{},
		},
		{
			name: "warn_lt < critical_lt",
			pc:   ProcessCountCheck{WarnLt: 1, CriticalLt: 3},
		},
		{
			name: "warn_gt > critical_gt",
			pc:   ProcessCountCheck{WarnGt: 100, CriticalGt: 50},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ins := &Instance{
				SearchExecName: "nginx",
				ProcessCount:   tc.pc,
			}
			if err := ins.Init(); err == nil {
				t.Fatalf("should reject invalid thresholds: %+v", tc.pc)
			}
		})
	}
}

func TestInitWinServicePlatformGuard(t *testing.T) {
	ins := &Instance{
		SearchWinService: "W32Time",
		ProcessCount:     ProcessCountCheck{CriticalLt: 1},
	}
	err := ins.Init()
	if runtime.GOOS == "windows" && err != nil {
		t.Fatalf("should accept on Windows: %v", err)
	}
	if runtime.GOOS != "windows" && err == nil {
		t.Fatal("should reject on non-Windows")
	}
}

func TestInitSearchModeDetection(t *testing.T) {
	cases := []struct {
		name string
		ins  Instance
		mode searchMode
	}{
		{
			name: "process mode",
			ins:  Instance{SearchExecName: "nginx"},
			mode: searchModeProcess,
		},
		{
			name: "pid_file mode",
			ins:  Instance{SearchPidFile: "/var/run/nginx.pid"},
			mode: searchModePidFile,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ins := tc.ins
			ins.ProcessCount = ProcessCountCheck{CriticalLt: 1}
			if err := ins.Init(); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ins.mode != tc.mode {
				t.Fatalf("expected mode %d, got %d", tc.mode, ins.mode)
			}
		})
	}
}

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
			name:     "win_service",
			ins:      Instance{SearchWinService: "W32Time"},
			expected: "W32Time",
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
