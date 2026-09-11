package protocol

import "encoding/binary"

const (
	CapabilityTCP uint64 = 1 << iota
	CapabilityReverse
	CapabilityReserveCommit
	CapabilityUDP
	CapabilityFlowControl
)

const AllCapabilities = CapabilityTCP | CapabilityReverse | CapabilityReserveCommit | CapabilityUDP | CapabilityFlowControl

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
