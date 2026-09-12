package localipc

import "errors"

var ErrEndpointInUse = errors.New("local IPC endpoint is already in use")
