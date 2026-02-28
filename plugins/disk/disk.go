package disk

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cprobe/catpaw/config"
	"github.com/cprobe/catpaw/logger"
	"github.com/cprobe/catpaw/pkg/conv"
	"github.com/cprobe/catpaw/pkg/filter"
	"github.com/cprobe/catpaw/pkg/safe"
	"github.com/cprobe/catpaw/plugins"
	"github.com/cprobe/catpaw/types"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/toolkits/pkg/concurrent/semaphore"
)

const pluginName = "disk"

type SpaceUsageCheck struct {
	WarnGe     float64 `toml:"warn_ge"`
	CriticalGe float64 `toml:"critical_ge"`
	TitleRule  string  `toml:"title_rule"`
}

type InodeUsageCheck struct {
	WarnGe     float64 `toml:"warn_ge"`
	CriticalGe float64 `toml:"critical_ge"`
	TitleRule  string  `toml:"title_rule"`
}

type WritableCheck struct {
	Severity  string `toml:"severity"`
	TestFile  string `toml:"test_file"`
	TitleRule string `toml:"title_rule"`
}

type Instance struct {
	config.InternalConfig

	MountPoints       []string `toml:"mount_points"`
	IgnoreMountPoints []string `toml:"ignore_mount_points"`
	IgnoreFSTypes     []string `toml:"ignore_fs_types"`

	SpaceUsage SpaceUsageCheck `toml:"space_usage"`
	InodeUsage InodeUsageCheck `toml:"inode_usage"`
	Writable   WritableCheck   `toml:"writable"`

	Concurrency   int             `toml:"concurrency"`
	GatherTimeout config.Duration `toml:"gather_timeout"`

	mountFilter filter.Filter
	inFlight    sync.Map
	prevHung    sync.Map
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

	if ins.Writable.Severity != "" && ins.Writable.TestFile == "" {
		ins.Writable.TestFile = ".catpaw_disk_check"
	}

	if ins.SpaceUsage.WarnGe > 0 && ins.SpaceUsage.CriticalGe > 0 &&
		ins.SpaceUsage.WarnGe >= ins.SpaceUsage.CriticalGe {
		return fmt.Errorf("space_usage.warn_ge(%.1f) must be less than space_usage.critical_ge(%.1f)",
			ins.SpaceUsage.WarnGe, ins.SpaceUsage.CriticalGe)
	}

	if ins.InodeUsage.WarnGe > 0 && ins.InodeUsage.CriticalGe > 0 &&
		ins.InodeUsage.WarnGe >= ins.InodeUsage.CriticalGe {
		return fmt.Errorf("inode_usage.warn_ge(%.1f) must be less than inode_usage.critical_ge(%.1f)",
			ins.InodeUsage.WarnGe, ins.InodeUsage.CriticalGe)
	}

	f, err := filter.NewIncludeExcludeFilter(ins.MountPoints, ins.IgnoreMountPoints)
	if err != nil {
		return fmt.Errorf("failed to compile mount point filter: %v", err)
	}
	ins.mountFilter = f

	return nil
}

func (ins *Instance) Gather(q *safe.Queue[*types.Event]) {
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

		tr := ins.SpaceUsage.TitleRule
		if tr == "" {
			tr = "[check] [target]"
		}

		q.PushFront(types.BuildEvent(map[string]string{
			"check":                        "disk::space_usage",
			"target":                       mountPoint,
			types.AttrPrefix + "device":    device,
			types.AttrPrefix + "fs_type":   fsType,
		}).SetTitleRule(tr).SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("failed to get disk usage: %v", err)))
		return
	}

	ins.checkUsage(q, mountPoint, device, fsType, usage)
	ins.checkInodes(q, mountPoint, device, fsType, usage)

	if ins.Writable.Severity != "" {
		ins.checkWritable(q, mountPoint, device, fsType)
	}
}

func (ins *Instance) checkUsage(q *safe.Queue[*types.Event], mountPoint, device, fsType string, usage *disk.UsageStat) {
	if ins.SpaceUsage.WarnGe == 0 && ins.SpaceUsage.CriticalGe == 0 {
		return
	}

	tr := ins.SpaceUsage.TitleRule
	if tr == "" {
		tr = "[check] [target]"
	}

	event := types.BuildEvent(map[string]string{
		"check":                              "disk::space_usage",
		"target":                             mountPoint,
		types.AttrPrefix + "device":          device,
		types.AttrPrefix + "fs_type":         fsType,
		types.AttrPrefix + "total":           conv.HumanBytes(usage.Total),
		types.AttrPrefix + "used":            conv.HumanBytes(usage.Used),
		types.AttrPrefix + "available":       conv.HumanBytes(usage.Free),
		types.AttrPrefix + "used_percent":    fmt.Sprintf("%.1f%%", usage.UsedPercent),
	}).SetTitleRule(tr).SetDescription("everything is ok")

	if ins.SpaceUsage.CriticalGe > 0 && usage.UsedPercent >= ins.SpaceUsage.CriticalGe {
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("disk usage %.1f%% >= critical threshold %.1f%%", usage.UsedPercent, ins.SpaceUsage.CriticalGe)))
		return
	}

	if ins.SpaceUsage.WarnGe > 0 && usage.UsedPercent >= ins.SpaceUsage.WarnGe {
		q.PushFront(event.SetEventStatus(types.EventStatusWarning).
			SetDescription(fmt.Sprintf("disk usage %.1f%% >= warning threshold %.1f%%", usage.UsedPercent, ins.SpaceUsage.WarnGe)))
		return
	}

	q.PushFront(event)
}

