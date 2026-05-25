package udpx

import (
	"encoding/binary"
	"fmt"
	"time"
)

const (
	frameHeaderLen = 3
	frameMagic     = uint16(0x5558) // "UX"
	frameVersion   = byte(1)
)

const (
	frameTypeData byte = iota
	frameTypeHello
	frameTypeHelloAck
	frameTypeClose
	frameTypePing
	frameTypePong
	frameTypeStats
)

const frameStatsPayloadLen = 40

type ConnStats struct {
	TxPackets   uint64
	RxPackets   uint64
	RxDropPkts  uint64
	TxDropPkts  uint64
	TimestampMs uint64
}

func makeVerType(version, typ byte) byte {
	return ((version & 0x03) << 6) | (typ & 0x3f)
}

func parseVerType(vt byte) (version byte, typ byte) {
	return vt >> 6, vt & 0x3f
}

func encodeFrame(typ byte, payload []byte) []byte {
	b := make([]byte, frameHeaderLen+len(payload))
	binary.BigEndian.PutUint16(b[:2], frameMagic)
	b[2] = makeVerType(frameVersion, typ)
	copy(b[frameHeaderLen:], payload)
	return b
}

func decodeFrame(b []byte) (version byte, typ byte, payload []byte, ok bool) {
	if len(b) < frameHeaderLen {
		return 0, 0, nil, false
	}
	if binary.BigEndian.Uint16(b[:2]) != frameMagic {
		return 0, 0, nil, false
	}
	version, typ = parseVerType(b[2])
	if version != frameVersion {
		return version, typ, nil, false
	}
	return version, typ, b[frameHeaderLen:], true
}

func decodeHelloFrame(b []byte) (token []byte, ok bool) {
	_, typ, payload, ok := decodeFrame(b)
	if !ok || typ != frameTypeHello || len(payload) != tokenSize {
		return nil, false
	}
	return payload, true
}

func decodeHelloAckFrame(b []byte) (token []byte, ok bool) {
	_, typ, payload, ok := decodeFrame(b)
	if !ok || typ != frameTypeHelloAck || len(payload) != tokenSize {
		return nil, false
	}
	return payload, true
}

func encodeStatsPayload(s ConnStats) []byte {
	b := make([]byte, frameStatsPayloadLen)
	binary.BigEndian.PutUint64(b[0:8], s.TxPackets)
	binary.BigEndian.PutUint64(b[8:16], s.RxPackets)
	binary.BigEndian.PutUint64(b[16:24], s.RxDropPkts)
	binary.BigEndian.PutUint64(b[24:32], s.TxDropPkts)
	binary.BigEndian.PutUint64(b[32:40], s.TimestampMs)
	return b
}

func decodeStatsPayload(b []byte) (ConnStats, error) {
	if len(b) != frameStatsPayloadLen {
		return ConnStats{}, fmt.Errorf("invalid stats payload len:%d", len(b))
	}
	return ConnStats{
		TxPackets:   binary.BigEndian.Uint64(b[0:8]),
		RxPackets:   binary.BigEndian.Uint64(b[8:16]),
		RxDropPkts:  binary.BigEndian.Uint64(b[16:24]),
		TxDropPkts:  binary.BigEndian.Uint64(b[24:32]),
		TimestampMs: binary.BigEndian.Uint64(b[32:40]),
	}, nil
}

func nowUnixMilli() uint64 {
	return uint64(time.Now().UnixMilli())
}

func trimFrameHeader(b MyBuffer) bool {
	buf, ok := b.(*Buffer)
	if !ok {
		return false
	}
	if len(buf.Bytes()) < frameHeaderLen {
		return false
	}
	buf.roffset += frameHeaderLen
	return true
}
