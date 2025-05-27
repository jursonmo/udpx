package udpx

import (
	"net"

	pkgerr "github.com/pkg/errors"
	"golang.org/x/net/ipv4"
)

// readBatchLoop ->handlePacket:分配新的内存对象,并且copy 一次
// 为了复用对象,同时减少一次内存copy, 实现 Listener readBatchLoopv2 -> handleBuffer
func (l *Listener) readBatchLoopv2() error {
	var err error
	InitPool(l.maxPacketSize)
	rms := make([]ipv4.Message, l.batchs)
	buffers := make([]MyBuffer, l.batchs)
	n := len(rms)
	l.logger.Infof("%v, started with readLoopv2(use MyBuffer)....\n", l)
	defer func() { l.logger.Errorf("%v, readLoopv2(use MyBuffer) quit, err:%v\n", l, err) }()
	for {
		for i := 0; i < n; i++ {
			b := GetMyBuffer(0)
			buffers[i] = b
			rms[i] = ipv4.Message{Buffers: [][]byte{b.Buffer()}}
		}
		n, err = l.pc.ReadBatch(rms, 0)
		if err != nil {
			l.logger.Errorf("%v, ReadBatch err:%v\n", l, err)
			l.Close()
			return pkgerr.WithMessagef(err, "listener:%v, ReadBatch err", l.lconn.LocalAddr())
		}

		if n == 0 {
			continue
		}

		if l.mode == DebugMode {
			l.logger.Infof("readBatchLoopv2 listener id:%d, batch got n:%d, max len(ms):%d\n", l.id, n, len(rms))
			for i := 0; i < n; i++ {
				l.logger.Infof("readBatchLoopv2 listener id:%d, ms[%d].N:%d, ms[%d].Addr:%v\n", l.id, i, rms[i].N, i, rms[i].Addr)
			}
		}

		for i := 0; i < n; i++ {
			buffers[i].Advance(rms[i].N)
			l.handleBuffer(rms[i].Addr, buffers[i])
		}
	}
}

func (l *Listener) handleBuffer(addr net.Addr, b MyBuffer) {
	//只统计非ctrl数据的包数
	if uc, isCtrlData := l.getUDPConn(addr, b.Bytes()); uc != nil && !isCtrlData {
		if err := uc.PutRxQueue2(b); err != nil {
			l.rxDropPkts++
		} else {
			l.rxPackets++
		}
	}
}
