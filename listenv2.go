package udpx

import (
	"context"
	"fmt"
	"net"
	"sync/atomic"
	"syscall"
	"unsafe"

	pkgerr "github.com/pkg/errors"
	"golang.org/x/net/ipv4"
	"golang.org/x/sys/unix"
)

// readBatchLoop ->handlePacket:分配新的内存对象,并且copy 一次
// 为了复用对象,同时减少一次内存copy, 实现 Listener readBatchLoopv2 -> handleBuffer
func (l *Listener) readBatchLoopv2() error {
	var err error
	InitPool(l.maxPacketSize, DefaultPoolStatEnable)
	l.logger.Infof("%v, started with readLoopv2(use MyBuffer)....\n", l)
	defer func() { l.logger.Errorf("%v, readLoopv2(use MyBuffer) quit, err:%v\n", l, err) }()

	// if IP_PKTINFO_ENABLE {
	// 	for {
	// 		if err := l.getUDPConnByOOB(); err != nil {
	// 			l.logger.Errorf("getUDPConnByOOB err:%v\n", err)
	// 			return err
	// 		}
	// 	}
	// }

	rms := make([]ipv4.Message, l.batchs)
	for i := 0; i < l.batchs; i++ {
		rms[i].Buffers = make([][]byte, 1)               //提前分配好rms[i].Buffers[0], 避免接收数据时每次都分配,导致产生很多小对象
		rms[i].OOB = make([]byte, syscall.CmsgSpace(40)) //如果开启了IP_PKTINFO,则需要设置OOB
	}
	buffers := make([]MyBuffer, l.batchs)
	n := len(rms)
	laddr := l.lconn.LocalAddr().(*net.UDPAddr)
	dstAddr := (*net.UDPAddr)(nil)
	for {
		for i := 0; i < n; i++ {
			b := GetMyBuffer(0)
			buffers[i] = b
			//rms[i] = ipv4.Message{Buffers: [][]byte{b.Buffer()}, Addr: nil} //这种方式赋值Buffers,会产生很多小对象，造成频繁gc
			rms[i].Buffers[0] = b.Buffer()
			rms[i].Addr = nil
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
			if rms[i].Addr == nil {
				panic("rms[i].Addr == nil")
			}

			dstAddr = nil
			//如果开启了IP_PKTINFO,则需要从OOB中获取目的地址
			//如果获取成功，并且绑定源和目的地址成功而产生独立的UDPConn, 后面该client的数据都要从这个UDPConn lconn读取, 那么listener 这里只会接受到新连接的数据
			//不会是established udp 的数据, 这样这里listener lconn的读取数据压力其实是很小的, 因为listener lconn的数据报文数量很少。
			//由于数据少, 即使listener lconn开启了IP_PKTINFO特性后, 也不会因为数据报文里带有out-of-band数据而影响性能。
			if IP_PKTINFO_ENABLE && rms[i].NN > 0 {
				dstAddr, err = parseDstAddrFromOOB(rms[i].OOB[:rms[i].NN], laddr.Port)
				if err != nil {
					l.logger.Errorf("parseDstAddrFromOOB err:%v\n", err)
					continue
				}
			}

			l.handleBuffer(dstAddr, rms[i].Addr, buffers[i])
		}
	}
}

