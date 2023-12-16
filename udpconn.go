package udpx

import (
	"errors"
	"log"
	"net"
	"sync"
	"time"

	"golang.org/x/net/ipv4"
)

var ErrConnClosed = errors.New("Conn Closed")

type UDPConn struct {
	mux    sync.Mutex
	ln     *Listener
	client bool //true表示lconn is conneted(绑定了目的地址), 即可以直接用Write，不需要WriteTo
	lconn  *net.UDPConn
	pc     *ipv4.PacketConn
	raddr  *net.UDPAddr
	// rms     []ipv4.Message
	// wms     []ipv4.Message
	// batch   bool

	//如果上层能保证一次性读完MyBuffer的内容，可以设置此为true.如果上层用bufio来读,一般能一次性读完
	//udp基于报文收发的, 系统默认就是要一次性读完的一个报文，不读完，内核就丢弃这个报文剩下的那部分，下次再读也读不到
	//设置此为true后，又一次性没读完，返回错误shortReadErr
	oneshotRead bool

	undrainedBufferMux  sync.Mutex
	lastUndrainedBuffer MyBuffer //在不要求一次性MyBuffer的内容的时候，没读完的就放在lastUndrainedBuffer 里

	txqueue     chan MyBuffer
	txqueuelen  int
	rxqueue     chan MyBuffer
	rxqueueB    chan []byte
	rxhandler   func([]byte)
	rxqueuelen  int
	rxDrop      int64
	readBatchs  int //表示是否需要单独为此conn 后台起goroutine来批量读
	writeBatchs int //表示是否需要单独为此conn 后台起goroutine来批量写
	maxBufSize  int

	readTimer  *time.Timer
	writeTimer *time.Timer

	err    error
	closed bool
	dead   chan struct{}
}

type UDPConnOpt func(*UDPConn)

func WithRxQueueLen(n int) UDPConnOpt {
	return func(u *UDPConn) {
		u.rxqueuelen = n
	}
}

func WithTxQueueLen(n int) UDPConnOpt {
	return func(u *UDPConn) {
		u.txqueuelen = n
	}
}

func WithBatchs(n int) UDPConnOpt {
	return func(u *UDPConn) {
		u.readBatchs = n
		u.writeBatchs = n
	}
}

func WithReadBatchs(n int) UDPConnOpt {
	return func(u *UDPConn) {
		u.readBatchs = n
	}
}
func WithWriteBatchs(n int) UDPConnOpt {
	return func(u *UDPConn) {
		u.writeBatchs = n
	}
}

func WithMaxPacketSize(n int) UDPConnOpt {
	return func(u *UDPConn) {
		u.maxBufSize = n
	}
}

func WithRxHandler(h func([]byte)) UDPConnOpt {
	return func(u *UDPConn) {
		u.rxhandler = h
	}
}

func WithOneshotRead(b bool) UDPConnOpt {
	return func(u *UDPConn) {
		u.oneshotRead = b
	}
}

func NewUDPConn(ln *Listener, lconn *net.UDPConn, raddr *net.UDPAddr, opts ...UDPConnOpt) *UDPConn {
	uc := &UDPConn{ln: ln, lconn: lconn, raddr: raddr, dead: make(chan struct{}, 1),
		rxqueuelen:  256,
		txqueuelen:  256,
		readBatchs:  defaultBatchs,
		writeBatchs: defaultBatchs,
		maxBufSize:  defaultMaxPacketSize,
		oneshotRead: true, //默认为true, udp 就应该是oneshotRead
	}
	uc.rxhandler = uc.handlePacket
	for _, opt := range opts {
		opt(uc)
	}
	uc.rxqueue = make(chan MyBuffer, uc.rxqueuelen)
	uc.rxqueueB = make(chan []byte, uc.rxqueuelen)
	uc.pc = ipv4.NewPacketConn(lconn)

	if uc.ln == nil {
		//client dial
		uc.client = true
		if uc.readBatchs > 0 {
			//go uc.ReadBatchLoop(uc.rxhandler)
			go uc.readBatchLoopv2()
		}
		if uc.writeBatchs > 0 {
			//后台起一个goroutine 负责批量写，上层直接write 就行。
			uc.txqueue = make(chan MyBuffer, uc.txqueuelen)
			go uc.writeBatchLoop()
		}
	}
	return uc
}

func (c *UDPConn) PutRxQueue(data []byte) {
	//非阻塞模式,避免某个UDPConn 的数据没有被处理而阻塞了listener 继续接受数据
	select {
	case c.rxqueueB <- data:
	default:
	}
}

func (c *UDPConn) IsClosed() bool {
	c.mux.Lock()
	defer c.mux.Unlock()
	return c.closed
}

func (c *UDPConn) Close() error {
	c.mux.Lock()
	if c.closed == true {
		c.mux.Unlock()
		return nil
	}
	c.closed = true
	c.mux.Unlock()

	if c.readTimer != nil {
		c.readTimer.Stop()
		c.readTimer = nil
	}
	if c.writeTimer != nil {
		c.writeTimer.Stop()
		c.writeTimer = nil
	}

	close(c.dead)
	if c.txqueue != nil {
		close(c.txqueue)
	}

	if c.ln != nil {
		if key, ok := udpAddrTrans(c.raddr); ok {
			c.ln.deleteConn(key)
		}
	}
	if c.client && c.lconn != nil {
		c.lconn.Close()
	}
	log.Printf("client:%v, %s<->%s, close over\n", c.client, c.LocalAddr().String(), c.RemoteAddr().String())
	return nil
}

