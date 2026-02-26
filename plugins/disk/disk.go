package disk

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"flashcat.cloud/catpaw/config"
	"flashcat.cloud/catpaw/logger"
	"flashcat.cloud/catpaw/pkg/filter"
	"flashcat.cloud/catpaw/pkg/safe"
	"flashcat.cloud/catpaw/plugins"
	"flashcat.cloud/catpaw/types"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/toolkits/pkg/concurrent/semaphore"
)

const pluginName = "disk"

type Instance struct {
	config.InternalConfig

	MountPoints       []string `toml:"mount_points"`
	IgnoreMountPoints []string `toml:"ignore_mount_points"`
	IgnoreFSTypes     []string `toml:"ignore_fs_types"`

	WarnIfUsedPercentGe     float64 `toml:"warn_if_used_percent_ge"`
	CriticalIfUsedPercentGe float64 `toml:"critical_if_used_percent_ge"`

	WarnIfInodesUsedPercentGe     float64 `toml:"warn_if_inodes_used_percent_ge"`
	CriticalIfInodesUsedPercentGe float64 `toml:"critical_if_inodes_used_percent_ge"`

	CheckWritable    bool   `toml:"check_writable"`
	WritableTestFile string `toml:"writable_test_file"`

	Concurrency   int             `toml:"concurrency"`
	GatherTimeout config.Duration `toml:"gather_timeout"`
	Check         string          `toml:"check"`

	mountFilter filter.Filter
	inFlight    sync.Map // mountPoint -> int64 (unix timestamp)
	prevHung    sync.Map // mountPoint -> bool
}

type DiskPlugin struct {
	config.InternalConfig
	Instances []*Instance `toml:"instances"`
}

func (p *DiskPlugin) GetInstances() []plugins.Instance {
	ret := make([]plugins.Instance, len(p.Instances))
	for i := 0; i < len(p.Instances); i++ {
		ret[i] = p.Instances[i]
	}
	return ret
}

func init() {
	plugins.Add(pluginName, func() plugins.Plugin {
		return &DiskPlugin{}
	})
}

func (ins *Instance) Init() error {
	if ins.Concurrency == 0 {
		ins.Concurrency = 10
	}

	if ins.GatherTimeout == 0 {
		ins.GatherTimeout = config.Duration(10 * time.Second)
	}

	if ins.WritableTestFile == "" {
		ins.WritableTestFile = ".catpaw_disk_check"
	}

	if ins.Check == "" {
		ins.Check = "disk check"
	}

	if ins.WarnIfUsedPercentGe > 0 && ins.CriticalIfUsedPercentGe > 0 &&
		ins.WarnIfUsedPercentGe >= ins.CriticalIfUsedPercentGe {
		return fmt.Errorf("warn_if_used_percent_ge(%.1f) must be less than critical_if_used_percent_ge(%.1f)",
			ins.WarnIfUsedPercentGe, ins.CriticalIfUsedPercentGe)
	}

	if ins.WarnIfInodesUsedPercentGe > 0 && ins.CriticalIfInodesUsedPercentGe > 0 &&
		ins.WarnIfInodesUsedPercentGe >= ins.CriticalIfInodesUsedPercentGe {
		return fmt.Errorf("warn_if_inodes_used_percent_ge(%.1f) must be less than critical_if_inodes_used_percent_ge(%.1f)",
			ins.WarnIfInodesUsedPercentGe, ins.CriticalIfInodesUsedPercentGe)
	}

	f, err := filter.NewIncludeExcludeFilter(ins.MountPoints, ins.IgnoreMountPoints)
	if err != nil {
		return fmt.Errorf("failed to compile mount point filter: %v", err)
	}
	ins.mountFilter = f

	return nil
}

func (ins *Instance) Gather(q *safe.Queue[*types.Event]) {
	if !ins.GetInitialized() {
		if err := ins.Init(); err != nil {
			logger.Logger.Errorw("failed to init disk plugin instance", "error", err)
			return
		}
		ins.SetInitialized()
	}

	partitions, err := disk.Partitions(true)
	if err != nil {
		logger.Logger.Errorw("failed to get disk partitions", "error", err)
		return
	}

	type mountInfo struct {
		MountPoint string
		Device     string
		FSType     string
	}

	var targets []mountInfo
	for _, p := range partitions {
		if ins.isIgnoredFSType(p.Fstype) {
			continue
		}

		if ins.mountFilter != nil && !ins.mountFilter.Match(p.Mountpoint) {
			continue
		}

		targets = append(targets, mountInfo{
			MountPoint: p.Mountpoint,
			Device:     p.Device,
			FSType:     p.Fstype,
		})
	}

	if len(targets) == 0 {
		return
	}

	gatherTimeout := time.Duration(ins.GatherTimeout)

	var wg sync.WaitGroup
	se := semaphore.NewSemaphore(ins.Concurrency)

	for _, t := range targets {
		mp := t.MountPoint

		if startTime, ok := ins.inFlight.Load(mp); ok {
			elapsed := time.Now().Unix() - startTime.(int64)
			if elapsed > int64(gatherTimeout.Seconds()) {
				q.PushFront(ins.buildHungEvent(mp, elapsed))
			}
			continue
		}

		if _, wasHung := ins.prevHung.Load(mp); wasHung {
			q.PushFront(ins.buildHungRecoveryEvent(mp))
			ins.prevHung.Delete(mp)
		}

		wg.Add(1)
		go func(mi mountInfo) {
			se.Acquire()
			defer se.Release()
			defer wg.Done()
			ins.inFlight.Store(mi.MountPoint, time.Now().Unix())
			defer ins.inFlight.Delete(mi.MountPoint)
			ins.gatherMountPoint(q, mi.MountPoint, mi.Device, mi.FSType)
		}(t)
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(gatherTimeout):
		ins.inFlight.Range(func(key, value any) bool {
			ins.prevHung.Store(key, true)
			return true
		})
	}
}

