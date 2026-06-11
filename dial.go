package udpx

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net"
	"sync/atomic"

	pkgerr "github.com/pkg/errors"
	"golang.org/x/net/ipv4"
)

func Dial(ctx context.Context, laddr, raddr string, opts ...UDPConnOpt) (net.Conn, error) {
	return DialWithOpt(ctx, "udp", laddr, raddr, opts...)
}

func DialWithOpt(ctx context.Context, network, laddr, raddr string, opts ...UDPConnOpt) (*UDPConn, error) {
	var err error
	la := &net.UDPAddr{IP: net.IPv4zero, Port: 0}
	if laddr != "" {
		la, err = net.ResolveUDPAddr(network, laddr)
		if err != nil {
			return nil, err
		}
	}
	ra, err := net.ResolveUDPAddr(network, raddr)
	if err != nil {
		return nil, err
	}

	lconn, err := net.DialUDP(network, la, ra)
	if err != nil {
		return nil, err
	}

	err = setSocketBuf(lconn, 1024*1024) //1M
	if err != nil {
		panic(fmt.Errorf("setSocketBuf failed, err:%v", err))
	}

	c := NewUDPConn(nil, lconn, true, ra, opts...)
	err = c.handshake(ctx)
	if err != nil {
		c.Close()
		return nil, err
	}

	if c.standalone {
		if c.readBatchs > 0 {
			//go uc.ReadBatchLoop(uc.rxhandler)
			go c.readBatchLoopv2()
		}
		if c.writeBatchs > 0 {
			//后台起一个goroutine 负责批量写，上层直接write 就行。
			c.txqueue = make(chan MyBuffer, c.txqueuelen) //还是跟以前一样提前初始化, 确保发送数据时，txqueue是确定已经初始化好的。避免上层发送数据时丢失
			go c.writeBatchLoop()
		}
	}

	gLogger.Infof("ok, started UDPConn:%v", c)
	//StartDebugStatsLoop 里会定时打印UDPConn的状态，方便观察和调试
	go c.StartDebugStatsLoop(ctx, DefaultDebugStatsInterval, c.logger)
	return c, nil
}

func (c *UDPConn) ReadBatchLoop(handler func(msg []byte)) error {
	readBatchs := c.readBatchs
	maxBufSize := c.maxBufSize
	pc := c.pc

	rms := make([]ipv4.Message, readBatchs)
	for i := 0; i < len(rms); i++ {
		rms[i] = ipv4.Message{Buffers: [][]byte{make([]byte, maxBufSize)}}
	}
	for {
		n, err := pc.ReadBatch(rms, 0)
		if err != nil {
			c.Close()
			return err
		}
		log.Printf("client ReadBatchLoop got n:%d, len(ms):%d\n", n, len(rms))

		if n == 0 {
			continue
		}
		for i := 0; i < n; i++ {
			if handler != nil {
				handler(rms[i].Buffers[0][:rms[i].N])
			}
		}
	}
}

// todo: 以后也改成pool 来复用对象
func (c *UDPConn) handlePacket(msg []byte) {
	//分配新的内存对象,并且copy 一次
	b := make([]byte, len(msg))
	copy(b, msg)
	c.PutRxQueue(b)
}

// 相比ReadBatchLoop->handlePacket, 复用了对象，少一次copy
func (c *UDPConn) readBatchLoopv2() error {
	var err error
	//InitPool(c.maxBufSize) //fixed bug:在readBatchLoopv2之前就应该初始化Pool,避免UDPConn发送数据时去pool获取内存对象panic
	rms := make([]ipv4.Message, c.readBatchs)
	buffers := make([]MyBuffer, c.readBatchs)
	n := len(rms)
	log.Printf("client:%v->%v,read batchs:%d, maxPacketSize:%d, readLoopv2(use MyBuffer)....",
		c.LocalAddr(), c.RemoteAddr(), c.readBatchs, c.maxBufSize)
	defer func() { log.Printf("%v readBatchLoopv2 quit, err:%v", c, err) }()

	checkLen := int(0)
	//检查
	if !c.standalone {
		panic(fmt.Errorf("only standalone UDPConn will run readBatchLoopv2, UDPConn:%v", c))
	}

	for {
		for i := 0; i < n; i++ {
			b := GetMyBuffer(0) //复用对象
			buffers[i] = b
			rms[i] = ipv4.Message{Buffers: [][]byte{b.Buffer()}} //引用内存对象，系统调用后，直接把数据写入到内存里
		}
		n, err = c.pc.ReadBatch(rms, 0)
		if err != nil {
			c.Close()
			return pkgerr.WithMessagef(err, "UDPConn:%v, ReadBatch err", c)
		}
		if gMode == DebugMode {
			log.Printf("readBatchLoopv2 client:%v->%v, batch got n:%d, max len(ms):%d\n", c.LocalAddr(), c.RemoteAddr(), n, len(rms))
		}
		if n == 0 {
			continue
		}
		for i := 0; i < n; i++ {
			buffers[i].Advance(rms[i].N)

			if c.ln != nil { //判断当前c是否是ln 产生的UDPConn
				//只需要检查独立UPConn在bind 和 connect 之间已经缓存到socket的那部分数据
				if checkLen < c.needCheck {
					checkLen += rms[i].N
					//为了避免独立UPConn在bind 和 connect 之间已经有数据到来，所以这里要检查下数据的源IP是否是connect的IP
					raddr := rms[i].Addr.(*net.UDPAddr)
					if raddr.Port != c.raddr.Port || !raddr.IP.Equal(c.raddr.IP) { //要检查远端的端口号是否一致
						gLogger.Warnf("readBatchLoopv2 client:%v->%v, drop pkt, pkt srcIP:%v, but UDPConn remoteIP:%v\n", c.LocalAddr(), c.RemoteAddr(), rms[i].Addr, c.raddr)
						continue
					}
				}
			}
			c.PutRxQueue2(buffers[i])
		}
	}
}

