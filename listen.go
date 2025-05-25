package udpx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	pkgerr "github.com/pkg/errors"
	"golang.org/x/net/ipv4"
	"golang.org/x/sys/unix"
)

const (
	ProMode   = 0 //生产环境
	DebugMode = 1 //debug打印环境,跑性能的时候不能用这种模式。
)

var gMode int

func SetMode(m int) {
	gMode = m
}

var ErrLnClosed = errors.New("udp listener closed")

type LnCfgOptions func(*ListenConfig)

func WithReuseport(b bool) LnCfgOptions {
	return func(lc *ListenConfig) {
		lc.reuseport = true
	}
}

func WithListenerNum(n int) LnCfgOptions {
	return func(lc *ListenConfig) {
		lc.listenerNum = n
	}
}

// 如果不用batchs 读写, 设置成 0
func Batchs(n int) LnCfgOptions {
	return func(lc *ListenConfig) {
		lc.batchs = n
	}
}

func MaxPacketSize(n int) LnCfgOptions {
	return func(lc *ListenConfig) {
		lc.maxPacketSize = n
	}
}

func CfgLogger(l Logger) LnCfgOptions {
	return func(lc *ListenConfig) {
		lc.logger = l
	}
}

type ListenConfig struct {
	network string
	addr    string
	//raddr     string
	reuseport     bool //如果没有指定listenerNum，且reuseport =true,那么就GOPROCESS来作为listenerNum
	listenerNum   int
	batchs        int //one
	maxPacketSize int

	logger Logger
}

type UdpListen struct {
	sync.Mutex
	ctx       context.Context
	logger    Logger
	listeners []*Listener
	laddr     *net.UDPAddr
	accept    chan net.Conn // 收集所有listeners accept 到的连接, 上层可以通过Accept()来获取, 这个队列可以使得大一点，避免阻塞
	dead      chan struct{}
	closed    bool
	cfg       ListenConfig
}

func (l *UdpListen) String() string {
	return fmt.Sprintf("udp leader listener, listeners:%d, local:%s, reuseport:%v", l.cfg.listenerNum, l.Addr(), l.cfg.reuseport)
}

func NewUdpListen(ctx context.Context, network, addr string, opts ...LnCfgOptions) (*UdpListen, error) {
	cfg := ListenConfig{network: network, addr: addr, batchs: defaultBatchs, logger: StdLogger{Logger: log.New(log.Writer(), log.Prefix(), log.Flags())}}
	for _, opt := range opts {
		opt(&cfg)
	}
	err := cfg.Tidy()
	if err != nil {
		return nil, err
	}
	cfg.logger.Infof("ListenConfig:%+v\n", cfg)
	ln := &UdpListen{
		ctx:    ctx,
		cfg:    cfg,
		accept: make(chan net.Conn, 1024),
		dead:   make(chan struct{}, 1),
		logger: cfg.logger,
		//[UdpListen]2024/01/04 18:02:13 INFO: listener, id:1, batchs:8, local:udp://[::]:3333 listenning....
		//logger: StdLogger{Logger: log.New(log.Writer(), "[UdpListen]", log.Flags())},
	}
	err = ln.Start()
	if err != nil {
		return nil, err
	}
	return ln, nil
}

func (ln *UdpListen) Start() error {
	cfg := ln.cfg
	laddr, err := net.ResolveUDPAddr(cfg.network, cfg.addr)
	if err != nil {
		return pkgerr.Wrapf(err, "ResolveUDPAddr %s://%s fail", cfg.network, cfg.addr)
	}
	ln.laddr = laddr
	ln.listeners = make([]*Listener, cfg.listenerNum)
	for i := 0; i < cfg.listenerNum; i++ {
		l, err := NewListener(ln.ctx, cfg.network, cfg.addr,
			WithId(i), WithLnBatchs(cfg.batchs), WithLnMaxPacketSize(cfg.maxPacketSize), WithLogger(ln.logger))
		if err != nil {
			return pkgerr.Wrapf(err, "NewListener %d fail", i)
		}
		ln.listeners[i] = l

	}
	ln.Listen()
	return nil
}

func (cfg *ListenConfig) Tidy() error {
	if cfg.network == "" || cfg.addr == "" {
		return fmt.Errorf("network or addr is empty")
	}

	//如果没有设置listener 的数量, 那么如果开启reuseport,就按cpu的个数来，否则就认为没有开口reuseport，即listner 数量只有一个
	if cfg.listenerNum == 0 {
		if cfg.reuseport {
			cfg.listenerNum = runtime.GOMAXPROCS(0)
		} else {
			cfg.listenerNum = 1
		}
	}

	if cfg.listenerNum <= 0 {
		log.Panicln("invaild, listenerNum <= 0")
	}

	if cfg.maxPacketSize == 0 {
		cfg.maxPacketSize = defaultMaxPacketSize
	}

	return nil
}