func (l *Listener) handleBuffer(dstAddr *net.UDPAddr, addr net.Addr, b MyBuffer) {
	if dstAddr != nil {
		l.CreateUDPConnByDstAddr(dstAddr, addr, b.Bytes())
		return
	}

	//开启了IP_PKTINFO_ENABLE后, 数据不会走到这里. 数据都被CreateUDPConnByDstAddr处理，或者被独立的UDPConn处理了。
	if IP_PKTINFO_ENABLE {
		panic("if IP_PKTINFO_ENABLE, data should be handled by standalone UDPConn, can't be here")
	}

	//只统计非ctrl数据的包数
	if uc, isCtrlData := l.getUDPConn(addr, b.Bytes()); uc != nil && !isCtrlData {
		if err := uc.PutRxQueue2(b); err != nil {
			l.rxDropPkts++
			if l.rxDropPkts&127 == 0 {
				//iperf跑流量测试时,iperf显示丢包很多, 但服务端这里没有打印, 说明不是rxqueue太小的问题。
				l.logger.Warnf("notice listener:%v, rxDropPkts:%d\n", l, l.rxDropPkts)
			}
		} else {
			l.rxPackets++
		}
	}
}
func (l *Listener) CreateUDPConnByDstAddr(laddr *net.UDPAddr, addr net.Addr, data []byte) {
	//如何获取到了数据报文的目的地址，可以直接创建新的UDPConn
	raddr := addr.(*net.UDPAddr)
	key, ok := udpAddrTrans(raddr)
	if !ok {
		return
	}
	//由于listener lconn是批量读取的, 这批里可能有多个报文都是来自同一个新的client, 第一个报文会创建UDPConn, 但是该client的第二个报文就不能再创建了
	//即, 如果raddr 的对应的UDPConn已经创建，那么这里就不需要再创建了，直接把数据put 到对应的UDPConn的rxqueue 里。
	v, ok := l.clients.Load(key)
	if ok {
		uc := v.(*UDPConn)
		b := GetMyBuffer(0)
		n, err := b.Write(data)
		if err != nil {
			l.logger.Errorf("%v, MyBuffer Write data err:%v\n", l, err)
			return
		}
		if n != len(data) {
			panic(fmt.Errorf("n != len(data), n:%d, len(data):%d", n, len(data)))
		}

		if err := uc.PutRxQueue2(b); err != nil {
			l.rxDropPkts++
			if l.rxDropPkts&127 == 0 {
				//iperf跑流量测试时, iperf显示丢包很多, 但服务端这里没有打印, 说明不是rxqueue太小的问题。
				l.logger.Warnf("notice listener:%v, rxDropPkts:%d\n", l, l.rxDropPkts)
			}
		} else {
			l.rxPackets++
		}
		return
	}

	//lconn, err := net.DialUDP(l.lconn.LocalAddr().Network(), dstAddr, raddr)
	// if err != nil {
	// 	l.logger.Errorf("create new udp, DialUDP err:%v\n", err)
	// 	//因为listener已经侦听了12347端口，所以不能再bind 12347端口?为啥，按道理应该允许绑定的，这样内核收到数据后，先根据五元组找到对应udp socket,找不到再交给listener的socket的
	// 	//这里打印错误信息:create new udp, DialUDP err:dial udp 192.168.x.x:12347-\u003e192.168.x.x:44122: bind: address already in use\n"
	// 	return
	// }

	//新的client数据, 第一个必须是握手报文token
	if len(data) != tokenSize {
		l.logger.Errorf("CreateUDPConnByDstAddr, first data len:%d is not tokenSize:%d, remote:%v", len(data), tokenSize, raddr)
		return
	}
	ok, err := VerifyToken(data)
	if !ok {
		l.logger.Errorf("CreateUDPConnByDstAddr, VerifyToken err:%v, remote:%v", err, raddr)
		return
	}

	l.logger.Infof("CreateUDPConnByDstAddr, laddr=%s://%v, raddr:%v", laddr.Network(), laddr.String(), raddr)
	lconn, err := l.newUDPConnBindAddr(laddr, raddr)
	if err != nil {
		panic(err)
	}
	//查看bind端口的情况: lsof -an -p $pid

	uc := NewUDPConn(l, lconn, true, raddr, WithBatchs(l.batchs), WithMaxPacketSize(l.maxPacketSize), WithOneshotRead(l.oneshotRead), WithTxBlocked(l.txBlocked))
	n := copy(uc.token[:], data)
	if n != tokenSize {
		panic(fmt.Sprintf("%v, token:%v, copy token fail, n:%d, tokenSize:%d", l, uc.token, n, tokenSize))
	}

	//记录当前socket的接收缓冲区的数据字节数，这部分数据是需要检查其地址是否正确的.
	uc.needCheck, err = getUDPSocketLen(uc.lconn)
	if err != nil {
		panic(err)
	}

	//if _, err := uc.lconn.WriteTo(data, addr); err != nil {
	if _, err := uc.lconn.Write(data); err != nil {
		l.logger.Errorf("%v, token:%v, write to addr:%v, err:%v", l, addr, uc.token, addr, err)
		lconn.Close()
		return
	}

	err = setSocketBuf(uc.lconn, 1024*1024) //1M
	if err != nil {
		panic(fmt.Errorf("setSocketBuf failed, err:%v", err))
	}

	if uc.readBatchs > 0 {
		//go uc.ReadBatchLoop(uc.rxhandler)
		go uc.readBatchLoopv2()
	}
	if uc.writeBatchs > 0 {
		//后台起一个goroutine 负责批量写，上层直接write 就行。
		uc.txqueue = make(chan MyBuffer, uc.txqueuelen)
		go uc.writeBatchLoop()
	}

	l.logger.Infof("CreateUDPConnByDstAddr, listener:%v, new conn:%v, token:%v", l, addr, uc.token)
	l.clients.Store(key, uc)
	atomic.AddInt64(&l.clientCount, 1)
	//这里如何阻塞, 会影响后面的处理，但是这个理论上不会阻塞，阻塞说明程序负载很大了
	l.accept <- uc
}

// 从辅助数据中解析目的地址
func parseDstAddrFromOOB(oob []byte, port int) (*net.UDPAddr, error) /*(*net.IPAddr, error)*/ {
	msgs, err := syscall.ParseSocketControlMessage(oob)
	if err != nil {
		return nil, fmt.Errorf("failed to parse control message: %v", err)
	}

	for _, msg := range msgs {
		if msg.Header.Level == syscall.IPPROTO_IP && msg.Header.Type == syscall.IP_PKTINFO {
			info := (*syscall.Inet4Pktinfo)(unsafe.Pointer(&msg.Data[0]))
			ip := net.IPv4(info.Spec_dst[0], info.Spec_dst[1], info.Spec_dst[2], info.Spec_dst[3])
			//return &net.IPAddr{IP: ip}, nil
			return &net.UDPAddr{IP: ip, Port: port}, nil
		}
	}

	return nil, fmt.Errorf("destination address not found in control messages")
}

