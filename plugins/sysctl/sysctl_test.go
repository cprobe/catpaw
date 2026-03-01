package sysctl

import (
	"runtime"
	"testing"
)

func skipIfNotLinux(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("sysctl is linux-only")
	}
}

// --- compareValues tests (platform-independent) ---

func TestCompareEqNormalizesWhitespace(t *testing.T) {
	cases := []struct {
		actual, expect string
		want           bool
	}{
		{"4096\t131072\t6291456", "4096 131072 6291456", true},
		{"4096  131072  6291456", "4096 131072 6291456", true},
		{"  4096\t\t131072   6291456  ", "4096 131072 6291456", true},
		{"4096 131072 6291456", "4096 262144 6291456", false},
	}
	for _, tc := range cases {
		got, err := compareValues(tc.actual, tc.expect, "eq")
		if err != nil {
			t.Fatalf("compareValues(%q, %q, eq): unexpected error: %v", tc.actual, tc.expect, err)
		}
		if got != tc.want {
			t.Fatalf("compareValues(%q, %q, eq) = %v, want %v", tc.actual, tc.expect, got, tc.want)
		}
	}
}

func TestCompareNeNormalizesWhitespace(t *testing.T) {
	got, err := compareValues("4096\t131072\t6291456", "4096 131072 6291456", "ne")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got {
		t.Fatal("expected false: values are equal after whitespace normalization")
	}
}

func TestCompareEq(t *testing.T) {
	cases := []struct {
		actual, expect string
		want           bool
	}{
		{"65535", "65535", true},
		{"128", "65535", false},
		{"cubic", "cubic", true},
		{"cubic", "reno", false},
		{"0", "0", true},
	}
	for _, tc := range cases {
		got, err := compareValues(tc.actual, tc.expect, "eq")
		if err != nil {
			t.Fatalf("compareValues(%q, %q, eq): unexpected error: %v", tc.actual, tc.expect, err)
		}
		if got != tc.want {
			t.Fatalf("compareValues(%q, %q, eq) = %v, want %v", tc.actual, tc.expect, got, tc.want)
		}
	}
}

func TestCompareNe(t *testing.T) {
	got, err := compareValues("128", "65535", "ne")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Fatal("expected true for ne comparison")
	}

	got, err = compareValues("128", "128", "ne")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got {
		t.Fatal("expected false for ne comparison with equal values")
	}
}

func TestCompareNumeric(t *testing.T) {
	cases := []struct {
		actual, expect, op string
		want               bool
	}{
		{"65535", "65535", "ge", true},
		{"65536", "65535", "ge", true},
		{"128", "65535", "ge", false},
		{"10", "10", "le", true},
		{"5", "10", "le", true},
		{"15", "10", "le", false},
		{"100", "99", "gt", true},
		{"99", "99", "gt", false},
		{"98", "99", "lt", true},
		{"99", "99", "lt", false},
	}
	for _, tc := range cases {
		got, err := compareValues(tc.actual, tc.expect, tc.op)
		if err != nil {
			t.Fatalf("compareValues(%q, %q, %q): unexpected error: %v", tc.actual, tc.expect, tc.op, err)
		}
		if got != tc.want {
			t.Fatalf("compareValues(%q, %q, %q) = %v, want %v", tc.actual, tc.expect, tc.op, got, tc.want)
		}
	}
}

func TestCompareNumericLargeIntegers(t *testing.T) {
	// Values beyond float64 precision (2^53) - must use integer path
	got, err := compareValues("9007199254740993", "9007199254740992", "gt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Fatal("expected true: 9007199254740993 > 9007199254740992 (integer precision)")
	}
}

func TestCompareNumericNonNumericActual(t *testing.T) {
	_, err := compareValues("cubic", "10", "ge")
	if err == nil {
		t.Fatal("expected error for non-numeric actual with numeric op")
	}
}

func TestCompareNumericNonNumericExpect(t *testing.T) {
	_, err := compareValues("10", "abc", "ge")
	if err == nil {
		t.Fatal("expected error for non-numeric expect with numeric op")
	}
}

// --- validateKey tests (platform-independent) ---

func TestValidateKeyValid(t *testing.T) {
	keys := []string{
		"net.core.somaxconn",
		"vm.swappiness",
		"net.ipv4.ip_forward",
		"fs.file-max",
		"kernel.pid_max",
	}
	for _, k := range keys {
		if err := validateKey(k); err != nil {
			t.Fatalf("validateKey(%q) unexpected error: %v", k, err)
		}
	}
}

func TestValidateKeyRejectsSlash(t *testing.T) {
	if err := validateKey("net/core/somaxconn"); err == nil {
		t.Fatal("expected error for key with slash")
	}
}

func TestValidateKeyRejectsDotDot(t *testing.T) {
	if err := validateKey("net..core.somaxconn"); err == nil {
		t.Fatal("expected error for key with '..'")
	}
}

func TestValidateKeyRejectsLeadingDot(t *testing.T) {
	if err := validateKey(".net.core.somaxconn"); err == nil {
		t.Fatal("expected error for key starting with '.'")
	}
}

func TestValidateKeyRejectsTrailingDot(t *testing.T) {
	if err := validateKey("net.core.somaxconn."); err == nil {
		t.Fatal("expected error for key ending with '.'")
	}
}

// --- keyToPath tests (platform-independent) ---