func (ln *UdpListen) Listen() {
	go ln.checkExpire()
	for _, l := range ln.listeners {
		if l == nil {
			continue
		}
		go func(l *Listener) {
			ln.logger.Infof("%v listenning....", l)
			for {
				conn, err := l.Accept()
				if err != nil {
					ln.logger.Errorf("%v Accept() err:%s and quit", l, err)
					return
				}
				ln.accept <- conn
			}
		}(l)
	}
}

// 实现 net.Listener 接口 Accept()、Addr() 、Close()
func (l *UdpListen) Accept() (net.Conn, error) {
	for {
		//check if dead first
		select {
		case <-l.dead:
			return nil, ErrLnClosed
		default:
		}

		select {
		case <-l.dead:
			return nil, ErrLnClosed
		case conn, ok := <-l.accept:
			if !ok {
				return nil, ErrLnClosed
			}
			return conn, nil
		}
	}
}

func (l *UdpListen) Addr() net.Addr {
	return l.laddr
}

func (l *UdpListen) Close() error {
	l.Lock()
	if l.closed {
		l.Unlock()
		return ErrLnClosed
	}
	l.closed = true
	l.Unlock()

	close(l.dead)
	close(l.accept)

	for _, listener := range l.listeners {
		if listener == nil {
			continue
		}
		listener.Close()
	}
	return nil
}

type Listener struct {
	sync.Mutex
	id     int
	logger Logger
	lconn  *net.UDPConn
	pc     *ipv4.PacketConn
	mode   int
	//ln      net.Listener
	clients        sync.Map
	expire         time.Duration //client expire ,根据clients的数量
	clientCount    int64
	accept         chan *UDPConn
	txqueue        chan MyBuffer
	writeBatchAble bool // write batch is enable?
	batchs         int
	maxPacketSize  int
	dead           chan struct{}
	closed         bool
}
type ListenerOpt func(*Listener)

func WithId(id int) ListenerOpt {
	return func(l *Listener) {
		l.id = id
	}
}

func WithLnBatchs(n int) ListenerOpt {
	return func(l *Listener) {
		l.batchs = n
	}
}

func WithLnMaxPacketSize(n int) ListenerOpt {
	return func(l *Listener) {
		l.maxPacketSize = n
	}
}

func WithMode(m int) ListenerOpt {
	return func(l *Listener) {
		l.mode = m
	}
}

func WithLogger(log Logger) ListenerOpt {
	return func(l *Listener) {
		l.logger = log
	}
}

func NewListener(ctx context.Context, network, addr string, opts ...ListenerOpt) (*Listener, error) {
	l := &Listener{batchs: defaultBatchs, maxPacketSize: defaultMaxPacketSize, mode: gMode}
	for _, opt := range opts {
		opt(l)
	}
	l.accept = make(chan *UDPConn, 512)

	var lc = net.ListenConfig{
		Control: func(network, address string, c syscall.RawConn) error {
			var opErr error
			if err := c.Control(func(fd uintptr) {
				opErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
			}); err != nil {
				return err
			}
			return opErr
		},
	}
	//conn, err := net.ListenUDP("udp", udpAddress)
	conn, err := lc.ListenPacket(ctx, network, addr)
	if err != nil {
		return nil, pkgerr.WithStack(err)
	}
	l.lconn = conn.(*net.UDPConn)
	l.pc = ipv4.NewPacketConn(conn)

	if l.batchs > 0 {
		l.txqueue = make(chan MyBuffer, 512)
		go l.writeBatchLoop()
		//go l.readBatchLoop()
		go l.readBatchLoopv2() //use buffer pool
	} else {
		//read one packet by one syscall
		go l.readLoop()
	}

	return l, nil
}

