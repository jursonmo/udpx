package udpx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	pkgerr "github.com/pkg/errors"
	"golang.org/x/net/ipv4"
)

var ErrConnClosed = errors.New("conn closed")

const (
	tokenSize = 4
)

var fixedToken [tokenSize]byte = [tokenSize]byte{0x01, 0x02, 0x03, 0x04}

type UDPConn struct {
	mux    sync.Mutex
	ln     *Listener
	logger Logger
	client bool //true表示lconn is conneted(绑定了目的地址), 即可以直接用Write，不需要WriteTo
	//standalone true表示是独立的udpconn, 即自己的lconn负责收发数据等任务, 比如client dial 生成的UDPConn, 它就是独立的udpconn, standalone 为true, 就不需要listener 来帮忙收发。
	// 一般listener产生的UDPConn, 它们的收发工作都是由listener lconn 负责的，UDPConn write时只是把数据放到 ln txqueue 里而已。
	//启用IP_PKTINFO后，listener产生的UDPConn会绑定源和目的地址，成为独立的udpconn, 就可以独立收发数据，像client UDPConn 一样
	//所以收发操作时，需要判断standalone，而不是判断c.client 或者 c.ln
	standalone bool
	needCheck  int //ln 产生独立UDPConn 在connect 时, socket 已经缓存的数据字节长度, 这些数据需要检查地址是否正确。
	token      []byte
	authData   []byte
	lconn      *net.UDPConn
	pc         *ipv4.PacketConn
	raddr      *net.UDPAddr

	//如果上层能保证一次性读完MyBuffer的内容，可以设置此为true.如果上层用bufio来读,一般能一次性读完
	//udp基于报文收发的, 系统默认就是要一次性读完的一个报文，不读完，内核就丢弃这个报文剩下的那部分，下次再读也读不到
	//设置此为true后，又一次性没读完，返回错误shortReadErr
	oneshotRead bool //默认为true, udp 就应该是oneshotRead, 即业务层传进来的buff 必须足够大，能一次性读完一个udp报文

	undrainedBufferMux  sync.Mutex
	lastUndrainedBuffer MyBuffer //在不要求一次性MyBuffer的内容的时候，没读完的就放在lastUndrainedBuffer 里

	txBlocked      bool //发送时，是否会阻塞, 默认为true, 即阻塞
	txqueue        chan MyBuffer
	txqueuelen     int
	txPackets      int64
	txDropPkts     int64
	txDropBytes    int64
	txDataPkts     int64
	requestDataSeq bool
	dataSeqEnabled bool
	txSeq          uint64
	rxDataPkts     int64
	peerStats      ConnStats
	peerStatsMu    sync.RWMutex
	rxqueue        chan MyBuffer
	rxqueueB       chan []byte
	rxhandler      func([]byte)
	rxqueuelen     int
	rxPackets      int64
	rxDropPkts     int64 //rxDropPkts虽然用了atomic.AddInt64 来增加,但只是偶发增长, 伪共享对周边字段的读写影响不大。
	rxDropBytes    int64

	rxSeqInited     uint32
	rxNextSeq       uint64
	rxGapPkts       uint64
	rxLatePkts      uint64
	seqStatsStarted uint32

	readBatchs  int //表示是否需要单独为此conn 后台起goroutine来批量读
	writeBatchs int //表示是否需要单独为此conn 后台起goroutine来批量写
	maxBufSize  int

	readTimer  *time.Timer
	writeTimer *time.Timer

	err    error
	closed bool
	dead   chan struct{}

	//检查是否长期没有收到数据, 死socket,避免上层协议忘记关闭socket 导致大量死socket残存
	//linux 内核对tcp 有keepalive 保证
	check checkTimeout
}
type checkTimeout struct {
	lastRxPkts     int64
	lastRxDropPkts int64
	lastAliveAt    time.Time
	timeoutCount   int
}

type UDPConnOpt func(*UDPConn)

