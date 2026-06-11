package udpx

import (
	"encoding/binary"
	"fmt"
	"os"
	"sync/atomic"
	"time"
)

// UDPX_TRACE_SEQ_CLIENT=1 ./xxx -c client.toml  // client 默认请求 seq trace
// UDPX_TRACE_SEQ_SERVER=1 ./xxx -c server.toml  // server 默认允许 seq trace
// UDPX_TRACE_SEQ=1 仍可作为同时开启两端的兼容兜底。middle node 既是 client 也是 server，可以通过它开启两种角色的 seq trace。
var traceClientDataSeq bool = false // client 默认不开启 seq trace, 但可以通过环境变量开启
var traceServerDataSeq bool = true  // server 默认开启 seq trace, 但最终由client hello报文决定是否开启。
func init() {
	if enabled, err := traceSeqEnv("UDPX_TRACE_SEQ_CLIENT"); err == nil {
		traceClientDataSeq = enabled
	}
	if enabled, err := traceSeqEnv("UDPX_TRACE_SEQ_SERVER"); err == nil {
		traceServerDataSeq = enabled
	}
	fmt.Printf("traceClientDataSeq:%v, traceServerDataSeq:%v\n", traceClientDataSeq, traceServerDataSeq)
}

func traceSeqEnv(name string) (bool, error) {
	if v, ok := os.LookupEnv(name); ok {
		return v == "1", nil
	}
	if v, ok := os.LookupEnv("UDPX_TRACE_SEQ"); ok {
		return v == "1", nil
	}
	return false, fmt.Errorf("env %s not found", name)
}

func (c *UDPConn) newDataFrameBuffer(payload []byte) (MyBuffer, error) {
	if c.dataSeqEnabled {
		seq := atomic.AddUint64(&c.txSeq, 1)
		return newDataSeqFrameBuffer(c.maxBufSize, seq, payload)
	}
	return newFrameBuffer(c.maxBufSize, frameTypeData, payload)
}

func (c *UDPConn) setDataSeqEnabled(enabled bool) {
	c.dataSeqEnabled = enabled
	if enabled {
		c.startSeqStatsLoopOnce()
	}
}

func (c *UDPConn) startSeqStatsLoopOnce() {
	if atomic.CompareAndSwapUint32(&c.seqStatsStarted, 0, 1) {
		go c.startSeqStatsLoop()
	}
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
	c.logger.Infof("start udpx seq stats loop, local=%v remote=%v", c.LocalAddr(), c.RemoteAddr())
	defer c.logger.Errorf("exit udpx seq stats loop, local=%v remote=%v", c.LocalAddr(), c.RemoteAddr())

	t := time.NewTicker(3 * time.Second)
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