func (l *Listener) getUDPConnByOOB() error {
	laddr := l.lconn.LocalAddr().(*net.UDPAddr)
	b := GetMyBuffer(0)
	oob := make([]byte, syscall.CmsgSpace(40))
	n, oobn, _, addr, err := l.lconn.ReadMsgUDP(b.Buffer(), oob)
	if err != nil {
		l.logger.Errorf("ReadMsgUDP failed: %v", err)
		return err
	}

	// 解析控制消息获取目的地址（包含IP和端口）
	dstAddr, err := parseDstAddrFromOOB(oob[:oobn], laddr.Port)
	if err != nil {
		l.logger.Errorf("parseDstAddrFromOOB err:%v\n", err)
		panic(err)
	}
	b.Advance(n)
	l.handleBuffer(dstAddr, addr, b)
	return nil
}

func (l *Listener) newUDPConnBindAddr(laddr *net.UDPAddr, raddr *net.UDPAddr) (*net.UDPConn, error) {
	var lc = net.ListenConfig{
		Control: func(network, address string, c syscall.RawConn) error {
			var opErr error
			if err := c.Control(func(fd uintptr) {
				opErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
			}); err != nil {
				panic(err)
			}
			// if err := c.Control(func(fd uintptr) {
			// 	opErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEADDR, 1)
			// }); err != nil {
			// 	panic(err)
			// }

			if err := c.Control(func(fd uintptr) {
				// ip4 := laddr.IP.To4()
				// l.logger.Infof("bind laddr, fd:%d, ip:%v, port:%d", int(fd), ip4, laddr.Port)
				// opErr = unix.Bind(int(fd), &unix.SockaddrInet4{Addr: [4]byte{ip4[0], ip4[1], ip4[2], ip4[3]}, Port: laddr.Port})
				// if opErr != nil {
				// 	l.logger.Errorf("bind laddr, fd:%d, ip:%v, port:%d, err:%v", int(fd), ip4, laddr.Port, opErr)
				// 	panic(opErr)
				// }
				// ip4 := raddr.IP.To4()
				// l.logger.Infof("connect raddr, fd:%d, ip:%v, port:%d", int(fd), ip4, raddr.Port)
				// opErr = unix.Connect(int(fd), &unix.SockaddrInet4{Addr: [4]byte{ip4[0], ip4[1], ip4[2], ip4[3]}, Port: raddr.Port})
				// if opErr != nil {
				// 	l.logger.Errorf("connect raddr, fd:%d, ip:%v, port:%d, err:%v", int(fd), ip4, raddr.Port, opErr)
				// 	panic(opErr)
				// }
			}); err != nil {
				panic(err)
				//return err
			}
			return opErr
		},
	}
	conn, err := lc.ListenPacket(context.Background(), laddr.Network(), laddr.String())
	if err != nil {
		//如果在上面的c.Control()里就调用unix.Connect(), 这里会panic: listen udp 192.168.6.70:12347: bind: invalid argument
		//原因是/usr/local/go/src/net/sock_posix.go func (fd *netFD) listenDatagram()里是先执行Control(), 再bind laddr的
		// 先执行Control() 里的unix.Connect()后，就相当于绑定一个具体的源地址(这个源地址有路由表决定, 源端口随机)，这时再bind laddr 就会失败
		panic(err)
	}
	uc := conn.(*net.UDPConn)
	//uc.Connect(raddr)

	//由于lc的Control()里不能调用unix.Connect(), 只能lc.ListenPacket()侦听后再Connect()
	//TODO: 想侦听再Connect()有个问题，这一瞬间如果有新的数据发送到这个处于侦听的conn，会出现啥异常情况？
	//connect() 之前收到的数据，都还在队列里等着被 recvfrom() 取走，connect() 不会把它们扔掉。
	//connect() 只是给未来到达的数据报设置了一个新的“门卫规则”。
	//所以要检查下数据的源地址是否是connect的地址, 但是每个数据都要检查下，也没必要，只需要检查当前connecth后socket的队列字节长度即可
	//如果不是connect的地址，就会被丢弃, 不能交给上层业务，

	rawconn, err := uc.SyscallConn()
	if err != nil {
		panic(err)
	}
	rawconn.Control(func(fd uintptr) {
		ip4 := raddr.IP.To4()
		l.logger.Infof("bind raddr, fd:%d, ip:%v, port:%d", fd, ip4, raddr.Port)
		err := unix.Connect(int(fd), &unix.SockaddrInet4{Addr: [4]byte{ip4[0], ip4[1], ip4[2], ip4[3]}, Port: raddr.Port})
		if err != nil {
			panic(err)
		}
		// //connect 后，恢复SO_REUSEPORT 为 0, 以免影响udp listener 负载？？但是这样会导致下次调用lc.ListenPacket() 出错bind: address already in use。不知道为啥。
		// err = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 0)
		// if err != nil {
		// 	panic(err)
		// }
	})
	return uc, nil
}
