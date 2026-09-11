package protocol

import "encoding/binary"

func EncodeWindowUpdate(count uint32) []byte {
	payload := make([]byte, 4)
	binary.BigEndian.PutUint32(payload, count)
	return payload
}

func DecodeWindowUpdate(payload []byte) uint32 {
	if len(payload) != 4 {
		return 0
	}
	return binary.BigEndian.Uint32(payload)
}
