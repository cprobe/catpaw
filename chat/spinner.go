package chat

import (
	"fmt"
	"sync"
	"time"
)

var spinnerFrames = []string{"|", "/", "-", "\\"}

type spinner struct {
	done chan struct{}
	wg   sync.WaitGroup
}

func startSpinner(msg string) *spinner {
	s := &spinner{done: make(chan struct{})}
	s.wg.Add(1)
	go s.run(msg)
	return s
}

func (s *spinner) run(msg string) {
	defer s.wg.Done()
	start := time.Now()
	i := 0
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-s.done:
			fmt.Print("\r\033[K")
			return
		case <-ticker.C:
			elapsed := time.Since(start).Truncate(time.Second)
			fmt.Printf("\r\033[K\033[36m%s\033[0m %s (%v)", spinnerFrames[i%len(spinnerFrames)], msg, elapsed)
			i++
		}
	}
}

func (s *spinner) stop() {
	close(s.done)
	s.wg.Wait()
}
