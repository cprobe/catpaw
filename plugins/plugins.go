package plugins

import (
	"flashcat.cloud/catpaw/config"
	"flashcat.cloud/catpaw/pkg/safe"
	"flashcat.cloud/catpaw/types"
)

type Instance interface {
	Initialized() bool
	SetInitialized()

	GetLabels() map[string]string
	GetInterval() config.Duration
	InitInternalConfig() error
	Process(*safe.Queue[*types.Event]) *safe.Queue[*types.Event]
}

type Plugin interface {
	GetLabels() map[string]string
	GetInterval() config.Duration
	InitInternalConfig() error
	Process(*safe.Queue[*types.Event]) *safe.Queue[*types.Event]
}

type Initializer interface {
	Init() error
}

type Gatherer interface {
	Gather(*safe.Queue[*types.Event])
}

type Dropper interface {
	Drop()
}

type InstancesGetter interface {
	GetInstances() []Instance
}

func MayInit(t interface{}) error {
	if initializer, ok := t.(Initializer); ok {
		return initializer.Init()
	}
	return nil
}

func MayGather(t interface{}, q *safe.Queue[*types.Event]) {
	if gather, ok := t.(Gatherer); ok {
		gather.Gather(q)
	}
}

func MayDrop(t interface{}) {
	if dropper, ok := t.(Dropper); ok {
		dropper.Drop()
	}
}

func MayGetInstances(t interface{}) []Instance {
	if instancesGetter, ok := t.(InstancesGetter); ok {
		return instancesGetter.GetInstances()
	}
	return nil
}

type Creator func() Plugin

var PluginCreators = map[string]Creator{}

func Add(name string, creator Creator) {
	PluginCreators[name] = creator
}
