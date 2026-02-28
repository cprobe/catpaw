package agent

import (
	"fmt"
	"sync"
	"time"

	"github.com/cprobe/catpaw/config"
	"github.com/cprobe/catpaw/engine"
	"github.com/cprobe/catpaw/logger"
	"github.com/cprobe/catpaw/pkg/runtimex"
	"github.com/cprobe/catpaw/pkg/safe"
	"github.com/cprobe/catpaw/plugins"
	"github.com/cprobe/catpaw/types"
)

type PluginRunner struct {
	pluginName   string
	pluginObject plugins.Plugin
	quitChan     []chan struct{}
	wg           sync.WaitGroup
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
		close(r.quitChan[i])
	}
	r.wg.Wait()
	for i := 0; i < len(r.Instances); i++ {
		plugins.MayDrop(r.Instances[i])
	}
}

func (r *PluginRunner) start() {
	r.Instances = plugins.MayGetInstances(r.pluginObject)
	r.quitChan = make([]chan struct{}, len(r.Instances))
	for i := 0; i < len(r.Instances); i++ {
		r.quitChan[i] = make(chan struct{})
		ins := r.Instances[i]
		ch := r.quitChan[i]
		r.wg.Add(1)
		go r.startInstancePlugin(ins, ch)
		time.Sleep(50 * time.Millisecond)
	}
}

func (r *PluginRunner) startInstancePlugin(instance plugins.Instance, ch chan struct{}) {
	defer r.wg.Done()

	interval := instance.GetInterval()
	if interval == 0 {
		interval = r.pluginObject.GetInterval()
		if interval == 0 {
			interval = config.Config.Global.Interval
		}
	}

	if err := plugins.MayInit(instance); err != nil {
		logger.Logger.Errorw("init plugin instance fail", "plugin", r.pluginName, "error", err)
		return
	}

	timer := time.NewTimer(0)
	defer timer.Stop()

	var start time.Time

	for {
		select {
		case <-ch:
			return
		case <-timer.C:
			start = time.Now()
			r.gatherInstancePlugin(instance)
			select {
			case <-ch:
				return
			default:
			}
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
			logger.Logger.Errorw("gather instance plugin panic", "plugin", r.pluginName, "stack", string(runtimex.Stack(3)))

			// 使用全新的 panicQueue，而非复用可能处于不一致状态的 queue
			panicQueue := safe.NewQueue[*types.Event]()
			panicQueue.PushFront(types.BuildEvent(map[string]string{
				"check":  r.pluginName + "::panic",
				"target": r.pluginName,
			}).SetTitleRule("[check]").
				SetEventStatus(types.EventStatusCritical).
				SetDescription(fmt.Sprintf("plugin panic: %v", rc)))
			engine.PushRawEvents(r.pluginName, r.pluginObject, ins, panicQueue)
		}
	}()

	queue := safe.NewQueue[*types.Event]()
	plugins.MayGather(ins, queue)
	if queue.Len() > 0 {
		engine.PushRawEvents(r.pluginName, r.pluginObject, ins, queue)
	}
}
