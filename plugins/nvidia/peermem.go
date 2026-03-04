package nvidia

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/cprobe/catpaw/diagnose"
	"github.com/cprobe/catpaw/pkg/cmdx"
)

func registerPeermem(registry *diagnose.ToolRegistry) {
	registry.RegisterCategory(
		"nvidia_gdr", "nvidia_gdr",
		"NVIDIA GPUDirect RDMA (GDR) readiness diagnostics: ACS status and nvidia_peermem module check",
		diagnose.ToolScopeLocal,
	)
	registry.Register("nvidia_gdr", diagnose.DiagnoseTool{
		Name: "nvidia_gdr_status",
		Description: "Check NVIDIA GPUDirect RDMA (GDR) readiness on this host. " +
			"Verifies: (1) ACS (Access Control Services) is fully disabled on all PCIe devices — required for GDR, " +
			"(2) nvidia_peermem kernel module is loaded, " +
			"(3) nvidia_peermem is persisted in /etc/modules-load.d/ so it survives reboots.",
		Scope:   diagnose.ToolScopeLocal,
		Execute: execGDRStatus,
	})
}

func execGDRStatus(ctx context.Context, _ map[string]string) (string, error) {
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("nvidia_gdr_status requires linux (current: %s)", runtime.GOOS)
	}

	timeout := ctxTimeout(ctx, defaultTimeout)
	var b strings.Builder

	b.WriteString("=== ACS (Access Control Services) ===\n")
	checkACS(&b, timeout)

	b.WriteString("\n=== nvidia_peermem module ===\n")
	loaded, err := isModuleLoaded()
	if err != nil {
		fmt.Fprintf(&b, "error reading /proc/modules: %v\n", err)
		return b.String(), nil
	}

	if !loaded {
		b.WriteString("Status:      NOT LOADED\n")
		b.WriteString("Fix:\n")
		b.WriteString("  modprobe nvidia_peermem\n")
		b.WriteString("  echo 'nvidia_peermem' >> /etc/modules-load.d/modules.conf\n")
		return b.String(), nil
	}

	b.WriteString("Status:      loaded\n")

	persisted, persistErr := isPeermemPersisted()
	if persistErr != nil {
		fmt.Fprintf(&b, "Persistence: error (%v)\n", persistErr)
	} else if !persisted {
		b.WriteString("Persistence: WARNING - not found in /etc/modules-load.d/, will not survive reboot\n")
		b.WriteString("Fix: echo 'nvidia_peermem' >> /etc/modules-load.d/modules.conf\n")
	} else {
		b.WriteString("Persistence: OK - persisted in /etc/modules-load.d/\n")
	}

	return b.String(), nil
}

// checkACS parses `lspci -vvv` output and checks whether ACS Source Validation
// is disabled (SrcValid-) on all PCIe devices. ACS must be fully disabled for
// GPUDirect RDMA to work correctly.
func checkACS(b *strings.Builder, _ interface{}) {
	bin, err := exec.LookPath("lspci")
	if err != nil {
		fmt.Fprintf(b, "ACS: lspci not found, skipping check\n")
		return
	}

	cmd := exec.Command(bin, "-vvv")
	var stdout strings.Builder
	cmd.Stdout = &stdout

	runErr, timedOut := cmdx.RunTimeout(cmd, defaultTimeout)
	if timedOut {
		fmt.Fprintf(b, "ACS: lspci timed out\n")
		return
	}
	if runErr != nil && stdout.Len() == 0 {
		fmt.Fprintf(b, "ACS: lspci error: %v\n", runErr)
		return
	}

	total, disabled := countACSEntries(stdout.String())

	if total == 0 {
		b.WriteString("ACS: no ACSCtl entries found in lspci output\n")
		b.WriteString("     Unable to verify ACS status; check BIOS/firmware settings manually.\n")
		return
	}

	enabled := total - disabled
	fmt.Fprintf(b, "ACS devices:    %d total, %d disabled (SrcValid-), %d enabled (SrcValid+)\n",
		total, disabled, enabled)
	if enabled > 0 {
		fmt.Fprintf(b, "Status:         FAIL - ACS not fully disabled (%d device(s) still have SrcValid+)\n", enabled)
		b.WriteString("Fix:            Disable ACS in BIOS or via setpci for each affected device.\n")
	} else {
		b.WriteString("Status:         OK - ACS fully disabled on all devices\n")
	}
}

func isModuleLoaded() (bool, error) {
	f, err := os.Open("/proc/modules")
	if err != nil {
		return false, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) > 0 && fields[0] == "nvidia_peermem" {
			return true, nil
		}
	}
	return false, scanner.Err()
}

func isPeermemPersisted() (bool, error) {
	return isPeermemPersistedIn("/etc/modules-load.d")
}

// isPeermemPersistedIn checks all files under dir for a non-comment line
// containing "nvidia_peermem".
func isPeermemPersistedIn(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, fmt.Errorf("readdir %s: %w", dir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, readErr := os.ReadFile(dir + "/" + entry.Name())
		if readErr != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "#") || line == "" {
				continue
			}
			if strings.Contains(line, "nvidia_peermem") {
				return true, nil
			}
		}
	}
	return false, nil
}

// countACSEntries parses lspci -vvv output and counts ACSCtl entries.
// Returns total count and how many have SrcValid- (ACS source validation disabled).
func countACSEntries(output string) (total, disabled int) {
	for _, line := range strings.Split(output, "\n") {
		if !strings.Contains(strings.ToLower(line), "acsctl") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		total++
		if strings.HasPrefix(fields[1], "SrcValid-") {
			disabled++
		}
	}
	return
}
