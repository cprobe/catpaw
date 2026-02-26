package plugins

import (
	"flashcat.cloud/catpaw/config"
	"flashcat.cloud/catpaw/pkg/safe"
	"flashcat.cloud/catpaw/types"
)

type Instance interface {
	GetLabels() map[string]string
	GetInterval() config.Duration
	GetAlerting() config.Alerting
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

func MayApplyPartials(p interface{}) error {
	if ap, ok := p.(IApplyPartials); ok {
		return ap.ApplyPartials()
	}
	return nil
}

type Initer interface {
	Init() error
}

func MayInit(t interface{}) error {
	if initer, ok := t.(Initer); ok {
		return initer.Init()
	}
	return nil
}

type Dropper interface {
	Drop()
}

type InstancesGetter interface {
	GetInstances() []Instance
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
