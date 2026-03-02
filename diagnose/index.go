package diagnose

import "sync"

// InstanceIndex maintains a global mapping from (plugin, target) to the
// plugin's Instance struct. Used as a fallback path when DiagnoseRequest
// doesn't carry an InstanceRef (e.g., cross-Instance diagnosis in future).
type InstanceIndex struct {
	mu    sync.RWMutex
	index map[string]any // key: "redis::10.0.0.1:6379"
}

// NewInstanceIndex creates an empty index.
func NewInstanceIndex() *InstanceIndex {
	return &InstanceIndex{
		index: make(map[string]any),
	}
}

func indexKey(plugin, target string) string {
	return plugin + "::" + target
}

// Register adds or overwrites an entry.
func (idx *InstanceIndex) Register(plugin, target string, instance any) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.index[indexKey(plugin, target)] = instance
}

// Lookup retrieves the Instance for a given plugin and target.
func (idx *InstanceIndex) Lookup(plugin, target string) (any, bool) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	ins, ok := idx.index[indexKey(plugin, target)]
	return ins, ok
}

// Count returns the number of registered entries.
func (idx *InstanceIndex) Count() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return len(idx.index)
}
