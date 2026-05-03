package cmd

import (
	"fmt"
	"time"
)

type spinner struct {
	stop chan struct{}
	done chan struct{}
}

func startSpinner(msg string) *spinner {
	s := &spinner{stop: make(chan struct{}), done: make(chan struct{})}
	frames := []string{"|", "/", "-", "\\"}
	go func() {
		defer close(s.done)
		i := 0
		for {
			select {
			case <-s.stop:
				fmt.Printf("\r%-60s\r", "") // clear line
				return
			case <-time.After(100 * time.Millisecond):
				fmt.Printf("\r%s %s", frames[i%len(frames)], msg)
				i++
			}
		}
	}()
	return s
}

func (s *spinner) Stop() {
	close(s.stop)
	<-s.done
}
