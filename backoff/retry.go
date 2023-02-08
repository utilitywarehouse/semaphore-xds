package backoff

import (
	"time"

	"github.com/utilitywarehouse/semaphore-xds/log"
	"github.com/utilitywarehouse/semaphore-xds/queue"
)

type watchOperation func(*queue.Queue, <-chan struct{}) error

const (
	defaultBackoffJitter = true
	defaultBackoffMin    = 2 * time.Second
	defaultBackoffMax    = 1 * time.Minute
)

// Retry will use the default backoff values to retry the passed operation
func RetryWatch(op watchOperation, Q *queue.Queue, stopCh <-chan struct{}, description string) {
	b := &Backoff{
		Jitter: defaultBackoffJitter,
		Min:    defaultBackoffMin,
		Max:    defaultBackoffMax,
	}
	RetryWithBackoff(op, Q, stopCh, b, description)
}

// RetryWithBackoff will retry the passed function (operation) using the given
// backoff
func RetryWithBackoff(op watchOperation, Q *queue.Queue, stopCh <-chan struct{}, b *Backoff, description string) {
	b.Reset()
	for {
		err := op(Q, stopCh)
		if err == nil {
			return
		}
		d := b.Duration()
		log.Logger.Error("Retry failed",
			"description", description,
			"error", err,
			"backoff", d,
		)
		time.Sleep(d)
	}
}
