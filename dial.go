package udpx

import (
	"context"
	"log"
	"net"

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

	c := NewUDPConn(nil, lconn, ra, opts...)
	// if c.rxhandler != nil {
	// 	go c.ReadBatchLoop(c.rxhandler)
	// }
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
func (c *UDPConn) readBatchLoopv2() {
	var err error
	InitPool(c.maxBufSize)
	rms := make([]ipv4.Message, c.readBatchs)
	buffers := make([]MyBuffer, c.readBatchs)
	n := len(rms)
	log.Printf("client:%v->%v,read batchs:%d, maxPacketSize:%d, readLoopv2(use MyBuffer)....",
		c.LocalAddr(), c.RemoteAddr(), c.readBatchs, c.maxBufSize)
	for {
		for i := 0; i < n; i++ {
			b := GetMyBuffer(0) //复用对象
			buffers[i] = b
			rms[i] = ipv4.Message{Buffers: [][]byte{b.Buffer()}} //引用内存对象，系统调用后，直接把数据写入到内存里
		}
		n, err = c.pc.ReadBatch(rms, 0)
		if err != nil {
			c.Close()
			panic(err)
		}
		if gMode == DebugMode {
			log.Printf("readBatchLoopv2 client:%v->%v, batch got n:%d, len(ms):%d\n", c.LocalAddr(), c.RemoteAddr(), n, len(rms))
		}
		if n == 0 {
			continue
		}
		for i := 0; i < n; i++ {
			buffers[i].Advance(rms[i].N)
			c.PutRxQueue2(buffers[i])
		}
	}
}

func (c *UDPConn) PutRxQueue2(b MyBuffer) {
	//todo: check control packet or data packet,
	//但是我认为，不应该在这里做控制层相关的业务，因为它只需提供连接的收发操作即可
	//如果需要握手验证和心跳，应该是在业务层做，或者在业务层和底层之间加一层来实现协议格式和控制协议报文

	//非阻塞模式,避免某个UDPConn 的数据没有被处理而阻塞了listener 或者 UDPConn 继续接受数据
	select {
	case c.rxqueue <- b:
	default:
		c.rxDrop += int64(len(b.Bytes()))
		Release(b)
	}
}
