package udpx

import (
	"encoding/binary"
	"fmt"
	"time"
)

// UDPX协议格式和大小定义：
//
// 所有 UDPX 报文都以 3 字节公共帧头开始：
//
//	0               1               2
//	+---------------+---------------+---------------+
//	| magic(0x5558)                 | ver(2b)|type(6b)|
//	+---------------+---------------+---------------+
//	| payload ...
//
// magic 使用大端 uint16，固定为 "UX"，用于快速过滤非 UDPX 报文。
// 第 3 字节高 2 bit 是协议版本，低 6 bit 是帧类型；当前版本为 1，
// 因此最多可表达 4 个版本和 64 种帧类型。
//
// frameTypeData 的 payload 是用户数据本身。
// frameTypeDataSeq 的 payload 前 8 字节为大端 uint64 seq，后面才是用户数据，
// 用于按连接统计乱序、丢包和迟到包。
//
// Hello payload:
//
//	version(1) | flags(1) | token(tokenSize) | authLen(uint16) | authData
//
// HelloAck payload:
//
//	version(1) | flags(1) | token(tokenSize)
//
// Stats payload:
//
//	txPackets | rxPackets | rxDropPkts | txDropPkts | timestampMs
//
// 以上字段均为大端 uint64，每个 8 字节，共 40 字节。
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
	frameTypeDataSeq
)

const (
	frameStatsPayloadLen = 40
	dataSeqHeaderLen     = 8 // seq uint64
)

const (
	helloPayloadVersion   byte = 1
	helloPayloadHeaderLen      = 1 + 1 + tokenSize + 2
	helloAckPayloadLen         = 1 + 1 + tokenSize
)

const (
	helloFlagDataSeqCapable byte = 1 << iota
	helloFlagDataSeqRequested
)

const (
	helloAckFlagDataSeqEnabled byte = 1 << iota
)

type ConnStats struct {
	TxPackets   uint64
	RxPackets   uint64
	RxDropPkts  uint64
	TxDropPkts  uint64
	TimestampMs uint64
}

type helloFrame struct {
	Token             []byte
	AuthData          []byte
	ClientCanDataSeq  bool
	ClientWantDataSeq bool
}

type helloAckFrame struct {
	Token          []byte
	DataSeqEnabled bool
}

func makeVerType(version, typ byte) byte {
	return ((version & 0x03) << 6) | (typ & 0x3f)
}

func parseVerType(vt byte) (version byte, typ byte) {
	return vt >> 6, vt & 0x3f
}

func encodeFrame(typ byte, payload []byte) []byte {
	b := make([]byte, frameHeaderLen+len(payload))
	writeFrameHeader(b[:frameHeaderLen], typ)
	copy(b[frameHeaderLen:], payload)
	return b
}

func writeFrameHeader(b []byte, typ byte) {
	binary.BigEndian.PutUint16(b[:2], frameMagic)
	b[2] = makeVerType(frameVersion, typ)
}

func newFrameBuffer(maxPacketSize int, typ byte, payload []byte) (MyBuffer, error) {
	if len(payload)+frameHeaderLen > maxPacketSize {
		return nil, fmt.Errorf("payload len:%d plus frame header:%d > max packet size:%d", len(payload), frameHeaderLen, maxPacketSize)
	}

	b := GetMyBuffer(frameHeaderLen + len(payload))
	buf := b.Buffer()
	writeFrameHeader(buf[:frameHeaderLen], typ)
	b.Advance(frameHeaderLen)

	if _, err := b.Write(payload); err != nil {
		Release(b)
		return nil, err
	}
	return b, nil
}

