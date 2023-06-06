package agent

import (
	"time"

	"flashcat.cloud/catpaw/config"
	"flashcat.cloud/catpaw/engine"
	"flashcat.cloud/catpaw/logger"
	"flashcat.cloud/catpaw/pkg/runtimex"
	"flashcat.cloud/catpaw/pkg/safe"
	"flashcat.cloud/catpaw/plugins"
	"flashcat.cloud/catpaw/types"
)

type PluginRunner struct {
	pluginName     string
	pluginObject   plugins.Plugin
	quitChanForSys chan struct{}
	quitChanForIns []chan struct{}
	Instances      []plugins.Instance
}

func newPluginRunner(pluginName string, p plugins.Plugin) *PluginRunner {
	return &PluginRunner{
		pluginName:   pluginName,
		pluginObject: p,
	}
}

func (r *PluginRunner) stop() {
	if r.pluginObject.IsSystemPlugin() {
		r.quitChanForSys <- struct{}{}
		plugins.MayDrop(r.pluginObject)
		return
	}

	for i := 0; i < len(r.Instances); i++ {
		r.quitChanForIns[i] <- struct{}{}
		plugins.MayDrop(r.Instances[i])
	}
}

func (r *PluginRunner) start() {
	if r.pluginObject.IsSystemPlugin() {
		r.quitChanForSys = make(chan struct{}, 1)
		go r.startSystemPlugin()
		return
	}

	r.Instances = plugins.MayGetInstances(r.pluginObject)
	r.quitChanForIns = make([]chan struct{}, len(r.Instances))
	for i := 0; i < len(r.Instances); i++ {
		r.quitChanForIns[i] = make(chan struct{}, 1)
		go r.startInstancePlugin(r.Instances[i], r.quitChanForIns[i])
	}
}

func (r *PluginRunner) startSystemPlugin() {
	interval := r.pluginObject.GetInterval()
	if interval == 0 {
		interval = config.Config.Global.Interval
	}

	if err := r.pluginObject.InitInternalConfig(); err != nil {
		logger.Logger.Errorw("init internal config fail: "+err.Error(), "plugin", r.pluginName)
		return
	}

	timer := time.NewTimer(0)
	defer timer.Stop()

	var start time.Time

	for {
		select {
		case <-r.quitChanForSys:
			close(r.quitChanForSys)
			return
		case <-timer.C:
			start = time.Now()
			r.gatherSystemPlugin()
			next := time.Duration(interval) - time.Since(start)
			if next < 0 {
				next = 0
			}
			timer.Reset(next)
		}
	}
}

func (r *PluginRunner) gatherSystemPlugin() {
	defer func() {
		if rc := recover(); rc != nil {
			logger.Logger.Errorw("gather system plugin panic: "+string(runtimex.Stack(3)), "plugin", r.pluginName)
		}
	}()

	queue := safe.NewQueue[*types.Event]()
	plugins.MayGather(r.pluginObject, queue)
	if queue.Len() > 0 {
		engine.PushRawEvents(r.pluginName, r.pluginObject, queue)
	}
}

func (r *PluginRunner) startInstancePlugin(instance plugins.Instance, ch chan struct{}) {
	interval := instance.GetInterval()
	if interval == 0 {
		interval = r.pluginObject.GetInterval()
		if interval == 0 {
			interval = config.Config.Global.Interval
		}
	}

	if err := instance.InitInternalConfig(); err != nil {
		logger.Logger.Errorw("init internal config fail: "+err.Error(), "plugin", r.pluginName)
		return
	}

	timer := time.NewTimer(0)
	defer timer.Stop()

	var start time.Time

	for {
		select {
		case <-ch:
			close(ch)
			return
		case <-timer.C:
			start = time.Now()
			r.gatherInstancePlugin(instance)
			next := time.Duration(interval) - time.Since(start)
			if next < 0 {
				next = 0
			}
			timer.Reset(next)
		}
	}
}

func (r *PluginRunner) gatherInstancePlugin(ins plugins.Instance) {
	defer func() {
		if rc := recover(); rc != nil {
			logger.Logger.Errorw("gather instance plugin panic: "+string(runtimex.Stack(3)), "plugin", r.pluginName)
		}
	}()

	queue := safe.NewQueue[*types.Event]()
	plugins.MayGather(ins, queue)
	if queue.Len() > 0 {
		engine.PushRawEvents(r.pluginName, r.pluginObject, queue)
	}
}
