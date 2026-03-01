package mount

import (
	"runtime"
	"strings"
	"testing"

	"github.com/cprobe/catpaw/pkg/safe"
	"github.com/cprobe/catpaw/types"
)

func skipIfNotLinux(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("mount plugin is linux-only")
	}
}

// --- unescapeOctal ---

func TestUnescapeOctal(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"/mnt/data", "/mnt/data"},
		{`/mnt/my\040data`, "/mnt/my data"},
		{`/mnt/tab\011here`, "/mnt/tab\there"},
		{`/mnt/back\134slash`, `/mnt/back\slash`},
		{`/mnt/a\040b\040c`, "/mnt/a b c"},
		{`\040start`, " start"},
		{`end\040`, "end "},
		{`no_escape`, "no_escape"},
		{`incomplete\04`, `incomplete\04`},
	}
	for _, tt := range tests {
		got := unescapeOctal(tt.input)
		if got != tt.want {
			t.Errorf("unescapeOctal(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// --- parseMountLines ---

func TestParseMountLines(t *testing.T) {
	content := `/dev/sda1 / ext4 rw,relatime,errors=remount-ro 0 0
tmpfs /tmp tmpfs rw,nosuid,nodev,noexec,relatime 0 0
192.168.1.100:/share /backup nfs rw,relatime,vers=4.2,addr=192.168.1.100 0 0
/dev/sdb1 /mnt/my\040data ext4 rw,relatime 0 0
`

	m, err := parseMountLines(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(m) != 4 {
		t.Fatalf("expected 4 entries, got %d", len(m))
	}

	root, ok := m["/"]
	if !ok {
		t.Fatal("missing /")
	}
	if root.fsType != "ext4" {
		t.Errorf("/ fsType = %q, want ext4", root.fsType)
	}
	if !root.options["rw"] || !root.options["relatime"] {
		t.Error("/ missing expected options")
	}

	tmp, ok := m["/tmp"]
	if !ok {
		t.Fatal("missing /tmp")
	}
	if tmp.fsType != "tmpfs" {
		t.Errorf("/tmp fsType = %q, want tmpfs", tmp.fsType)
	}
	for _, opt := range []string{"noexec", "nosuid", "nodev"} {
		if !tmp.options[opt] {
			t.Errorf("/tmp missing option %s", opt)
		}
	}

	backup, ok := m["/backup"]
	if !ok {
		t.Fatal("missing /backup")
	}
	if backup.fsType != "nfs" {
		t.Errorf("/backup fsType = %q, want nfs", backup.fsType)
	}

	data, ok := m["/mnt/my data"]
	if !ok {
		t.Fatal("missing /mnt/my data (octal-escaped path)")
	}
	if data.fsType != "ext4" {
		t.Errorf("/mnt/my data fsType = %q, want ext4", data.fsType)
	}
}

func TestParseMountLinesEmpty(t *testing.T) {
	m, err := parseMountLines("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(m) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(m))
	}
}

func TestParseMountLinesSkipsShortLines(t *testing.T) {
	content := "malformed line\n/dev/sda1 / ext4 rw 0 0\n"
	m, err := parseMountLines(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(m) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(m))
	}
}

// --- formatExpect ---

func TestFormatExpect(t *testing.T) {
	tests := []struct {
		name string
		spec MountSpec
		want string
	}{
		{"path only", MountSpec{Path: "/data"}, "mounted"},
		{"with fstype", MountSpec{Path: "/data", FSType: "ext4"}, "ext4"},
		{"with options", MountSpec{Path: "/tmp", Options: []string{"noexec", "nosuid"}}, "noexec,nosuid"},
		{"fstype + options", MountSpec{Path: "/tmp", FSType: "tmpfs", Options: []string{"noexec"}}, "tmpfs, noexec"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatExpect(&tt.spec)
			if got != tt.want {
				t.Errorf("formatExpect() = %q, want %q", got, tt.want)
			}
		})
	}
}

// --- normalizeSeverity ---

func TestNormalizeSeverityDefaults(t *testing.T) {
	s := ""
	if err := normalizeSeverity(&s); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s != "Warning" {
		t.Fatalf("expected default 'Warning', got %q", s)
	}
}

func TestNormalizeSeverityAcceptsValid(t *testing.T) {
	for _, sev := range []string{"Info", "Warning", "Critical"} {
		s := sev
		if err := normalizeSeverity(&s); err != nil {
			t.Fatalf("should accept %q: %v", sev, err)
		}
	}
}

func TestNormalizeSeverityRejectsInvalid(t *testing.T) {
	for _, sev := range []string{"Ok", "Fatal", "error"} {
		s := sev
		if err := normalizeSeverity(&s); err == nil {
			t.Fatalf("should reject %q", sev)
		}
	}
}

// --- Init tests ---

func TestInitPlatformGuard(t *testing.T) {
	ins := &Instance{
		Mounts: []MountSpec{{Path: "/data"}},
	}
	err := ins.Init()
	if runtime.GOOS == "linux" && err != nil {
		t.Fatalf("should accept on Linux: %v", err)
	}
	if runtime.GOOS != "linux" && err == nil {
		t.Fatal("should reject on non-Linux")
	}
}

func TestInitRejectsEmptyMounts(t *testing.T) {
	skipIfNotLinux(t)
	ins := &Instance{}
	if err := ins.Init(); err == nil {
		t.Fatal("should reject empty mounts")
	}
}

func TestInitRejectsEmptyPath(t *testing.T) {
	skipIfNotLinux(t)
	ins := &Instance{
		Mounts: []MountSpec{{Path: ""}},
	}
	if err := ins.Init(); err == nil {
		t.Fatal("should reject empty path")
	}
}

func TestInitRejectsRelativePath(t *testing.T) {
	skipIfNotLinux(t)
	ins := &Instance{
		Mounts: []MountSpec{{Path: "data"}},
	}
	if err := ins.Init(); err == nil {
		t.Fatal("should reject relative path")
	}
}

func TestInitRejectsDuplicatePath(t *testing.T) {
	skipIfNotLinux(t)
	ins := &Instance{
		Mounts: []MountSpec{
			{Path: "/data"},
			{Path: "/data"},
		},
	}
	if err := ins.Init(); err == nil {
		t.Fatal("should reject duplicate paths")
	}
}

func TestInitRejectsEmptyOption(t *testing.T) {
	skipIfNotLinux(t)
	ins := &Instance{
		Mounts: []MountSpec{{Path: "/tmp", Options: []string{"noexec", ""}}},
	}
	if err := ins.Init(); err == nil {
		t.Fatal("should reject empty option")
	}
}

func TestInitRejectsInvalidSeverity(t *testing.T) {
	skipIfNotLinux(t)
	ins := &Instance{
		Mounts: []MountSpec{{Path: "/data", Severity: "Fatal"}},
	}
	if err := ins.Init(); err == nil {
		t.Fatal("should reject invalid severity")
	}
}

func TestInitAcceptsValidConfig(t *testing.T) {
	skipIfNotLinux(t)
	ins := &Instance{
		Mounts: []MountSpec{
			{Path: "/data", FSType: "ext4", Severity: "Critical"},
			{Path: "/tmp", Options: []string{"noexec", "nosuid", "nodev"}},
			{Path: "/backup", FSType: "nfs"},
		},
	}
	if err := ins.Init(); err != nil {
		t.Fatalf("should accept valid config: %v", err)
	}
	if ins.Mounts[1].Severity != "Warning" {
		t.Fatalf("expected default severity 'Warning', got %q", ins.Mounts[1].Severity)
	}
}

func TestInitNormalizesFSType(t *testing.T) {
	skipIfNotLinux(t)
	ins := &Instance{
		Mounts: []MountSpec{{Path: "/data", FSType: " EXT4 "}},
	}
	if err := ins.Init(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ins.Mounts[0].FSType != "ext4" {
		t.Fatalf("expected normalized 'ext4', got %q", ins.Mounts[0].FSType)
	}
}

func TestInitTrimsOptionSpaces(t *testing.T) {
	skipIfNotLinux(t)
	ins := &Instance{
		Mounts: []MountSpec{{Path: "/tmp", Options: []string{" noexec ", " nosuid "}}},
	}
	if err := ins.Init(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ins.Mounts[0].Options[0] != "noexec" || ins.Mounts[0].Options[1] != "nosuid" {
		t.Fatalf("expected trimmed options, got %v", ins.Mounts[0].Options)
	}
}

func TestInitNormalizesTrailingSlash(t *testing.T) {
	skipIfNotLinux(t)
	ins := &Instance{
		Mounts: []MountSpec{
			{Path: "/data/"},
			{Path: "/var/log///"},
			{Path: "/"},
		},
	}
	if err := ins.Init(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ins.Mounts[0].Path != "/data" {
		t.Errorf("expected '/data', got %q", ins.Mounts[0].Path)
	}
	if ins.Mounts[1].Path != "/var/log" {
		t.Errorf("expected '/var/log', got %q", ins.Mounts[1].Path)
	}
	if ins.Mounts[2].Path != "/" {
		t.Errorf("root path should stay '/', got %q", ins.Mounts[2].Path)
	}
}

func TestInitTrailingSlashDedup(t *testing.T) {
	skipIfNotLinux(t)
	ins := &Instance{
		Mounts: []MountSpec{
			{Path: "/data"},
			{Path: "/data/"},
		},
	}
	if err := ins.Init(); err == nil {
		t.Fatal("should reject duplicate paths after trailing slash normalization")
	}
}

func TestInitAllowsFstabOnly(t *testing.T) {
	skipIfNotLinux(t)
	ins := &Instance{
		Fstab: FstabCheck{Enabled: true},
	}
	if err := ins.Init(); err != nil {
		t.Fatalf("should accept fstab-only config: %v", err)
	}
	if ins.Fstab.Severity != "Warning" {
		t.Errorf("expected default severity 'Warning', got %q", ins.Fstab.Severity)
	}
	expectedExclude := []string{"tmpfs", "devtmpfs", "squashfs", "overlay"}
	if len(ins.Fstab.ExcludeFSTypes) != len(expectedExclude) {
		t.Errorf("expected default exclude_fstype %v, got %v", expectedExclude, ins.Fstab.ExcludeFSTypes)
	}
}

func TestInitRejectsNoMountsNoFstab(t *testing.T) {
	skipIfNotLinux(t)
	ins := &Instance{}
	if err := ins.Init(); err == nil {
		t.Fatal("should reject when neither mounts nor fstab configured")
	}
}

// --- checkMount unit tests ---

func buildMountMap(lines string) map[string]mountEntry {
	m, _ := parseMountLines(lines)
	return m
}

func popEvent(q *safe.Queue[*types.Event]) *types.Event {
	v := q.PopBack()
	if v == nil {
		return nil
	}
	return *v
}

func TestCheckMountNotMounted(t *testing.T) {
	ins := &Instance{}
	q := safe.NewQueue[*types.Event]()
	mountMap := buildMountMap("/dev/sda1 / ext4 rw,relatime 0 0\n")

	spec := &MountSpec{Path: "/data", FSType: "ext4", Severity: "Critical"}
	ins.checkMount(q, spec, mountMap)

	if q.Len() != 1 {
		t.Fatalf("expected 1 event, got %d", q.Len())
	}
	ev := popEvent(q)
	if ev.EventStatus != "Critical" {
		t.Errorf("expected Critical, got %s", ev.EventStatus)
	}
	if !strings.Contains(ev.Description, "not mounted") {
		t.Errorf("description should mention 'not mounted', got %q", ev.Description)
	}
	if ev.Labels[types.AttrPrefix+"actual"] != "not mounted" {
		t.Errorf("_attr_actual should be 'not mounted', got %q", ev.Labels[types.AttrPrefix+"actual"])
	}
}

func TestCheckMountFSTypeMismatch(t *testing.T) {
	ins := &Instance{}
	q := safe.NewQueue[*types.Event]()
	mountMap := buildMountMap("/dev/sda1 /data xfs rw,relatime 0 0\n")

	spec := &MountSpec{Path: "/data", FSType: "ext4", Severity: "Warning"}
	ins.checkMount(q, spec, mountMap)

	if q.Len() != 1 {
		t.Fatalf("expected 1 event, got %d", q.Len())
	}
	ev := popEvent(q)
	if ev.EventStatus != "Warning" {
		t.Errorf("expected Warning, got %s", ev.EventStatus)
	}
	if !strings.Contains(ev.Description, "xfs") || !strings.Contains(ev.Description, "ext4") {
		t.Errorf("description should mention actual and expected fstype, got %q", ev.Description)
	}
}

func TestCheckMountMissingOptions(t *testing.T) {
	ins := &Instance{}
	q := safe.NewQueue[*types.Event]()
	mountMap := buildMountMap("tmpfs /tmp tmpfs rw,relatime,nodev 0 0\n")

	spec := &MountSpec{Path: "/tmp", Options: []string{"noexec", "nosuid", "nodev"}, Severity: "Warning"}
	ins.checkMount(q, spec, mountMap)

	if q.Len() != 1 {
		t.Fatalf("expected 1 event, got %d", q.Len())
	}
	ev := popEvent(q)
	if ev.EventStatus != "Warning" {
		t.Errorf("expected Warning, got %s", ev.EventStatus)
	}
	if !strings.Contains(ev.Description, "noexec") || !strings.Contains(ev.Description, "nosuid") {
		t.Errorf("description should list missing options, got %q", ev.Description)
	}
	if strings.Contains(ev.Description, "nodev") && strings.Contains(ev.Description, "missing mount options: nodev") {
		t.Error("nodev is present, should not be listed as missing")
	}
}

func TestCheckMountAllOk(t *testing.T) {
	ins := &Instance{}
	q := safe.NewQueue[*types.Event]()
	mountMap := buildMountMap("tmpfs /tmp tmpfs rw,nosuid,nodev,noexec,relatime 0 0\n")

	spec := &MountSpec{Path: "/tmp", FSType: "tmpfs", Options: []string{"noexec", "nosuid", "nodev"}, Severity: "Warning"}
	ins.checkMount(q, spec, mountMap)

	if q.Len() != 1 {
		t.Fatalf("expected 1 event, got %d", q.Len())
	}
	ev := popEvent(q)
	if ev.EventStatus != "Ok" {
		t.Errorf("expected Ok, got %s", ev.EventStatus)
	}
	if !strings.Contains(ev.Description, "expected options") {
		t.Errorf("description should mention expected options, got %q", ev.Description)
	}
}

func TestCheckMountPathOnlyOk(t *testing.T) {
	ins := &Instance{}
	q := safe.NewQueue[*types.Event]()
	mountMap := buildMountMap("/dev/sda1 /data ext4 rw,relatime 0 0\n")

	spec := &MountSpec{Path: "/data", Severity: "Warning"}
	ins.checkMount(q, spec, mountMap)

	if q.Len() != 1 {
		t.Fatalf("expected 1 event, got %d", q.Len())
	}
	ev := popEvent(q)
	if ev.EventStatus != "Ok" {
		t.Errorf("expected Ok, got %s", ev.EventStatus)
	}
	if ev.Labels[types.AttrPrefix+"expect"] != "mounted" {
		t.Errorf("_attr_expect should be 'mounted', got %q", ev.Labels[types.AttrPrefix+"expect"])
	}
}

// --- parseFstabLines tests ---

func TestParseFstabLines(t *testing.T) {
	content := `# /etc/fstab: static file system information
UUID=xxxx / ext4 defaults 0 1
UUID=yyyy /boot ext4 defaults 0 2
/dev/sda2 none swap sw 0 0
192.168.1.100:/share /backup nfs defaults 0 0
tmpfs /tmp tmpfs defaults,noexec,nosuid,nodev 0 0
/dev/sdb1 /data ext4 defaults,noauto 0 0
`
	entries := parseFstabLines(content)
	if len(entries) != 6 {
		t.Fatalf("expected 6 entries, got %d", len(entries))
	}
	if entries[0].mountPoint != "/" || entries[0].fsType != "ext4" {
		t.Errorf("first entry: got mountPoint=%q fsType=%q", entries[0].mountPoint, entries[0].fsType)
	}
	if entries[2].fsType != "swap" || entries[2].mountPoint != "none" {
		t.Errorf("swap entry: got mountPoint=%q fsType=%q", entries[2].mountPoint, entries[2].fsType)
	}
	if entries[4].fsType != "tmpfs" {
		t.Errorf("tmpfs entry: got fsType=%q", entries[4].fsType)
	}
}

func TestParseFstabLinesEmpty(t *testing.T) {
	entries := parseFstabLines("")
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(entries))
	}
}

func TestParseFstabLinesSkipsComments(t *testing.T) {
	content := "# comment\n\n# another\nUUID=x / ext4 defaults 0 1\n"
	entries := parseFstabLines(content)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
}

func TestHasOption(t *testing.T) {
	opts := []string{"defaults", "noauto", "noexec"}
	if !hasOption(opts, "noauto") {
		t.Error("should find noauto")
	}
	if hasOption(opts, "nosuid") {
		t.Error("should not find nosuid")
	}
}

// --- fstab filtering integration test (without actual filesystem) ---

func TestFstabFilteringLogic(t *testing.T) {
	fstabContent := `UUID=xxxx / ext4 defaults 0 1
/dev/sda2 none swap sw 0 0
UUID=yyyy /boot ext4 defaults 0 2
/dev/sdc1 /data ext4 defaults 0 0
/dev/sdd1 /mnt/usb ext4 defaults,noauto 0 0
tmpfs /tmp tmpfs defaults,noexec 0 0
`
	entries := parseFstabLines(fstabContent)

	manualPaths := map[string]bool{"/": true}
	excludeFSTypes := map[string]bool{"swap": true}
	excludePaths := map[string]bool{"/tmp": true}

	var checked []string
	for _, entry := range entries {
		if entry.mountPoint == "none" || entry.fsType == "swap" {
			continue
		}
		if excludeFSTypes[entry.fsType] || excludePaths[entry.mountPoint] {
			continue
		}
		if hasOption(entry.options, "noauto") {
			continue
		}
		if manualPaths[entry.mountPoint] {
			continue
		}
		checked = append(checked, entry.mountPoint)
	}

	expected := []string{"/boot", "/data"}
	if len(checked) != len(expected) {
		t.Fatalf("expected %v, got %v", expected, checked)
	}
	for i, p := range expected {
		if checked[i] != p {
			t.Errorf("expected[%d] = %q, got %q", i, p, checked[i])
		}
	}
}