func (l *Listener) readBatchLoop() {
	readBatchs := l.batchs
	maxPacketSize := l.maxPacketSize
	rms := make([]ipv4.Message, readBatchs)
	for i := 0; i < len(rms); i++ {
		rms[i] = ipv4.Message{Buffers: [][]byte{make([]byte, maxPacketSize)}}
	}
	l.logger.Infof("listener, id:%d, batchs:%d, maxPacketSize:%d, readLoop....", l.id, l.batchs, l.maxPacketSize)
	for {
		n, err := l.pc.ReadBatch(rms, 0)
		if err != nil {
			l.Close()
			panic(err)
		}
		if l.mode == DebugMode {
			l.logger.Infof("listener id:%d, batch got n:%d, len(ms):%d\n", l.id, n, len(rms))
		}
		if n == 0 {
			continue
		}
		for i := 0; i < n; i++ {
			l.handlePacket(rms[i].Addr, rms[i].Buffers[0][:rms[i].N])
		}
	}
}

func (l *Listener) readLoop() error {
	buf := make([]byte, l.maxPacketSize)
	for {
		rn, ra, err := l.lconn.ReadFromUDP(buf)
		if err != nil {
			l.Close()
			return err
		}
		l.handlePacket(ra, buf[:rn])
	}
}

func (l *Listener) handlePacket(addr net.Addr, data []byte) {
	var uc *UDPConn
	if len(data) == 0 {
		return
	}

	uc, isCtrlData := l.getUDPConn(addr, data)
	if isCtrlData {
		//data 是初始化用的控制数据，不需要处理
		return
	}
	if uc.rxhandler != nil {
		uc.rxhandler(data)
	}
}

func (l *Listener) getUDPConn(addr net.Addr, data []byte) (uc *UDPConn, isCtrlData bool) {
	// go tool pprof -alloc_objects http://192.168.64.5:6061/debug/pprof/heap
	//raddr := addr.String() //net.UDPConn.String() 方法会产生很多小对象, 不如把addr 转化一下
	udpaddr := addr.(*net.UDPAddr)
	key, ok := udpAddrTrans(udpaddr)
	if !ok {
		return
	}

	v, ok := l.clients.Load(key)
	if !ok {
		//new client? check magic
		if len(data) != magicSize {
			return nil, true
		}
		//new udpConn
		uc = NewUDPConn(l, l.lconn, udpaddr, WithBatchs(0), WithMaxPacketSize(l.maxPacketSize))
		uc.magic[0] = data[0]
		uc.magic[1] = data[1]
		uc.magic[2] = data[2]
		uc.magic[3] = data[3]

		if _, err := uc.lconn.WriteTo(data, addr); err != nil {
			l.logger.Errorf("%v, magic:%v, write to addr:%v, err:%v", l, addr, uc.magic, addr, err)
			return nil, true
		}
		l.logger.Infof("%v, new conn:%v, magic:%v", l, addr, uc.magic)
		l.clients.Store(key, uc)
		atomic.AddInt64(&l.clientCount, 1)
		//这里如何阻塞, 会影响后面的处理，但是这个理论上不会阻塞，阻塞说明程序负载很大了
		l.accept <- uc
		return uc, true
	}
	uc = v.(*UDPConn)

	//为了避免client重复发送magic时，服务器误以为是业务数据而网上送, 这里保险点再判断一次, 如果是控制数据，就不需要处理了
	//这样导致的后果就是业务层不能发送跟 magic 一样是数据，否则会被当成是控制数据；TODO: 可以在业务数据上再加一个头部来区分业务数据和控制数据
	if len(data) == magicSize && reflect.DeepEqual(data, uc.magic[:]) {
		return uc, true
	}
	return uc, false
}

func (l *Listener) deleteConn(key AddrKey /*interface{}*/) error {
	l.logger.Errorf("id:%d, del: %s, local:%s, remote: %v", l.id, l.LocalAddr().Network(), l.LocalAddr().String(), key)
	_, exist := l.clients.LoadAndDelete(key)
	if !exist {
		//暂时用panic 来确保业务层对同一个conn 删除两次时，我可以看出来
		log.Panicf("%v try to delete conn key: %v, but key isn't exist", l, key)
		return fmt.Errorf("%v try to delete conn key: %v, but key isn't exist", l, key)
	}
	atomic.AddInt64(&l.clientCount, -1)
	return nil
}

func (l *Listener) LocalAddr() net.Addr {
	return l.lconn.LocalAddr()
}

// 实现 net.Listener 接口 Accept()、Addr() 、Close()
func (l *Listener) Accept() (net.Conn, error) {
	for {
		select {
		case <-l.dead:
			return nil, ErrLnClosed
		case c, ok := <-l.accept:
			if !ok {
				return nil, ErrLnClosed
			}
			return c, nil
		}
	}
}

func (l *Listener) Addr() net.Addr {
	return l.LocalAddr()
}