func (ins *Instance) gatherMountPoint(q *safe.Queue[*types.Event], mountPoint, device, fsType string) {
	usage, err := disk.Usage(mountPoint)
	if err != nil {
		logger.Logger.Errorw("failed to get disk usage", "mount_point", mountPoint, "error", err)
		q.PushFront(types.BuildEvent(map[string]string{
			"check":       ins.Check,
			"check_type":  "usage",
			"mount_point": mountPoint,
		}).SetTitleRule("[check]").SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf(`[MD]
- **mount_point**: %s
- **device**: %s
- **fs_type**: %s

**message**: failed to get disk usage: %v
`, mountPoint, device, fsType, err)))
		return
	}

	ins.checkUsage(q, mountPoint, device, fsType, usage)
	ins.checkInodes(q, mountPoint, device, fsType, usage)

	if ins.CheckWritable {
		ins.checkWritable(q, mountPoint, device, fsType)
	}
}

func (ins *Instance) checkUsage(q *safe.Queue[*types.Event], mountPoint, device, fsType string, usage *disk.UsageStat) {
	if ins.WarnIfUsedPercentGe == 0 && ins.CriticalIfUsedPercentGe == 0 {
		return
	}

	labels := map[string]string{
		"check":       ins.Check,
		"check_type":  "usage",
		"mount_point": mountPoint,
	}

	event := types.BuildEvent(labels).SetTitleRule("[check]").
		SetDescription(ins.buildUsageDesc(mountPoint, device, fsType, usage, "everything is ok"))

	if ins.CriticalIfUsedPercentGe > 0 && usage.UsedPercent >= ins.CriticalIfUsedPercentGe {
		msg := fmt.Sprintf("disk usage %.1f%% >= critical threshold %.1f%%", usage.UsedPercent, ins.CriticalIfUsedPercentGe)
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription(ins.buildUsageDesc(mountPoint, device, fsType, usage, msg)))
		return
	}

	if ins.WarnIfUsedPercentGe > 0 && usage.UsedPercent >= ins.WarnIfUsedPercentGe {
		msg := fmt.Sprintf("disk usage %.1f%% >= warning threshold %.1f%%", usage.UsedPercent, ins.WarnIfUsedPercentGe)
		q.PushFront(event.SetEventStatus(types.EventStatusWarning).
			SetDescription(ins.buildUsageDesc(mountPoint, device, fsType, usage, msg)))
		return
	}

	q.PushFront(event)
}

func (ins *Instance) checkInodes(q *safe.Queue[*types.Event], mountPoint, device, fsType string, usage *disk.UsageStat) {
	if usage.InodesTotal == 0 {
		return
	}

	if ins.WarnIfInodesUsedPercentGe == 0 && ins.CriticalIfInodesUsedPercentGe == 0 {
		return
	}

	labels := map[string]string{
		"check":       ins.Check,
		"check_type":  "inode",
		"mount_point": mountPoint,
	}

	event := types.BuildEvent(labels).SetTitleRule("[check]").
		SetDescription(ins.buildInodeDesc(mountPoint, device, fsType, usage, "everything is ok"))

	if ins.CriticalIfInodesUsedPercentGe > 0 && usage.InodesUsedPercent >= ins.CriticalIfInodesUsedPercentGe {
		msg := fmt.Sprintf("inode usage %.1f%% >= critical threshold %.1f%%", usage.InodesUsedPercent, ins.CriticalIfInodesUsedPercentGe)
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription(ins.buildInodeDesc(mountPoint, device, fsType, usage, msg)))
		return
	}

	if ins.WarnIfInodesUsedPercentGe > 0 && usage.InodesUsedPercent >= ins.WarnIfInodesUsedPercentGe {
		msg := fmt.Sprintf("inode usage %.1f%% >= warning threshold %.1f%%", usage.InodesUsedPercent, ins.WarnIfInodesUsedPercentGe)
		q.PushFront(event.SetEventStatus(types.EventStatusWarning).
			SetDescription(ins.buildInodeDesc(mountPoint, device, fsType, usage, msg)))
		return
	}

	q.PushFront(event)
}

