package plugins

import (
	"github.com/cprobe/catpaw/digcore/config"
	"github.com/cprobe/catpaw/digcore/diagnose"
	"github.com/cprobe/catpaw/digcore/pkg/safe"
	"github.com/cprobe/catpaw/digcore/types"
)

type Instance interface {
	GetLabels() map[string]string
	GetInterval() config.Duration
	GetAlerting() config.Alerting
	GetDiagnoseConfig() config.DiagnoseConfig
}

type Plugin interface {
	GetLabels() map[string]string
	GetInterval() config.Duration
}

type IApplyPartials interface {
	ApplyPartials() error
}

type Gatherer interface {
	Gather(*safe.Queue[*types.Event])
}

func MayApplyPartials(p any) error {
	if ap, ok := p.(IApplyPartials); ok {
		return ap.ApplyPartials()
	}
	return nil
}

type Initer interface {
	Init() error
}

func MayInit(t any) error {
	if initer, ok := t.(Initer); ok {
		return initer.Init()
	}
	return nil
}

type Dropper interface {
	Drop()
}

// PluginIniter is called once on the Plugin object after config unmarshal,
// before any Instance goroutines start. Use for shared resource initialization
// (e.g. Informer, connection pool, leader election).
type PluginIniter interface {
	PluginInit() error
}

// PluginDropper is called once on the Plugin object after all Instance
// goroutines have stopped. Use for shared resource cleanup.
type PluginDropper interface {
	PluginDrop()
}

type InstancesGetter interface {
	GetInstances() []Instance
}

func MayGather(t any, q *safe.Queue[*types.Event]) {
	if gather, ok := t.(Gatherer); ok {
		gather.Gather(q)
	}
}

func MayDrop(t any) {
	if dropper, ok := t.(Dropper); ok {
		dropper.Drop()
	}
}

func MayPluginInit(t any) error {
	if initer, ok := t.(PluginIniter); ok {
		return initer.PluginInit()
	}
	return nil
}

func MayPluginDrop(t any) {
	if dropper, ok := t.(PluginDropper); ok {
		dropper.PluginDrop()
	}
}

func MayGetInstances(t any) []Instance {
	if instancesGetter, ok := t.(InstancesGetter); ok {
		return instancesGetter.GetInstances()
	}
	return nil
}

type Diagnosable interface {
	RegisterDiagnoseTools(registry *diagnose.ToolRegistry)
}

func MayRegisterDiagnoseTools(t any, registry *diagnose.ToolRegistry) {
	if d, ok := t.(Diagnosable); ok {
		d.RegisterDiagnoseTools(registry)
	}
}

type Creator func() Plugin

var PluginCreators = map[string]Creator{}

func Add(name string, creator Creator) {
	PluginCreators[name] = creator
}

// DiagnoseRegistrar registers diagnostic tools that are independent of any
// alert-collecting plugin (e.g. system-level troubleshooting utilities).
type DiagnoseRegistrar func(registry *diagnose.ToolRegistry)

var DiagnoseRegistrars []DiagnoseRegistrar

func AddDiagnoseRegistrar(r DiagnoseRegistrar) {
	DiagnoseRegistrars = append(DiagnoseRegistrars, r)
}