func (l *Listener) Close() error {
	l.Lock()
	if l.closed {
		return ErrLnClosed
	}
	l.closed = true
	l.Unlock()

	l.logger.Errorf("%v closing....", l)
	defer l.logger.Errorf("%v over", l)
	close(l.dead)
	close(l.accept)
	err := l.lconn.Close()
	if err != nil {
		return err
	}
	//todo:
	//先关闭lconn,再考虑是否要关闭txqueue, 因为UDPConn发送数据先是发给ln的txqueue,再由ln的侦听socket批量发送出去
	//如果这里close(l.txqueue)，然后UDPConn还发送数据ln的txqueue，就会panic
	//即使先关闭lconn也无法保证没有UDPConn发送数据, 所以这里close l.txqueue是有风险的
	//原来的本意是通过关闭l.txqueue，让WriteBatchLoop 退出，但是现在lconn.Close()后，WriteBatchLoop发送数据出错也会退出
	//每个UDPConn都会发生心跳，所以保证 listener的 WriteBatchLoop发送数据出错也会退出
	//所以这里可以不用关闭l.txqueue
	// if l.txqueue != nil {
	// 	close(l.txqueue)
	// }

	return nil
}

func (l *Listener) String() string {
	return fmt.Sprintf("listener, id:%d, batchs:%d, local:%s://%s",
		l.id, l.batchs, l.LocalAddr().Network(), l.LocalAddr().String())
}

func (l *Listener) ListClientConns() []*UDPConn {
	list := make([]*UDPConn, 0, atomic.LoadInt64(&l.clientCount))
	l.clients.Range(func(key, value any) bool {
		uc := value.(*UDPConn)
		list = append(list, uc)
		return true
	})
	return list
}

type ListenerInfo struct {
	ListenerId  int
	ClientCount int64
	Clients     []*UDPConn
}

func (l *Listener) Detail() []byte {
	detail := ListenerInfo{ListenerId: l.id, Clients: l.ListClientConns()}
	d, _ := json.Marshal(&detail)
	return d
}

func (ln *UdpListen) Detail() []byte {
	details := make([]ListenerInfo, 0, len(ln.listeners))
	for _, l := range ln.listeners {
		details = append(details, ListenerInfo{
			ListenerId:  l.id,
			ClientCount: atomic.LoadInt64(&l.clientCount),
			Clients:     l.ListClientConns()})
	}

	ld, err := json.MarshalIndent(details, "", "\t")
	if err != nil {
		ld = []byte(err.Error())
	}
	info := make([]byte, 0, len(ld)+256)
	x := []byte(fmt.Sprintf("%v\n", ln))
	info = append(info, x...)
	info = append(info, ld...)
	return info
}

// 虽然UDPConn是否超时，应该由上层协议来检测，
// 但是我们这里也可以做个兜底,避免上层协议出错，忘记关闭UDPConn,导致udpx残存死UDPConn过多
func (ln *UdpListen) checkExpire() error {
	intv := time.Minute * 3
	t := time.NewTicker(intv)
	defer t.Stop()

	for {
		select {
		case <-ln.dead:
			return nil
		case <-ln.ctx.Done():
			return ln.ctx.Err()
		case <-t.C:
			ln.logger.Infof("---checkExpire---\n")
			for _, l := range ln.listeners {
				ccs := l.ListClientConns()
				l.updateClientExpire(len(ccs))
				for _, c := range ccs {
					if c.check.lastRxPkts != c.rxPackets || c.check.lastAliveAt.IsZero() {
						c.check.lastRxPkts = c.rxPackets
						c.check.timeoutCount = 0 //reset
						c.check.lastAliveAt = time.Now()
						continue
					}
					//没有收到任何数据?
					c.check.timeoutCount += 1
					inactiveElapse := time.Since(c.check.lastAliveAt)
					if inactiveElapse > l.expire {
						l.logger.Errorf("conn:%v, inactiveElapse:%v, l.expire:%v\n", c, inactiveElapse, l.expire)
						c.Close()
					}
				}
			}

		}

	}
}

// 一般业务层都要有心跳，而且心跳不应该超过5分钟的
func (l *Listener) updateClientExpire(n int) {
	switch {
	case n > 1000:
		exipre := time.Minute * 5
		l.logger.Warnf("%v client socket num:%d over 1000, change expire to %v\n", l, n, exipre)
		l.expire = exipre
	case n > 500:
		l.expire = time.Minute * 10
	default:
		l.expire = time.Minute * 20
	}
}