func (ins *Instance) checkWritable(q *safe.Queue[*types.Event], mountPoint, device, fsType string) {
	labels := map[string]string{
		"check":       ins.Check,
		"check_type":  "writable",
		"mount_point": mountPoint,
	}

	event := types.BuildEvent(labels).SetTitleRule("[check]")

	testFile := filepath.Join(mountPoint, ins.WritableTestFile)
	testContent := fmt.Sprintf("catpaw-disk-check-%d", time.Now().UnixNano())

	err := os.WriteFile(testFile, []byte(testContent), 0644)
	if err != nil {
		msg := ins.classifyWriteError(err)
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription(ins.buildWritableDesc(mountPoint, device, fsType, msg)))
		return
	}

	readBack, err := os.ReadFile(testFile)
	if err != nil {
		_ = os.Remove(testFile)
		msg := fmt.Sprintf("write succeeded but read-back failed: %v", err)
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription(ins.buildWritableDesc(mountPoint, device, fsType, msg)))
		return
	}

	_ = os.Remove(testFile)

	if string(readBack) != testContent {
		msg := "write succeeded but read-back content mismatch (possible data corruption)"
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription(ins.buildWritableDesc(mountPoint, device, fsType, msg)))
		return
	}

	q.PushFront(event.SetDescription(ins.buildWritableDesc(mountPoint, device, fsType, "read-write test passed")))
}

func (ins *Instance) classifyWriteError(err error) string {
	errMsg := err.Error()
	if os.IsPermission(err) || strings.Contains(errMsg, "permission denied") {
		return fmt.Sprintf("permission denied (catpaw process lacks write access): %v", err)
	}
	if strings.Contains(errMsg, "read-only file system") {
		return fmt.Sprintf("read-only file system: %v", err)
	}
	if strings.Contains(errMsg, "no space left") {
		return fmt.Sprintf("no space left on device: %v", err)
	}
	return fmt.Sprintf("write failed (possible disk fault): %v", err)
}

func (ins *Instance) isIgnoredFSType(fsType string) bool {
	for _, ignored := range ins.IgnoreFSTypes {
		if strings.EqualFold(ignored, fsType) {
			return true
		}
	}
	return false
}

func (ins *Instance) buildHungEvent(mountPoint string, elapsedSec int64) *types.Event {
	labels := map[string]string{
		"check":       ins.Check,
		"check_type":  "hung",
		"mount_point": mountPoint,
	}
	desc := fmt.Sprintf(`[MD]
- **mount_point**: %s
- **status**: disk check hung for %d seconds (possible NFS/network disk issue)
`, mountPoint, elapsedSec)

	return types.BuildEvent(labels).SetTitleRule("[check]").
		SetEventStatus(types.EventStatusCritical).SetDescription(desc)
}

func (ins *Instance) buildHungRecoveryEvent(mountPoint string) *types.Event {
	labels := map[string]string{
		"check":       ins.Check,
		"check_type":  "hung",
		"mount_point": mountPoint,
	}
	desc := fmt.Sprintf(`[MD]
- **mount_point**: %s
- **status**: disk check recovered from hung state
`, mountPoint)

	return types.BuildEvent(labels).SetTitleRule("[check]").SetDescription(desc)
}

func humanBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

func (ins *Instance) buildUsageDesc(mountPoint, device, fsType string, usage *disk.UsageStat, message string) string {
	return fmt.Sprintf(`[MD]
- **mount_point**: %s
- **device**: %s
- **fs_type**: %s
- **total**: %s
- **used**: %s
- **available**: %s
- **used_percent**: %.1f%%

**message**: %s
`, mountPoint, device, fsType,
		humanBytes(usage.Total), humanBytes(usage.Used), humanBytes(usage.Free),
		usage.UsedPercent, message)
}

func (ins *Instance) buildInodeDesc(mountPoint, device, fsType string, usage *disk.UsageStat, message string) string {
	return fmt.Sprintf(`[MD]
- **mount_point**: %s
- **device**: %s
- **fs_type**: %s
- **inodes_total**: %d
- **inodes_used**: %d
- **inodes_free**: %d
- **inodes_used_percent**: %.1f%%

**message**: %s
`, mountPoint, device, fsType,
		usage.InodesTotal, usage.InodesUsed, usage.InodesFree,
		usage.InodesUsedPercent, message)
}

func (ins *Instance) buildWritableDesc(mountPoint, device, fsType, message string) string {
	return fmt.Sprintf(`[MD]
- **mount_point**: %s
- **device**: %s
- **fs_type**: %s

**message**: %s
`, mountPoint, device, fsType, message)
}