func WithUCLogger(logger Logger) UDPConnOpt {
	return func(u *UDPConn) {
		u.logger = logger
	}
}

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

func WithTxBlocked(b bool) UDPConnOpt {
	return func(u *UDPConn) {
		u.txBlocked = b
	}
}

func WithAuthData(data []byte) UDPConnOpt {
	return func(u *UDPConn) {
		u.authData = cloneBytes(data)
	}
}

func WithDataSeqRequest(b bool) UDPConnOpt {
	return func(u *UDPConn) {
		u.requestDataSeq = b
	}
}

func withDataSeqEnabled(b bool) UDPConnOpt {
	return func(u *UDPConn) {
		u.dataSeqEnabled = b
	}
}

// listener accept 得到 udpc conn 后, 不想跟listener 的txBlocked 属性一样，可以设置此接口来设置发送时是否可以阻塞。默认是true,是阻塞的。
func (uc *UDPConn) SetTxBlocked(b bool) {
	uc.txBlocked = b
}

func NewUDPConn(ln *Listener, lconn *net.UDPConn, standalone bool, raddr *net.UDPAddr, opts ...UDPConnOpt) *UDPConn {
	uc := &UDPConn{ln: ln, lconn: lconn, raddr: raddr, dead: make(chan struct{}, 1), txBlocked: txqueueBlocked,
		rxqueuelen:     1024, //接收的队列可以适当大一点, 避免突发流量丢包, 特别是ln 批量读数据后，put 到指定UDPConn的rxqueue 时是非阻塞的。
		txqueuelen:     512,
		readBatchs:     defaultBatchs,
		writeBatchs:    defaultBatchs,
		maxBufSize:     defaultMaxPacketSize,
		oneshotRead:    true, //默认为true, udp 就应该是oneshotRead
		standalone:     standalone,
		requestDataSeq: ln == nil && traceClientDataSeq, //主动发起的client 由traceClientDataSeq来设置，server 由traceServerDataSeq 来设置，但最终由client hello报文决定是否开启seq trace
		logger:         gLogger,
	}
	uc.rxhandler = uc.handlePacket //原始非batch模式: readLoop()-->handlePacket()-->rxhandler
	for _, opt := range opts {
		opt(uc)
	}
	InitPool(uc.maxBufSize, DefaultPoolStatEnable)
	uc.rxqueue = make(chan MyBuffer, uc.rxqueuelen)
	uc.rxqueueB = make(chan []byte, uc.rxqueuelen)
	uc.pc = ipv4.NewPacketConn(lconn)

	if uc.ln == nil {
		//client dial
		uc.client = true
		//init token
		//uc.token = fixedToken
		uc.token = GenToken()
	} else {
		//server accept's UDPConn need to alloc space for saving token
		uc.token = make([]byte, tokenSize)
	}

	uc.logger.Infof("new UDPConn:%v\n", uc)
	return uc
}

