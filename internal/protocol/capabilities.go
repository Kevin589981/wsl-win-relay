package protocol

import (
	"encoding/binary"
	"errors"
)

const (
	CapabilityTCP uint64 = 1 << iota
	CapabilityReverse
	CapabilityReverseUDP
	CapabilityReserveCommit
	CapabilityUDP
	CapabilityFlowControl
)

const AllCapabilities = CapabilityTCP | CapabilityReverse | CapabilityReverseUDP | CapabilityReserveCommit | CapabilityUDP | CapabilityFlowControl

func EncodeCapabilities(capabilities uint64) []byte {
	payload := make([]byte, 8)
	binary.BigEndian.PutUint64(payload, capabilities)
	return payload
}

func DecodeCapabilities(payload []byte) uint64 {
	if len(payload) != 8 {
		return 0
	}
	return binary.BigEndian.Uint64(payload)
}

// EncodeHelloOK carries capabilities and, when non-zero, the identity of the
// broker process that owns the peer sockets. The capability-only form remains
// valid for the legacy stdio relay.
func EncodeHelloOK(capabilities, instanceID uint64) []byte {
	if instanceID == 0 {
		return EncodeCapabilities(capabilities)
	}
	payload := make([]byte, 16)
	binary.BigEndian.PutUint64(payload, capabilities)
	binary.BigEndian.PutUint64(payload[8:], instanceID)
	return payload
}

func DecodeHelloOK(payload []byte) (uint64, uint64, error) {
	if len(payload) != 8 && len(payload) != 16 {
		return 0, 0, errors.New("hello response must contain capabilities and optional instance id")
	}
	capabilities := binary.BigEndian.Uint64(payload)
	if len(payload) == 8 {
		return capabilities, 0, nil
	}
	return capabilities, binary.BigEndian.Uint64(payload[8:]), nil
}