func (c *UDPConn) PutRxQueue2(b MyBuffer) error {
	//check control packet or data packet, if control packet, then handle it, else put to rxqueue
	_, typ, payload, ok := decodeFrame(b.Bytes())
	if !ok {
		gLogger.Warnf("drop invalid udpx frame, len:%d, client:%v->%v\n", len(b.Bytes()), c.LocalAddr(), c.RemoteAddr())
		Release(b)
		return nil
	}

	switch typ {
	case frameTypeData:
		if !trimFrameHeader(b) {
			// payloadBuffer := GetMyBuffer(len(payload))
			// if _, err := payloadBuffer.Write(payload); err != nil {
			// 	Release(payloadBuffer)
			// 	Release(b)
			// 	return err
			// }
			// Release(b)
			// b = payloadBuffer
			panic("trimFrameHeader failed")
		}
	case frameTypeDataSeq:
		if _, ok := c.decodeDataSeqPayload(payload); !ok {
			Release(b)
			return nil
		}
		if !trimFrameHeaderN(b, frameHeaderLen+dataSeqHeaderLen) {
			Release(b)
			panic("trimFrameHeaderN data seq failed")
		}
	case frameTypeHello:
		// 服务端连接收到重复 Hello，说明客户端可能没收到 HelloAck，可以重发 ack；客户端侧直接丢弃。
		hello, ok := decodeHelloPayload(payload)
		if c.ln != nil && ok && bytes.Equal(hello.Token, c.token[:]) {
			ack, err := encodeHelloAckFrame(c.token[:], SessionPolicy{EnableDataSeq: c.dataSeqEnabled})
			if err != nil {
				Release(b)
				return err
			}
			_, err = c.writeRaw(ack)
			Release(b)
			return err
		}
		Release(b)
		return nil
	case frameTypeHelloAck:
		Release(b)
		return nil
	case frameTypePing:
		_, err := c.writeControlFrame(frameTypePong, payload)
		Release(b)
		return err
	case frameTypePong:
		Release(b)
		return nil
	case frameTypeClose:
		Release(b)
		c.Close()
		return nil
	case frameTypeStats:
		if stats, err := decodeStatsPayload(payload); err == nil {
			c.setPeerStats(stats)
		}
		Release(b)
		return nil
	default:
		gLogger.Warnf("drop unknown udpx frame type:%d, client:%v->%v\n", typ, c.LocalAddr(), c.RemoteAddr())
		Release(b)
		return nil
	}

	//非阻塞模式,避免某个UDPConn 的数据没有被处理而阻塞了listener 或者 UDPConn 继续接受数据
	select {
	case c.rxqueue <- b:
		c.rxPackets += 1
		c.rxDataPkts += 1
	default:
		rxDropPkts := atomic.AddInt64(&c.rxDropPkts, 1)
		//c.rxDropBytes += int64(len(b.Bytes()))

		//iperf跑流量测试时,iperf显示丢包很多,但服务端这里没有打印, 压力测试了很久才打印一次,所以这里导致丢包的
		if rxDropPkts == 1 || rxDropPkts&127 == 0 {
			//panic(fmt.Errorf("notice udpxConn:%v, rxDropPkts:%d\n", c, c.rxDropPkts))
			gLogger.Warnf("notice udpxConn:%v, rxDropPkts:%d\n", c, rxDropPkts)
		}
		Release(b)
		return ErrRxQueueFull
	}
	return nil
}
