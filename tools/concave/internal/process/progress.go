package process

import (
	"context"
	"sync"
	"time"
)

type Observation struct {
	Phase    string
	Detail   string
	Finished bool
	Elapsed  time.Duration
}
type progressKey struct{}

func WithProgress(ctx context.Context, observe func(Observation)) context.Context {
	return context.WithValue(ctx, progressKey{}, observe)
}

// Phase emits observations only. Timing never participates in artifact identity.
func Phase(ctx context.Context, phase, detail string) func() {
	observe, _ := ctx.Value(progressKey{}).(func(Observation))
	if observe == nil {
		return func() {}
	}
	started := time.Now()
	observe(Observation{Phase: phase, Detail: detail})
	var once sync.Once
	return func() {
		once.Do(func() {
			observe(Observation{Phase: phase, Detail: detail, Finished: true, Elapsed: time.Since(started)})
		})
	}
}
