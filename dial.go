package udpx

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net"

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
	// if c.rxhandler != nil {
	// 	go c.ReadBatchLoop(c.rxhandler)
	// }
	err = c.handshake(ctx)
	if err != nil {
		c.Close()
		return nil, err
	}

	if c.standalone {
		if c.readBatchs > 0 {
			//go uc.ReadBatchLoop(uc.rxhandler)
			InitPool(c.maxBufSize)
			go c.readBatchLoopv2()
		}
		if c.writeBatchs > 0 {
			//后台起一个goroutine 负责批量写，上层直接write 就行。
			c.txqueue = make(chan MyBuffer, c.txqueuelen) //还是跟以前一样提前初始化, 确保发送数据时，txqueue是确定已经初始化好的。避免上层发送数据时丢失
			go c.writeBatchLoop()
		}
	}

	gLogger.Infof("ok, started UDPConn:%v", c)
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
				//服务端产生的独立UDPConn, 所以需要判断数据是否是magic data, 避免client 重传了magic data
				if rms[i].N == magicSize && bytes.Equal(buffers[i].Bytes(), c.magic[:magicSize]) {
					continue
				}

				//只需要检查独立UPConn在bind 和 connect 之间已经缓存到socket的那部分数据
				if checkLen < c.needCheck {
					checkLen += rms[i].N
					//为了避免独立UPConn在bind 和 connect 之间已经有数据到来，所以这里要检查下数据的源IP是否是connect的IP
					if !rms[i].Addr.(*net.UDPAddr).IP.Equal(c.raddr.IP) {
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
	//todo: check control packet or data packet,
	//但是我认为，不应该在这里做控制层相关的业务，因为它只需提供连接的收发操作即可
	//如果需要握手验证和心跳，应该是在业务层做，或者在业务层和底层之间加一层来实现协议格式和控制协议报文

	//非阻塞模式,避免某个UDPConn 的数据没有被处理而阻塞了listener 或者 UDPConn 继续接受数据
	select {
	case c.rxqueue <- b:
		c.rxPackets += 1
	default:
		c.rxDropPkts += 1
		//c.rxDropBytes += int64(len(b.Bytes()))

		//iperf跑流量测试时,iperf显示丢包很多,但服务端这里没有打印, 压力测试了很久才打印一次,所以这里导致丢包的
		if c.rxDropPkts&127 == 0 {
			//panic(fmt.Errorf("notice udpxConn:%v, rxDropPkts:%d\n", c, c.rxDropPkts))
			gLogger.Warnf("notice udpxConn:%v, rxDropPkts:%d\n", c, c.rxDropPkts)
		}
		Release(b)
		return ErrRxQueueFull
	}
	return nil
}