func newDataSeqFrameBuffer(maxPacketSize int, seq uint64, payload []byte) (MyBuffer, error) {
	if len(payload)+frameHeaderLen+dataSeqHeaderLen > maxPacketSize {
		return nil, fmt.Errorf("payload len:%d plus frame header:%d plus seq header:%d > max packet size:%d", len(payload), frameHeaderLen, dataSeqHeaderLen, maxPacketSize)
	}

	b := GetMyBuffer(frameHeaderLen + dataSeqHeaderLen + len(payload))
	buf := b.Buffer()
	writeFrameHeader(buf[:frameHeaderLen], frameTypeDataSeq)
	binary.BigEndian.PutUint64(buf[frameHeaderLen:frameHeaderLen+dataSeqHeaderLen], seq)
	b.Advance(frameHeaderLen + dataSeqHeaderLen)

	if _, err := b.Write(payload); err != nil {
		Release(b)
		return nil, err
	}
	return b, nil
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

func encodeHelloFrame(token, authData []byte, clientCanDataSeq, clientWantDataSeq bool) ([]byte, error) {
	if len(token) != tokenSize {
		return nil, fmt.Errorf("invalid hello token len:%d", len(token))
	}
	if len(authData) > 0xffff {
		return nil, fmt.Errorf("hello auth data too large:%d", len(authData))
	}
	flags := byte(0)
	if clientCanDataSeq {
		flags |= helloFlagDataSeqCapable
	}
	if clientWantDataSeq {
		flags |= helloFlagDataSeqRequested
	}

	payload := make([]byte, helloPayloadHeaderLen+len(authData))
	payload[0] = helloPayloadVersion
	payload[1] = flags
	copy(payload[2:2+tokenSize], token)
	binary.BigEndian.PutUint16(payload[2+tokenSize:helloPayloadHeaderLen], uint16(len(authData)))
	copy(payload[helloPayloadHeaderLen:], authData)
	return encodeFrame(frameTypeHello, payload), nil
}

func decodeHelloFrame(b []byte) (helloFrame, bool) {
	_, typ, payload, ok := decodeFrame(b)
	if !ok || typ != frameTypeHello {
		return helloFrame{}, false
	}
	return decodeHelloPayload(payload)
}

func decodeHelloPayload(payload []byte) (helloFrame, bool) {
	if len(payload) < helloPayloadHeaderLen || payload[0] != helloPayloadVersion {
		return helloFrame{}, false
	}
	authDataLen := int(binary.BigEndian.Uint16(payload[2+tokenSize : helloPayloadHeaderLen]))
	if len(payload) != helloPayloadHeaderLen+authDataLen {
		return helloFrame{}, false
	}
	flags := payload[1]
	return helloFrame{
		Token:             cloneBytes(payload[2 : 2+tokenSize]),
		AuthData:          cloneBytes(payload[helloPayloadHeaderLen:]),
		ClientCanDataSeq:  flags&helloFlagDataSeqCapable != 0,
		ClientWantDataSeq: flags&helloFlagDataSeqRequested != 0,
	}, true
}

func encodeHelloAckFrame(token []byte, policy SessionPolicy) ([]byte, error) {
	if len(token) != tokenSize {
		return nil, fmt.Errorf("invalid hello ack token len:%d", len(token))
	}
	flags := byte(0)
	if policy.EnableDataSeq {
		flags |= helloAckFlagDataSeqEnabled
	}

	payload := make([]byte, helloAckPayloadLen)
	payload[0] = helloPayloadVersion
	payload[1] = flags
	copy(payload[2:], token)
	return encodeFrame(frameTypeHelloAck, payload), nil
}

func decodeHelloAckFrame(b []byte) (helloAckFrame, bool) {
	_, typ, payload, ok := decodeFrame(b)
	if !ok || typ != frameTypeHelloAck || len(payload) != helloAckPayloadLen || payload[0] != helloPayloadVersion {
		return helloAckFrame{}, false
	}
	flags := payload[1]
	return helloAckFrame{
		Token:          cloneBytes(payload[2:]),
		DataSeqEnabled: flags&helloAckFlagDataSeqEnabled != 0,
	}, true
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
	return trimFrameHeaderN(b, frameHeaderLen)
}

func trimFrameHeaderN(b MyBuffer, n int) bool {
	buf, ok := b.(*Buffer)
	if !ok {
		return false
	}
	if len(buf.Bytes()) < n {
		return false
	}
	buf.roffset += n
	return true
}
