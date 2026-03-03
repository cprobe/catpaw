package agent

import (
	"fmt"
	"path"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/cprobe/catpaw/config"
	"github.com/cprobe/catpaw/diagnose"
	"github.com/cprobe/catpaw/plugins"
	"github.com/toolkits/pkg/file"
)

// RunInspect executes a proactive health inspection from the CLI.
// For remote plugins (redis, mysql), it loads config to find connection credentials.
// For local-only plugins (cpu, mem, disk), target can be empty.
func RunInspect(pluginName, target string) error {
	if !config.Config.AI.Enabled {
		return fmt.Errorf("AI diagnose is not enabled. Set [ai] enabled=true in config")
	}

	registry := buildFullRegistry()
	if registry.ToolCount() == 0 {
		return fmt.Errorf("no diagnostic tools registered")
	}

	if pluginName == "system" || pluginName == "local" {
		return runInspectRequest(registry, "system", "localhost", nil)
	}

	creator, ok := plugins.PluginCreators[pluginName]
	if !ok {
		return fmt.Errorf("unknown plugin: %q\nAvailable: %s", pluginName, availablePluginNames())
	}

	var instanceRef any
	isRemote := hasAccessorFactory(registry, pluginName)

	if isRemote {
		if target == "" {
			return fmt.Errorf("remote plugin %q requires a target address (e.g. 10.0.0.1:6379)", pluginName)
		}
		ref, err := resolveInstance(creator, pluginName, target)
		if err != nil {
			return err
		}
		instanceRef = ref
	} else {
		if target == "" {
			target = "localhost"
		}
	}

	return runInspectRequest(registry, pluginName, target, instanceRef)
}

func runInspectRequest(registry *diagnose.ToolRegistry, pluginName, target string, instanceRef any) error {
	engine := diagnose.NewDiagnoseEngine(registry, config.Config.AI)

	req := &diagnose.DiagnoseRequest{
		Mode:        diagnose.ModeInspect,
		Plugin:      pluginName,
		Target:      target,
		InstanceRef: instanceRef,
		Timeout:     2 * time.Minute,
	}

	fmt.Printf("[*] Starting health inspection: plugin=%s target=%s\n", pluginName, target)
	fmt.Printf("   AI model: %s, max rounds: %d\n", config.Config.AI.Model, config.Config.AI.MaxRounds)
	fmt.Println()

	engine.RunDiagnose(req)

	record := req.Session.Record
	if record.Status == "failed" {
		return fmt.Errorf("inspection failed: %s", record.Error)
	}

	fmt.Println(record.Report)
	fmt.Println()
	fmt.Printf("--- Inspection completed in %dms, %d rounds, %d tokens ---\n",
		record.DurationMs, record.AI.TotalRounds,
		record.AI.InputTokens+record.AI.OutputTokens)
	fmt.Printf("Record saved: %s\n", record.FilePath())

	return nil
}

func buildFullRegistry() *diagnose.ToolRegistry {
	registry := diagnose.NewToolRegistry()
	for _, c := range plugins.PluginCreators {
		plugins.MayRegisterDiagnoseTools(c(), registry)
	}
	for _, r := range plugins.DiagnoseRegistrars {
		r(registry)
	}
	return registry
}

func resolveInstance(creator plugins.Creator, pluginName, target string) (any, error) {
	content, err := readInspectPluginConfig(pluginName)
	if err != nil {
		return nil, fmt.Errorf("read plugin config: %w", err)
	}

	pluginObj := creator()
	if err := toml.Unmarshal(content, pluginObj); err != nil {
		return nil, fmt.Errorf("parse plugin config: %w", err)
	}
	if err := plugins.MayApplyPartials(pluginObj); err != nil {
		return nil, fmt.Errorf("apply partials: %w", err)
	}

	instances := plugins.MayGetInstances(pluginObj)
	if len(instances) == 0 {
		return nil, fmt.Errorf("plugin %q has no instances configured", pluginName)
	}

	for _, inst := range instances {
		if err := plugins.MayInit(inst); err != nil {
			continue
		}
		if instanceContainsTarget(inst, target) {
			swapTargetToFront(inst, target)
			return inst, nil
		}
	}

	var available []string
	for _, inst := range instances {
		available = append(available, describeTargets(inst)...)
	}
	return nil, fmt.Errorf("target %q not found in plugin %q config.\nAvailable targets:\n  %s",
		target, pluginName, strings.Join(available, "\n  "))
}

func instanceContainsTarget(inst any, target string) bool {
	v := reflect.ValueOf(inst)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}

	if f := v.FieldByName("Targets"); f.IsValid() && f.Kind() == reflect.Slice {
		for i := 0; i < f.Len(); i++ {
			if f.Index(i).String() == target {
				return true
			}
		}
	}

	if f := v.FieldByName("Target"); f.IsValid() && f.Kind() == reflect.String {
		if f.String() == target {
			return true
		}
	}

	return false
}

func swapTargetToFront(inst any, target string) {
	v := reflect.ValueOf(inst)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}

	f := v.FieldByName("Targets")
	if !f.IsValid() || f.Kind() != reflect.Slice || !f.CanSet() {
		return
	}

	for i := 1; i < f.Len(); i++ {
		if f.Index(i).String() == target {
			first := f.Index(0).String()
			f.Index(0).SetString(target)
			f.Index(i).SetString(first)
			return
		}
	}
}

func describeTargets(inst any) []string {
	v := reflect.ValueOf(inst)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}

	if f := v.FieldByName("Targets"); f.IsValid() && f.Kind() == reflect.Slice {
		targets := make([]string, f.Len())
		for i := 0; i < f.Len(); i++ {
			targets[i] = f.Index(i).String()
		}
		return targets
	}

	if f := v.FieldByName("Target"); f.IsValid() && f.Kind() == reflect.String && f.String() != "" {
		return []string{f.String()}
	}

	return []string{"(unknown)"}
}

func readInspectPluginConfig(pluginName string) ([]byte, error) {
	pluginDir := path.Join(config.Config.ConfigDir, "p."+pluginName)

	files, err := file.FilesUnder(pluginDir)
	if err != nil {
		return nil, fmt.Errorf("read config dir %s: %w", pluginDir, err)
	}

	sort.Strings(files)

	var content []byte
	var tomlCount int
	for _, f := range files {
		if !strings.HasSuffix(f, ".toml") {
			continue
		}
		if tomlCount > 0 {
			content = append(content, '\n', '\n')
		}
		bs, err := file.ReadBytes(path.Join(pluginDir, f))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", f, err)
		}
		content = append(content, bs...)
		tomlCount++
	}

	if tomlCount == 0 {
		return nil, fmt.Errorf("no .toml config files found in %s", pluginDir)
	}
	return content, nil
}

func hasAccessorFactory(registry *diagnose.ToolRegistry, plugin string) bool {
	return registry.HasAccessorFactory(plugin)
}

func availablePluginNames() string {
	names := make([]string, 0, len(plugins.PluginCreators))
	for name := range plugins.PluginCreators {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