func TestKeyToPath(t *testing.T) {
	cases := []struct {
		key  string
		want string
	}{
		{"net.core.somaxconn", "/proc/sys/net/core/somaxconn"},
		{"vm.swappiness", "/proc/sys/vm/swappiness"},
		{"net.ipv4.ip_forward", "/proc/sys/net/ipv4/ip_forward"},
		{"kernel.pid_max", "/proc/sys/kernel/pid_max"},
	}
	for _, tc := range cases {
		got := keyToPath(tc.key)
		if got != tc.want {
			t.Fatalf("keyToPath(%q) = %q, want %q", tc.key, got, tc.want)
		}
	}
}

// --- Init tests ---

func TestInitPlatformGuard(t *testing.T) {
	ins := &Instance{
		ParamCheck: ParamCheck{
			Params: []ParamSpec{{Key: "vm.swappiness", Value: "10"}},
		},
	}
	err := ins.Init()
	if runtime.GOOS == "linux" && err != nil {
		t.Fatalf("should accept on Linux: %v", err)
	}
	if runtime.GOOS != "linux" && err == nil {
		t.Fatal("should reject on non-Linux")
	}
}

func TestInitRejectsEmptyParams(t *testing.T) {
	skipIfNotLinux(t)
	ins := &Instance{
		ParamCheck: ParamCheck{Params: []ParamSpec{}},
	}
	if err := ins.Init(); err == nil {
		t.Fatal("should reject empty params")
	}
}

func TestInitRejectsEmptyKey(t *testing.T) {
	skipIfNotLinux(t)
	ins := &Instance{
		ParamCheck: ParamCheck{
			Params: []ParamSpec{{Key: "", Value: "10"}},
		},
	}
	if err := ins.Init(); err == nil {
		t.Fatal("should reject empty key")
	}
}

func TestInitRejectsEmptyExpect(t *testing.T) {
	skipIfNotLinux(t)
	ins := &Instance{
		ParamCheck: ParamCheck{
			Params: []ParamSpec{{Key: "vm.swappiness", Value: ""}},
		},
	}
	if err := ins.Init(); err == nil {
		t.Fatal("should reject empty expect")
	}
}

func TestInitRejectsInvalidOp(t *testing.T) {
	skipIfNotLinux(t)
	ins := &Instance{
		ParamCheck: ParamCheck{
			Params: []ParamSpec{{Key: "vm.swappiness", Value: "10", Op: "contains"}},
		},
	}
	if err := ins.Init(); err == nil {
		t.Fatal("should reject invalid op")
	}
}

func TestInitRejectsNonNumericExpectForNumericOp(t *testing.T) {
	skipIfNotLinux(t)
	ins := &Instance{
		ParamCheck: ParamCheck{
			Params: []ParamSpec{{Key: "vm.swappiness", Value: "abc", Op: "ge"}},
		},
	}
	if err := ins.Init(); err == nil {
		t.Fatal("should reject non-numeric expect for numeric op")
	}
}

func TestInitRejectsInvalidSeverity(t *testing.T) {
	skipIfNotLinux(t)
	cases := []string{"Fatal", "Ok"}
	for _, sev := range cases {
		ins := &Instance{
			ParamCheck: ParamCheck{
				Params: []ParamSpec{{Key: "vm.swappiness", Value: "10", Severity: sev}},
			},
		}
		if err := ins.Init(); err == nil {
			t.Fatalf("should reject invalid severity %q", sev)
		}
	}
}

func TestInitRejectsUnsafeKey(t *testing.T) {
	skipIfNotLinux(t)
	cases := []string{
		"../../etc/passwd",
		".hidden.key",
		"net/core/somaxconn",
		"net.core.somaxconn.",
	}
	for _, key := range cases {
		ins := &Instance{
			ParamCheck: ParamCheck{
				Params: []ParamSpec{{Key: key, Value: "1"}},
			},
		}
		if err := ins.Init(); err == nil {
			t.Fatalf("should reject unsafe key %q", key)
		}
	}
}

func TestInitDefaultsOpAndSeverity(t *testing.T) {
	skipIfNotLinux(t)
	ins := &Instance{
		ParamCheck: ParamCheck{
			Params: []ParamSpec{{Key: "vm.swappiness", Value: "10"}},
		},
	}
	if err := ins.Init(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ins.ParamCheck.Params[0].Op != "eq" {
		t.Fatalf("expected default op 'eq', got %q", ins.ParamCheck.Params[0].Op)
	}
	if ins.ParamCheck.Params[0].Severity != "Warning" {
		t.Fatalf("expected default severity 'Warning', got %q", ins.ParamCheck.Params[0].Severity)
	}
}

func TestInitAcceptsValidConfig(t *testing.T) {
	skipIfNotLinux(t)
	ins := &Instance{
		ParamCheck: ParamCheck{
			Params: []ParamSpec{
				{Key: "net.core.somaxconn", Value: "65535", Op: "ge"},
				{Key: "vm.swappiness", Value: "10", Op: "le"},
				{Key: "net.ipv4.tcp_sack", Value: "1", Severity: "Info"},
				{Key: "net.ipv4.ip_forward", Value: "1", Severity: "Critical"},
				{Key: "net.ipv4.tcp_congestion_control", Value: "cubic", Op: "eq"},
			},
		},
	}
	if err := ins.Init(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