func (ins *Instance) checkInodes(q *safe.Queue[*types.Event], mountPoint, device, fsType string, usage *disk.UsageStat) {
	if usage.InodesTotal == 0 {
		return
	}

	if ins.InodeUsage.WarnGe == 0 && ins.InodeUsage.CriticalGe == 0 {
		return
	}

	tr := ins.InodeUsage.TitleRule
	if tr == "" {
		tr = "[check] [target]"
	}

	event := types.BuildEvent(map[string]string{
		"check":                                    "disk::inode_usage",
		"target":                                   mountPoint,
		types.AttrPrefix + "device":                device,
		types.AttrPrefix + "fs_type":               fsType,
		types.AttrPrefix + "inodes_total":          fmt.Sprintf("%d", usage.InodesTotal),
		types.AttrPrefix + "inodes_used":           fmt.Sprintf("%d", usage.InodesUsed),
		types.AttrPrefix + "inodes_free":           fmt.Sprintf("%d", usage.InodesFree),
		types.AttrPrefix + "inodes_used_percent":   fmt.Sprintf("%.1f%%", usage.InodesUsedPercent),
	}).SetTitleRule(tr).SetDescription("everything is ok")

	if ins.InodeUsage.CriticalGe > 0 && usage.InodesUsedPercent >= ins.InodeUsage.CriticalGe {
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("inode usage %.1f%% >= critical threshold %.1f%%", usage.InodesUsedPercent, ins.InodeUsage.CriticalGe)))
		return
	}

	if ins.InodeUsage.WarnGe > 0 && usage.InodesUsedPercent >= ins.InodeUsage.WarnGe {
		q.PushFront(event.SetEventStatus(types.EventStatusWarning).
			SetDescription(fmt.Sprintf("inode usage %.1f%% >= warning threshold %.1f%%", usage.InodesUsedPercent, ins.InodeUsage.WarnGe)))
		return
	}

	q.PushFront(event)
}

func (ins *Instance) checkWritable(q *safe.Queue[*types.Event], mountPoint, device, fsType string) {
	tr := ins.Writable.TitleRule
	if tr == "" {
		tr = "[check] [target]"
	}

	event := types.BuildEvent(map[string]string{
		"check":                        "disk::writable",
		"target":                       mountPoint,
		types.AttrPrefix + "device":    device,
		types.AttrPrefix + "fs_type":   fsType,
	}).SetTitleRule(tr)

	testFile := filepath.Join(mountPoint, ins.Writable.TestFile)
	testContent := fmt.Sprintf("catpaw-disk-check-%d", time.Now().UnixNano())

	err := os.WriteFile(testFile, []byte(testContent), 0644)
	if err != nil {
		q.PushFront(event.SetEventStatus(ins.Writable.Severity).
			SetDescription(ins.classifyWriteError(err)))
		return
	}

	readBack, err := os.ReadFile(testFile)
	if err != nil {
		_ = os.Remove(testFile)
		q.PushFront(event.SetEventStatus(ins.Writable.Severity).
			SetDescription(fmt.Sprintf("write succeeded but read-back failed: %v", err)))
		return
	}

	_ = os.Remove(testFile)

	if string(readBack) != testContent {
		q.PushFront(event.SetEventStatus(ins.Writable.Severity).
			SetDescription("write succeeded but read-back content mismatch (possible data corruption)"))
		return
	}

	q.PushFront(event.SetDescription("read-write test passed"))
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
	return types.BuildEvent(map[string]string{
		"check":                                "disk::hung",
		"target":                               mountPoint,
		types.AttrPrefix + "elapsed_seconds":   fmt.Sprintf("%d", elapsedSec),
	}).SetTitleRule("[check]").
		SetEventStatus(types.EventStatusCritical).
		SetDescription(fmt.Sprintf("disk check hung for %d seconds (possible NFS/network disk issue)", elapsedSec))
}

func (ins *Instance) buildHungRecoveryEvent(mountPoint string) *types.Event {
	return types.BuildEvent(map[string]string{
		"check":  "disk::hung",
		"target": mountPoint,
	}).SetTitleRule("[check]").
		SetDescription("disk check recovered from hung state")
}


