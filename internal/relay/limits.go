package relay

import (
	"errors"
	"fmt"
)

const (
	MaxConcurrentStreams          = 512
	MaxConcurrentListeners        = 256
	MaxConcurrentDatagrams        = 64
	MaxConcurrentReverseDatagrams = 64
	MaxReverseDatagramFlows       = 256
	MaxConcurrentOpenOperations   = 32
	datagramQueueDepth            = 16
)

var ErrResourceLimit = errors.New("relay resource limit reached")

func resourceLimitError(resource string, maximum int) error {
	return fmt.Errorf("%w: maximum %d %s", ErrResourceLimit, maximum, resource)
}
