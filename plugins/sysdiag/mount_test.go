package sysdiag

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseMountLine(t *testing.T) {
	tests := []struct {
		line   string
		wantOK bool
		wantRO bool
		wantMP string
	}{
		{"/dev/sda1 / ext4 rw,relatime 0 0", true, false, "/"},
		{"/dev/sda2 /data xfs ro,noatime 0 0", true, true, "/data"},
		{"proc /proc proc rw,nosuid 0 0", true, false, "/proc"},
		{"bad line", false, false, ""},
		{"", false, false, ""},
	}

	for _, tt := range tests {
		e, ok := parseMountLine(tt.line)
		if ok != tt.wantOK {
			t.Errorf("parseMountLine(%q): ok=%v, want %v", tt.line, ok, tt.wantOK)
			continue
		}
		if ok {
			if e.readOnly != tt.wantRO {
				t.Errorf("parseMountLine(%q): readOnly=%v, want %v", tt.line, e.readOnly, tt.wantRO)
			}
			if e.mountPoint != tt.wantMP {
				t.Errorf("parseMountLine(%q): mountPoint=%q, want %q", tt.line, e.mountPoint, tt.wantMP)
			}
		}
	}
}

func TestUnescapeMountField(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"/normal/path", "/normal/path"},
		{`/mnt/my\040drive`, "/mnt/my drive"},
		{`/mnt/tab\011here`, "/mnt/tab\there"},
		{`no\escape`, `no\escape`}, // incomplete octal
	}
	for _, tt := range tests {
		got := unescapeMountField(tt.input)
		if got != tt.want {
			t.Errorf("unescapeMountField(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestParseMounts(t *testing.T) {
	content := `/dev/sda1 / ext4 rw,relatime 0 0
/dev/sda2 /data xfs ro,noatime 0 0
proc /proc proc rw,nosuid,nodev,noexec,relatime 0 0
tmpfs /dev/shm tmpfs rw,nosuid,nodev 0 0
`
	dir := t.TempDir()
	path := filepath.Join(dir, "mounts")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	entries, err := parseMounts(path)
	if err != nil {
		t.Fatalf("parseMounts: %v", err)
	}
	if len(entries) != 4 {
		t.Fatalf("expected 4 entries, got %d", len(entries))
	}

	real := filterRealFS(entries)
	if len(real) != 2 {
		t.Errorf("expected 2 real FS, got %d", len(real))
	}
}

func TestFormatMountsReadOnly(t *testing.T) {
	entries := []mountEntry{
		{device: "/dev/sda1", mountPoint: "/", fsType: "ext4", options: "rw,relatime", readOnly: false},
		{device: "/dev/sda2", mountPoint: "/data", fsType: "xfs", options: "ro,noatime", readOnly: true},
	}

	out := formatMounts(entries)
	if !strings.Contains(out, "[RO]") {
		t.Fatal("expected [RO] marker in output")
	}
	if !strings.Contains(out, "READ-ONLY") {
		t.Fatal("expected READ-ONLY warning in header")
	}
}

func TestIsReadOnly(t *testing.T) {
	if !isReadOnly("ro,noatime,errors=continue") {
		t.Fatal("expected ro to be detected")
	}
	if isReadOnly("rw,relatime") {
		t.Fatal("rw should not be detected as ro")
	}
}

func TestAbbreviateOpts(t *testing.T) {
	short := "rw,relatime"
	if abbreviateOpts(short) != short {
		t.Fatal("short options should not be abbreviated")
	}

	long := strings.Repeat("a,", 50)
	result := abbreviateOpts(long)
	if len(result) > 63 {
		t.Fatalf("abbreviated opts too long: %d", len(result))
	}
	if !strings.HasSuffix(result, "...") {
		t.Fatal("expected ... suffix")
	}
}