// 握手报文使用 frameTypeHello/frameTypeHelloAck，避免控制报文和业务报文混淆。
func (c *UDPConn) handshake(_ context.Context) error {
	hello, err := encodeHelloFrame(c.token[:], c.authData, true, c.requestDataSeq)
	if err != nil {
		return err
	}
	_, err = c.lconn.Write(hello)
	if err != nil {
		gLogger.Errorf("send token err:%v", err)
		return err
	}
	buf := make([]byte, c.maxBufSize)
	c.lconn.SetDeadline(time.Now().Add(time.Second * 2))
	defer c.lconn.SetDeadline(time.Time{})
	//_, raddr, err := c.lconn.ReadFrom(buf)
	n, err := c.lconn.Read(buf)
	if err != nil {
		if e, ok := err.(net.Error); ok && e.Temporary() {
			//todo
		}
		return err
	}

	ack, ok := decodeHelloAckFrame(buf[:n])
	if ok && bytes.Equal(ack.Token, c.token[:]) {
		c.setDataSeqEnabled(ack.DataSeqEnabled)
		return nil
	}
	return fmt.Errorf("token not match")
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
	if c.closed {
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
	if q := c.txqueue; q != nil {
		//close(c.txqueue)// 直接关闭，PutTxQueue() 写入一个closed channel 从而panic
		c.txqueue = nil //置空, 再关闭原来channel, 避免 PutTxQueue()时panic
		close(q)
	}

	if c.ln != nil {
		if key, ok := udpAddrTrans(c.raddr); ok {
			c.ln.deleteConn(key)
		}
	}
	if c.standalone && c.lconn != nil {
		c.lconn.Close()
	}
	c.logger.Errorf("udpx client:%v, %s->%s, close over\n", c.client, c.LocalAddr().String(), c.RemoteAddr().String())
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
var ErrShortRead = errors.New("short Read error, Read(buf) should parameters buf len is small than udp packet")

func (c *UDPConn) Read(buf []byte) (n int, err error) {
	//客户端读模式(应该判断是否是独立收发的)，又不启用batch, 就一个个读
	if /*c.client*/ c.standalone && c.readBatchs == 0 {
		raw := make([]byte, c.maxBufSize)
		for {
			rn, err := c.lconn.Read(raw)
			if err != nil {
				return 0, err
			}
			_, typ, payload, ok := decodeFrame(raw[:rn])
			if !ok {
				continue
			}
			switch typ {
			case frameTypeData:
				n = copy(buf, payload)
				if n < len(payload) && c.oneshotRead {
					return n, fmt.Errorf("user_buf_len:%d, have copyed:%d, remain:%d, %w", len(buf), n, len(payload)-n, ErrShortRead)
				}
				c.rxPackets++
				c.rxDataPkts++
				return n, nil
			case frameTypeDataSeq:
				payload, ok := c.decodeDataSeqPayload(payload)
				if !ok {
					continue
				}
				n = copy(buf, payload)
				if n < len(payload) && c.oneshotRead {
					return n, fmt.Errorf("user_buf_len:%d, have copyed:%d, remain:%d, %w", len(buf), n, len(payload)-n, ErrShortRead)
				}
				c.rxPackets++
				c.rxDataPkts++
				return n, nil
			case frameTypeHello:
				hello, ok := decodeHelloPayload(payload)
				if c.ln != nil && ok && bytes.Equal(hello.Token, c.token[:]) {
					ack, err := encodeHelloAckFrame(c.token[:], SessionPolicy{EnableDataSeq: c.dataSeqEnabled})
					if err != nil {
						return 0, err
					}
					if _, err := c.writeRaw(ack); err != nil {
						return 0, err
					}
				}
			case frameTypePing:
				if _, err := c.writeControlFrame(frameTypePong, payload); err != nil {
					return 0, err
				}
			case frameTypeClose:
				c.Close()
				return 0, ErrConnClosed
			case frameTypeStats:
				if stats, err := decodeStatsPayload(payload); err == nil {
					c.setPeerStats(stats)
				}
			}
		}
	}
	user_buf_len := len(buf)
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
	//3.服务端模式，但是生成的UDPConn 是独立模式, 独立模式下自己有单独的任务批量读取，那么这里就只需从队列里读, 跟1一样。
	select {
	case b, ok := <-c.rxqueueB: //[]byte rxqueue
		if !ok {
			return 0, errors.New("rxqueueB closed")
		}
		n = copy(buf, b)
		c.rxPackets++
		c.rxDataPkts++
		return
	case b, ok := <-c.rxqueue: //MyBuffer rxqueue
		if !ok {
			return 0, errors.New("rxqueue closed")
		}
		//这里有两种处理，1: 要求一次性读完MyBuffer 的内容，一次未读完就报错。2. 可以不要求一次性读完，没有读完的下次再读
		if c.oneshotRead {
			n, err = b.Read(buf)
			remain := len(b.Bytes())
			if err == nil && remain > 0 { //要求一次性读完MyBuffer 的内容，但是没读完，返回ErrShortRead
				err = fmt.Errorf("user_buf_len:%d, have copyed:%d, remain:%d, %w", user_buf_len, n, remain, ErrShortRead)
			}
			Release(b) //fixbug:放在这里释放MyBuffer, 不能放在读取len(b.Bytes())前。
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
func (c *UDPConn) WriteTo(b []byte, addr net.Addr) (n int, err error) {
	fb, err := c.newDataFrameBuffer(b)
	if err != nil {
		return 0, pkgerr.WithMessage(err, "new data frame buffer failed")
	}
	_, err = c.lconn.WriteTo(fb.Bytes(), addr)
	Release(fb)
	if err != nil {
		return 0, err
	}
	c.txDataPkts++
	return len(b), nil
}
func (c *UDPConn) Write(b []byte) (n int, err error) {
	fb, err := c.newDataFrameBuffer(b)
	if err != nil {
		return 0, pkgerr.WithMessage(err, "new data frame buffer failed")
	}
	_, err = c.writeFrameBuffer(fb)
	if err != nil {
		return 0, err
	}
	c.txDataPkts++
	return len(b), nil
}

func (c *UDPConn) writeControlFrame(typ byte, payload []byte) (n int, err error) {
	return c.writeRaw(encodeFrame(typ, payload))
}

func (c *UDPConn) writeFrameBuffer(b MyBuffer) (n int, err error) {
	if c.standalone {
		if c.writeBatchs > 0 {
			n = len(b.Bytes())
			err = c.PutTxQueue(b, c.txBlocked)
			if err != nil {
				if err != ErrTxQueueFull {
					Release(b)
				}
				return 0, err
			}
			return n, nil
		}

		// 普通非batch模式, 直接发送
		n, err = c.lconn.Write(b.Bytes())
		Release(b)
		if err != nil {
			atomic.AddInt64(&c.txDropPkts, 1)
		} else {
			c.txPackets++
		}
		return
	}

	//the conn that accepted by listener
	//由listener accept产生的UDPConn, 发送前判断是否是关闭状态. dial 产生UDPConn，如果已经关闭，底层socket 会报错返回，不需要判断
	if c.closed {
		Release(b)
		return 0, ErrConnClosed
	}
	if c.ln.WriteBatchAble() {
		b.SetAddr(c.raddr)
		n = len(b.Bytes())
		err = c.ln.PutTxQueue(b, c.txBlocked)
		if err != nil {
			if err != ErrTxQueueFull {
				Release(b)
			}
			atomic.AddInt64(&c.txDropPkts, 1)
			return 0, err
		}
		c.txPackets++
		return n, nil
	}
	n, err = c.lconn.WriteTo(b.Bytes(), c.raddr)
	Release(b)
	if err != nil {
		atomic.AddInt64(&c.txDropPkts, 1)
		atomic.AddInt64(&c.ln.txDropPkts, 1)
	} else {
		c.txPackets++
		c.ln.txPackets++
	}
	return
}

func (c *UDPConn) writeRaw(b []byte) (n int, err error) {
	//client conn, 应该是判断是否独立收发的
	if /*c.client*/ c.standalone {
		if c.writeBatchs > 0 {
			return c.writeRawWithBatch(b)
		}
		n, err = c.lconn.Write(b)
		if err != nil {
			atomic.AddInt64(&c.txDropPkts, 1)
		} else {
			c.txPackets++
		}
		return
	}

	//the conn that accepted by listener
	//由listener accept产生的UDPConn, 发送前判断是否是关闭状态. dial 产生UDPConn，如果已经关闭，底层socket 会报错返回，不需要判断
	if c.closed {
		return 0, ErrConnClosed
	}
	if c.ln.WriteBatchAble() {
		return c.writeRawWithBatch(b)
	}
	n, err = c.lconn.WriteTo(b, c.raddr)
	if err != nil {
		atomic.AddInt64(&c.txDropPkts, 1)
		atomic.AddInt64(&c.ln.txDropPkts, 1)
	} else {
		c.txPackets++
		c.ln.txPackets++
	}
	return
}

func (c *UDPConn) LocalStats() ConnStats {
	return ConnStats{
		TxPackets:   uint64(c.txDataPkts),
		RxPackets:   uint64(c.rxDataPkts),
		RxDropPkts:  uint64(atomic.LoadInt64(&c.rxDropPkts)),
		TxDropPkts:  uint64(atomic.LoadInt64(&c.txDropPkts)),
		TimestampMs: nowUnixMilli(),
	}
}

func (c *UDPConn) PeerStats() ConnStats {
	c.peerStatsMu.RLock()
	defer c.peerStatsMu.RUnlock()
	return c.peerStats
}

func (c *UDPConn) SendStats() error {
	return nil //暂时不发送stats
	// _, err := c.writeControlFrame(frameTypeStats, encodeStatsPayload(c.LocalStats()))
	// return err
}

func (c *UDPConn) setPeerStats(s ConnStats) {
	c.peerStatsMu.Lock()
	c.peerStats = s
	c.peerStatsMu.Unlock()
}

func (c *UDPConn) writeBatchLoop() {
	defer c.logger.Errorf("client %v, writeBatchLoop quit", c.pc.LocalAddr())

	bw, _ := NewPCBioWriter(c.pc, c.writeBatchs)
	if c.txqueue == nil {
		c.txqueue = make(chan MyBuffer, c.txqueuelen)
	}
	bw.WriteBatchLoop(c.txqueue)
}

// 返回的error 应该实现net.Error temporary(), 这样上层Write可以认为Eagain,再次调用Write
func (c *UDPConn) PutTxQueue(b MyBuffer, blocked bool) error {
	if c.closed {
		return ErrConnClosed
	}

	if blocked {
		c.txqueue <- b
		c.txPackets++ //统计发送的包数,但是不是特别严谨, 因为这里不代表已经发送出去了
		return nil
	}

	//non-blocked
	select {
	case c.txqueue <- b:
		c.txPackets++ //统计发送的包数,但是不是很严谨,因为这不能代表已经发送出去了。
	default:
		atomic.AddInt64(&c.txDropPkts, 1)
		//c.txDropBytes += int64(len(b.Bytes()))
		Release(b)
		return ErrTxQueueFull
	}
	return nil
}

func (c *UDPConn) String() string {
	var lnString = "ln is nil"
	if c.ln != nil {
		lnString = fmt.Sprintf("listener id:%v", c.ln.id)
	}
	return fmt.Sprintf("isClient:%v, standalone:%v, %s, laddr:%v, raddr:%v, oneshotRead:%v, dataSeq:%v, rwbatch(%d,%d), rxtxqueue(%d,%d), rx:%d, rxDrop:%d(%d), tx:%d, txDrop:%d(%d), txBlocked:%v",
		c.client, c.standalone, lnString, c.lconn.LocalAddr(), c.raddr, c.oneshotRead, c.dataSeqEnabled, c.readBatchs, c.writeBatchs, c.rxqueuelen, c.txqueuelen,
		c.rxPackets, atomic.LoadInt64(&c.rxDropPkts), c.rxDropBytes, c.txPackets, atomic.LoadInt64(&c.txDropPkts), c.txDropBytes, c.txBlocked)
}

// 重写对象MarshalJSON方法，返回的内容要符合{"key": "value"}的json Marshal 后的格式,
// 不然提示json: error calling MarshalJSON for type *udpx.UDPConn: invalid character '.' after object key:value pair
func (c *UDPConn) MarshalJSON() ([]byte, error) {
	return []byte(fmt.Sprintf(`{"isClient": %v,"raddr": "%s"}`, c.client, c.raddr.String())), nil
}
