package udpx

import (
	"encoding/binary"
	"os"
	"sync/atomic"
	"time"
)

// UDPX_TRACE_SEQ=1 ./xxx -c client.toml  // 开启seq trace
var traceDataSeq = os.Getenv("UDPX_TRACE_SEQ") == "1"

func (c *UDPConn) newDataFrameBuffer(payload []byte) (MyBuffer, error) {
	if traceDataSeq {
		seq := atomic.AddUint64(&c.txSeq, 1)
		return newDataSeqFrameBuffer(c.maxBufSize, seq, payload)
	}
	return newFrameBuffer(c.maxBufSize, frameTypeData, payload)
}

func (c *UDPConn) decodeDataSeqPayload(payload []byte) ([]byte, bool) {
	if len(payload) < dataSeqHeaderLen {
		c.logger.Warnf("drop short udpx data seq frame, len:%d, local:%v, remote:%v", len(payload), c.LocalAddr(), c.RemoteAddr())
		return nil, false
	}

	seq := binary.BigEndian.Uint64(payload[:dataSeqHeaderLen])
	userPayload := payload[dataSeqHeaderLen:]
	c.checkRxSeq(seq, len(userPayload))
	return userPayload, true
}

func (c *UDPConn) checkRxSeq(seq uint64, payloadLen int) {
	if atomic.CompareAndSwapUint32(&c.rxSeqInited, 0, 1) {
		atomic.StoreUint64(&c.rxNextSeq, seq+1)
		c.logger.Infof("udpx seq init local=%v remote=%v seq=%d payloadLen=%d", c.LocalAddr(), c.RemoteAddr(), seq, payloadLen)
		return
	}

	expected := atomic.LoadUint64(&c.rxNextSeq)
	switch {
	case seq == expected:
		atomic.StoreUint64(&c.rxNextSeq, expected+1)
	case seq > expected:
		atomic.AddUint64(&c.rxGapPkts, seq-expected)
		atomic.StoreUint64(&c.rxNextSeq, seq+1)
	case seq < expected:
		atomic.AddUint64(&c.rxLatePkts, 1)
	}
}

func (c *UDPConn) startSeqStatsLoop() {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()

	var lastGap, lastLate uint64
	for {
		select {
		case <-c.dead:
			return
		case <-t.C:
			gap := atomic.LoadUint64(&c.rxGapPkts)
			late := atomic.LoadUint64(&c.rxLatePkts)
			if gap == lastGap && late == lastLate {
				continue
			}

			c.logger.Warnf("udpx seq stats local=%v remote=%v txSeq=%d rxNextSeq=%d rxGapPkts=%d(+%d) rxLatePkts=%d(+%d)",
				c.LocalAddr(), c.RemoteAddr(),
				atomic.LoadUint64(&c.txSeq),
				atomic.LoadUint64(&c.rxNextSeq),
				gap, gap-lastGap,
				late, late-lastLate,
			)
			lastGap = gap
			lastLate = late
		}
	}
}