func (c *UDPConn) LocalAddr() net.Addr {
	return c.lconn.LocalAddr()
}
func (c *UDPConn) RemoteAddr() net.Addr {
	return c.raddr
}

// 内核copy 一次数据到MyBuffer, 这里也会发生一次copy 到业务层。
// 如果业务层用了bufio, 这里这次copy是copy 到 bufio 的buf 里，再等待业务层copy
// 也就是三次copy 操作。比正常的操作多一次copy
// var oneShot = true
var shortReadErr = errors.New("short Read error, Read(buf) should parameters buf len is small than udp packet")

func (c *UDPConn) Read(buf []byte) (n int, err error) {
	//客户端读模式，又不启用batch, 就一个个读
	if c.client && c.readBatchs == 0 {
		return c.lconn.Read(buf)
	}

	//这里有两种处理，
	//1: oneshot, 要求一次性读完MyBuffer 的内容，一次未读完就报错。(如果业务层使用了bufio的话，一般都能一次性读完,所以使用oneShot)
	//2. 可以不要求一次性读完，没有读完的下次再读, 这样应用层合理的方式就是只有一个线程在调用Read, 但是为了支持多线程读，这里只能加锁。
	//if !oneShot {
	if !c.oneshotRead {
		//如果MyBuffer 的内容支持可以多次读，那么就只允许同时只有一个线程在执行读操作
		//需要用锁保证这个过程是串行的。否则多个读线程都把没读完的MyBuffer 存储在c.lastUndrainedBuffer, 这就出问题了。
		c.undrainedBufferMux.Lock()
		defer c.undrainedBufferMux.Unlock()

		if c.lastUndrainedBuffer != nil {
			n, err = c.lastUndrainedBuffer.Read(buf)
			if len(c.lastUndrainedBuffer.Bytes()) == 0 {
				//已经读完，就释放并重置
				Release(c.lastUndrainedBuffer)
				c.lastUndrainedBuffer = nil
			}
			return
		}
	}

	//1.客户端读模式, 启用了batch读(说明后台有任务负责批量读), 这里只需从队列里读
	//2.服务端模式, 不管是否批量读，都是由listen socket去完成读，UDPConn只需从队列里读
	select {
	case b := <-c.rxqueueB: //[]byte rxqueue
		n = copy(buf, b)
		return
	case b := <-c.rxqueue: //MyBuffer rxqueue
		//这里有两种处理，1: 要求一次性读完MyBuffer 的内容，一次未读完就报错。2. 可以不要求一次性读完，没有读完的下次再读
		if c.oneshotRead {
			n, err = b.Read(buf)
			Release(b)
			if err == nil && len(b.Bytes()) > 0 { //要求一次性读完MyBuffer 的内容，但是没读完，返回shortReadErr
				err = shortReadErr
			}
			return
		} else {
			//支持MyBuffer多次读的情况
			//check, here c.lastUndrainedBuffer must be nil
			if c.lastUndrainedBuffer != nil {
				panic("c.lastUndrainedBuffer != nil")
			}
			n, err = b.Read(buf)
			if len(b.Bytes()) == 0 {
				Release(b) //已经读完了
			} else {
				//未读完b 的内容，把b暂存起来
				c.lastUndrainedBuffer = b //直接设置，因为有c.undrainedBufferMux 锁的保护。
			}
			return
		}

	case <-c.dead:
		if c.err != nil {
			return 0, c.err
		}
		return 0, ErrConnClosed
	}
}

func (c *UDPConn) Write(b []byte) (n int, err error) {
	//client conn
	if c.client {
		if c.writeBatchs > 0 {
			return c.WriteWithBatch(b)
		}
		return c.lconn.Write(b)
	}

	//the conn that accepted by listener
	//由listener accept产生的UDPConn, 发送前判断是否是关闭状态. dial 产生UDPConn，如果已经关闭，底层socket 会报错返回，不需要判断
	if c.closed {
		return 0, ErrConnClosed
	}
	if c.ln.WriteBatchAble() {
		return c.WriteWithBatch(b)
	}
	return c.lconn.WriteTo(b, c.raddr)
}

func (c *UDPConn) writeBatchLoop() {
	defer log.Printf("client %v, writeBatchLoop quit", c.pc.LocalAddr())
	bw, _ := NewPCBioWriter(c.pc, c.writeBatchs)
	bw.WriteBatchLoop(c.txqueue)
}

// 返回的error 应该实现net.Error temporary(), 这样上层Write可以认为Eagain,再次调用Write
func (c *UDPConn) PutTxQueue(b MyBuffer) error {
	select {
	case c.txqueue <- b:
	default:
		Release(b)
		return ErrTxQueueFull
	}
	return nil
}
