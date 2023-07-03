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
	pluginName   string
	pluginObject plugins.Plugin
	quitChan     []chan struct{}
	Instances    []plugins.Instance
}

func newPluginRunner(pluginName string, p plugins.Plugin) *PluginRunner {
	return &PluginRunner{
		pluginName:   pluginName,
		pluginObject: p,
	}
}

func (r *PluginRunner) stop() {
	for i := 0; i < len(r.Instances); i++ {
		r.quitChan[i] <- struct{}{}
		plugins.MayDrop(r.Instances[i])
	}
}

func (r *PluginRunner) start() {
	r.Instances = plugins.MayGetInstances(r.pluginObject)
	r.quitChan = make([]chan struct{}, len(r.Instances))
	for i := 0; i < len(r.Instances); i++ {
		r.quitChan[i] = make(chan struct{}, 1)
		ins := r.Instances[i]
		ch := r.quitChan[i]
		go r.startInstancePlugin(ins, ch)
		time.Sleep(50 * time.Millisecond)
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
		engine.PushRawEvents(r.pluginName, ins, queue)
	}
}
