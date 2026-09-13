package protocol

import (
	"encoding/binary"
	"errors"
	"fmt"
)

func EncodeDatagram(endpoint string, data []byte) ([]byte, error) {
	if len(endpoint) == 0 || len(endpoint) > MaxTargetSize {
		return nil, fmt.Errorf("invalid datagram endpoint length")
	}
	if len(endpoint)+2+len(data) > MaxDatagramSize {
		return nil, fmt.Errorf("datagram exceeds %d bytes", MaxDatagramSize)
	}
	payload := make([]byte, 2+len(endpoint)+len(data))
	binary.BigEndian.PutUint16(payload[:2], uint16(len(endpoint)))
	copy(payload[2:], endpoint)
	copy(payload[2+len(endpoint):], data)
	return payload, nil
}

func DecodeDatagram(payload []byte) (string, []byte, error) {
	if len(payload) < 3 {
		return "", nil, errors.New("truncated datagram")
	}
	if len(payload) > MaxDatagramSize {
		return "", nil, errors.New("oversized datagram")
	}
	length := int(binary.BigEndian.Uint16(payload[:2]))
	if length == 0 || length > MaxTargetSize || 2+length > len(payload) {
		return "", nil, errors.New("invalid datagram endpoint")
	}
	return string(payload[2 : 2+length]), payload[2+length:], nil
}
