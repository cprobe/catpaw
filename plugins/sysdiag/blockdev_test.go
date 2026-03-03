package sysdiag

import (
	"strings"
	"testing"
)

func TestFormatBlockDevs(t *testing.T) {
	devices := []blockDev{
		{
			Name: "sda", Type: "disk", Size: "100G",
			Children: []blockDev{
				{Name: "sda1", Type: "part", Size: "512M", FsType: "vfat", MountPoint: "/boot/efi"},
				{Name: "sda2", Type: "part", Size: "99.5G", FsType: "ext4", MountPoint: "/"},
			},
		},
		{
			Name: "sdb", Type: "disk", Size: "500G",
			Children: []blockDev{
				{Name: "sdb1", Type: "part", Size: "500G",
					Children: []blockDev{
						{Name: "vg_data-lv_data", Type: "lvm", Size: "500G", FsType: "xfs", MountPoint: "/data"},
					},
				},
			},
		},
	}

	out := formatBlockDevs(devices)
	if !strings.Contains(out, "2 disks") {
		t.Fatal("expected '2 disks' in header")
	}
	if !strings.Contains(out, "sda") {
		t.Fatal("expected sda in output")
	}
	if !strings.Contains(out, "sda1") {
		t.Fatal("expected sda1 partition")
	}
	if !strings.Contains(out, "lvm") {
		t.Fatal("expected lvm type")
	}
	if !strings.Contains(out, "/data") {
		t.Fatal("expected /data mountpoint")
	}
}

func TestFormatBlockDevsEmpty(t *testing.T) {
	out := formatBlockDevs(nil)
	if !strings.Contains(out, "No block") {
		t.Fatal("expected 'No block' message")
	}
}

func TestCountDevices(t *testing.T) {
	devices := []blockDev{
		{Name: "sda", Children: []blockDev{
			{Name: "sda1"},
			{Name: "sda2", Children: []blockDev{
				{Name: "lv1"},
			}},
		}},
	}
	if c := countDevices(devices); c != 4 {
		t.Errorf("countDevices=%d, want 4", c)
	}
}

func TestConvertLsblkEntry(t *testing.T) {
	entry := lsblkEntry{
		Name:       "sda",
		Type:       "disk",
		Size:       "100G",
		FsType:     nil,
		MountPoint: nil,
		RO:         false,
		Children: []lsblkEntry{
			{Name: "sda1", Type: "part", Size: "100G", FsType: "ext4", MountPoint: "/"},
		},
	}

	dev := convertLsblkEntry(entry)
	if dev.Name != "sda" {
		t.Errorf("name=%q, want sda", dev.Name)
	}
	if dev.FsType != "" {
		t.Errorf("fstype=%q, want empty (was null)", dev.FsType)
	}
	if len(dev.Children) != 1 {
		t.Fatalf("children=%d, want 1", len(dev.Children))
	}
	if dev.Children[0].FsType != "ext4" {
		t.Errorf("child fstype=%q, want ext4", dev.Children[0].FsType)
	}
}

func TestWriteBlockDev(t *testing.T) {
	var b strings.Builder
	dev := blockDev{
		Name: "sda", Type: "disk", Size: "100G", RO: true, MountPoint: "/mnt",
	}
	writeBlockDev(&b, dev, 0)
	out := b.String()
	if !strings.Contains(out, "sda") {
		t.Fatal("expected device name")
	}
	if !strings.Contains(out, "RO") {
		t.Fatal("expected RO flag")
	}
	if !strings.Contains(out, "[!]") {
		t.Fatal("expected [!] for RO with mountpoint")
	}
}
