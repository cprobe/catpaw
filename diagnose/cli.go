package diagnose

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// CLIList prints a summary table of recent diagnosis records from stateDir.
func CLIList(stateDir string, limit int) error {
	dir := filepath.Join(stateDir, "diagnoses")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("No diagnosis records found.")
			return nil
		}
		return fmt.Errorf("read diagnoses dir: %w", err)
	}

	type fileEntry struct {
		name    string
		modTime time.Time
	}

	var files []fileEntry
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, fileEntry{name: e.Name(), modTime: info.ModTime()})
	}

	if len(files) == 0 {
		fmt.Println("No diagnosis records found.")
		return nil
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].modTime.After(files[j].modTime)
	})

	if limit > 0 && limit < len(files) {
		files = files[:limit]
	}

	fmt.Printf("%-50s  %-8s  %-8s  %-6s  %-20s  %s\n",
		"ID", "Status", "Plugin", "Checks", "Time", "Duration")
	fmt.Println(strings.Repeat("-", 120))

	for _, f := range files {
		record, err := loadRecord(filepath.Join(dir, f.name))
		if err != nil {
			fmt.Printf("%-50s  (error: %s)\n", f.name, err)
			continue
		}
		fmt.Printf("%-50s  %-8s  %-8s  %-6d  %-20s  %dms\n",
			record.ID,
			record.Status,
			record.Alert.Plugin,
			len(record.Alert.Checks),
			record.CreatedAt.Format(time.DateTime),
			record.DurationMs)
	}

	fmt.Printf("\nTotal: %d records\n", len(files))
	return nil
}

// CLIShow prints the full details of a specific diagnosis record.
func CLIShow(stateDir string, id string) error {
	dir := filepath.Join(stateDir, "diagnoses")

	path := filepath.Join(dir, id+".json")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		path = filepath.Join(dir, id)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return tryFuzzyMatch(dir, id)
		}
	}

	record, err := loadRecord(path)
	if err != nil {
		return err
	}

	printRecordDetail(record)
	return nil
}

func loadRecord(path string) (*DiagnoseRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}
	var record DiagnoseRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, fmt.Errorf("parse json: %w", err)
	}
	return &record, nil
}

func tryFuzzyMatch(dir, pattern string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("record %q not found", pattern)
	}

	var matches []string
	for _, e := range entries {
		if strings.Contains(e.Name(), pattern) {
			matches = append(matches, strings.TrimSuffix(e.Name(), ".json"))
		}
	}

	if len(matches) == 0 {
		return fmt.Errorf("record %q not found", pattern)
	}

	fmt.Printf("Record %q not found. Did you mean:\n", pattern)
	for _, m := range matches {
		fmt.Printf("  %s\n", m)
	}
	return fmt.Errorf("record %q not found, see suggestions above", pattern)
}

func printRecordDetail(r *DiagnoseRecord) {
	fmt.Println("=== Diagnosis Record ===")
	fmt.Printf("ID:       %s\n", r.ID)
	fmt.Printf("Status:   %s\n", r.Status)
	if r.Error != "" {
		fmt.Printf("Error:    %s\n", r.Error)
	}
	fmt.Printf("Created:  %s\n", r.CreatedAt.Format(time.DateTime))
	fmt.Printf("Duration: %dms\n", r.DurationMs)
	fmt.Println()

	fmt.Println("--- Alert Context ---")
	fmt.Printf("Plugin: %s\n", r.Alert.Plugin)
	fmt.Printf("Target: %s\n", r.Alert.Target)
	for i, c := range r.Alert.Checks {
		fmt.Printf("Check[%d]: %s status=%s value=%s", i, c.Check, c.Status, c.CurrentValue)
		if c.WarningThreshold != "" {
			fmt.Printf(" warn=%s", c.WarningThreshold)
		}
		if c.CriticalThreshold != "" {
			fmt.Printf(" crit=%s", c.CriticalThreshold)
		}
		fmt.Println()
	}
	fmt.Println()

	fmt.Println("--- AI Model ---")
	fmt.Printf("Model:   %s\n", r.AI.Model)
	fmt.Printf("Rounds:  %d\n", r.AI.TotalRounds)
	fmt.Printf("Tokens:  %d input + %d output = %d total\n",
		r.AI.InputTokens, r.AI.OutputTokens, r.AI.InputTokens+r.AI.OutputTokens)
	fmt.Println()

	if len(r.Rounds) > 0 {
		fmt.Println("--- Tool Calls ---")
		for _, round := range r.Rounds {
			for _, tc := range round.ToolCalls {
				fmt.Printf("[Round %d] %s(%v) → %dms\n",
					round.Round, tc.Name, formatArgs(tc.Args), tc.DurationMs)
				result := tc.Result
				if len(result) > 500 {
					result = TruncateUTF8(result, 497) + "..."
				}
				fmt.Printf("  Result: %s\n", result)
			}
			if round.AIReasoning != "" {
				reasoning := round.AIReasoning
				if len(reasoning) > 300 {
					reasoning = TruncateUTF8(reasoning, 297) + "..."
				}
				fmt.Printf("[Round %d] AI: %s\n", round.Round, reasoning)
			}
		}
		fmt.Println()
	}

	if r.Report != "" {
		fmt.Println("--- Final Report ---")
		fmt.Println(r.Report)
	}
}

func formatArgs(args map[string]string) string {
	if len(args) == 0 {
		return ""
	}
	parts := make([]string, 0, len(args))
	for k, v := range args {
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, ", ")
}
