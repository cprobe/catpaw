package nvidia

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/cprobe/catpaw/diagnose"
	"github.com/cprobe/catpaw/pkg/cmdx"
)

func registerGPULink(registry *diagnose.ToolRegistry) {
	registry.RegisterCategory(
		"nvidia_gpu_link", "nvidia_gpu_link",
		"NVIDIA GPU PCIe link diagnostics: check link width and speed against hardware capability",
		diagnose.ToolScopeLocal,
	)
	registry.Register("nvidia_gpu_link", diagnose.DiagnoseTool{
		Name: "nvidia_gpu_link_status",
		Description: "Check PCIe link width and speed for all NVIDIA GPUs. " +
			"Compares actual link state (LnkSta) against hardware capability (LnkCap) for each GPU. " +
			"Reports mismatches in width (e.g. x16 vs x8) or speed (e.g. 16GT/s vs 8GT/s) per PCI bus ID.",
		Scope:   diagnose.ToolScopeLocal,
		Execute: execGPULinkStatus,
	})
}

type gpuLinkInfo struct {
	busID    string
	name     string
	speedCap string
	speedSta string
	widthCap string
	widthSta string
	lspciErr error
}

func execGPULinkStatus(ctx context.Context, _ map[string]string) (string, error) {
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("gpu_pcie_link_status requires linux (current: %s)", runtime.GOOS)
	}

	timeout := ctxTimeout(ctx, defaultTimeout)

	busIDs, err := getGPUBusIDs(timeout)
	if err != nil {
		return "", fmt.Errorf("get GPU bus IDs: %w", err)
	}
	if len(busIDs) == 0 {
		return "No NVIDIA GPUs found (nvidia-smi returned empty list).\n", nil
	}

	infos := make([]gpuLinkInfo, 0, len(busIDs))
	for _, entry := range busIDs {
		info := gpuLinkInfo{busID: entry.busID, name: entry.name}
		lspciOut, lspciErr := runLspci(entry.busID, timeout)
		if lspciErr != nil {
			info.lspciErr = lspciErr
		} else {
			info.speedCap, info.widthCap, info.speedSta, info.widthSta = parseLinkInfo(lspciOut)
		}
		infos = append(infos, info)
	}

	return formatLinkReport(infos), nil
}

type gpuEntry struct {
	busID string
	name  string
}

func getGPUBusIDs(timeout time.Duration) ([]gpuEntry, error) {
	bin, err := exec.LookPath("nvidia-smi")
	if err != nil {
		return nil, fmt.Errorf("nvidia-smi not found: %w", err)
	}

	cmd := exec.Command(bin, "--query-gpu=pci.bus_id,name", "--format=csv,noheader,nounits")
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr, timedOut := cmdx.RunTimeout(cmd, timeout)
	if timedOut {
		return nil, fmt.Errorf("nvidia-smi timed out after %s", timeout)
	}
	if runErr != nil && stdout.Len() == 0 {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return nil, fmt.Errorf("nvidia-smi: %w", runErr)
		}
		return nil, fmt.Errorf("nvidia-smi: %s", msg)
	}

	var entries []gpuEntry
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ",", 2)
		rawBusID := strings.TrimSpace(parts[0])
		name := ""
		if len(parts) == 2 {
			name = strings.TrimSpace(parts[1])
		}
		entries = append(entries, gpuEntry{busID: stripDomain(rawBusID), name: name})
	}
	return entries, nil
}

// stripDomain removes the leading domain prefix (e.g. "00000000:") from nvidia-smi bus IDs.
func stripDomain(s string) string {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) == 2 && len(parts[0]) == 8 {
		return parts[1]
	}
	return s
}

func runLspci(busID string, timeout time.Duration) (string, error) {
	bin, err := exec.LookPath("lspci")
	if err != nil {
		return "", fmt.Errorf("lspci not found: %w", err)
	}

	cmd := exec.Command(bin, "-vvs", busID)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr, timedOut := cmdx.RunTimeout(cmd, timeout)
	if timedOut {
		return "", fmt.Errorf("lspci timed out after %s", timeout)
	}
	if runErr != nil && stdout.Len() == 0 {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return "", fmt.Errorf("lspci -vvs %s: %w", busID, runErr)
		}
		return "", fmt.Errorf("lspci -vvs %s: %s", busID, msg)
	}
	return stdout.String(), nil
}

// parseLinkInfo parses lspci -vvs output and extracts PCIe link capability and status.
//
// Example lspci lines:
//
//	LnkCap:  Port #0, Speed 16GT/s, Width x16, ASPM L0s L1, ...
//	LnkSta:  Speed 16GT/s (ok), Width x16 (ok)
func parseLinkInfo(output string) (speedCap, widthCap, speedSta, widthSta string) {
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "LnkCap:") {
			parts := strings.Split(trimmed, ",")
			if len(parts) >= 2 {
				if f := strings.Fields(parts[1]); len(f) >= 2 {
					speedCap = strings.ToLower(strings.Trim(f[1], ","))
				}
			}
			if len(parts) >= 3 {
				if f := strings.Fields(parts[2]); len(f) >= 2 {
					widthCap = strings.Trim(f[1], ",")
				}
			}
		}

		if strings.HasPrefix(trimmed, "LnkSta:") {
			parts := strings.Split(trimmed, ",")
			if len(parts) >= 1 {
				if f := strings.Fields(parts[0]); len(f) >= 3 {
					speedSta = strings.ToLower(strings.Trim(f[2], ","))
				}
			}
			if len(parts) >= 2 {
				if f := strings.Fields(parts[1]); len(f) >= 2 {
					widthSta = strings.Trim(f[1], ",")
				}
			}
		}
	}
	return
}

func formatLinkReport(infos []gpuLinkInfo) string {
	var b strings.Builder
	fmt.Fprintf(&b, "NVIDIA GPU PCIe Link Status (%d GPU(s))\n\n", len(infos))
	fmt.Fprintf(&b, "%-16s  %-30s  %-12s  %-12s  %-10s  %-10s  %s\n",
		"Bus ID", "Name", "SpeedCap", "SpeedSta", "WidthCap", "WidthSta", "Result")
	fmt.Fprintf(&b, "%s\n", strings.Repeat("-", 110))

	okCount, failCount := 0, 0
	for _, info := range infos {
		if info.lspciErr != nil {
			fmt.Fprintf(&b, "%-16s  %-30s  lspci error: %v\n", info.busID, truncate(info.name, 30), info.lspciErr)
			failCount++
			continue
		}

		widthOK := info.widthSta != "" && info.widthSta == info.widthCap
		speedOK := info.speedSta != "" && info.speedSta == info.speedCap
		result := "OK"
		if !widthOK || !speedOK {
			result = "MISMATCH"
			failCount++
		} else {
			okCount++
		}

		fmt.Fprintf(&b, "%-16s  %-30s  %-12s  %-12s  %-10s  %-10s  %s\n",
			info.busID, truncate(info.name, 30),
			info.speedCap, info.speedSta,
			info.widthCap, info.widthSta,
			result)
	}

	b.WriteByte('\n')
	fmt.Fprintf(&b, "Summary: %d OK, %d MISMATCH\n", okCount, failCount)
	if failCount > 0 {
		b.WriteString("\nNote: PCIe link degradation may indicate a hardware fault, loose connector,\n")
		b.WriteString("or BIOS/firmware misconfiguration. Check server event logs and reseat the GPU if needed.\n")
	}
	return b.String()
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}
