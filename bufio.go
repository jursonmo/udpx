package udpx

import (
	"bytes"
	"io"
	"log"
	"net"

	"golang.org/x/net/ipv4"
)

/* 实现bufio部分接口，让应用层像使用bufio 一样使用UDPConn
func (*bufio.Writer).Write(b []byte)(int, error)
func (*bufio.Writer).Buffered() int
func (*bufio.Writer).Flush() error
*/
//这几个方法不是并发安全的, 最好是在同一个goroutine里串行
type BufioWriter interface {
	Write(b []byte) (int, error)
	Buffered() int
	Flush() error
}

type UDPBufioWriter struct {
	c       *UDPConn
	batchs  int
	wms     []ipv4.Message
	buffers []*bytes.Buffer
	err     error
}

func NewBufioWriter(conn net.Conn, batchs int) BufioWriter {
	if v, ok := conn.(*UDPConn); ok {
		return NewUDPBufioWriter(v, batchs)
	}
	return nil
}

func NewUDPBufioWriter(c *UDPConn, batchs int) *UDPBufioWriter {
	if batchs == 0 {
		batchs = c.writeBatchs
	}
	if batchs <= 0 {
		panic("batchs <= 0")
	}
	ub := &UDPBufioWriter{c: c, batchs: batchs}
	ub.wms = make([]ipv4.Message, 0, batchs)
	ub.buffers = make([]*bytes.Buffer, batchs)
	for i := 0; i < batchs; i++ {
		ub.buffers[i] = bytes.NewBuffer(make([]byte, c.maxBufSize))
	}
	return ub
}

func (ub *UDPBufioWriter) Write(b []byte) (int, error) {
	if ub.err != nil {
		return 0, ub.err
	}

	var dst net.Addr
	if !ub.c.client {
		//accept 到达UDPConn 的pc 是没有指定目的地址，发送数据时，需要 SetControlMessage 指定目的地址
		dst = ub.c.raddr
	}
	//copy data to buffer, and ipv4.Message refence buffer
	n := len(ub.wms)
	buffer := ub.buffers[n]
	buffer.Reset()
	buffer.Write(b)
	//不能引用上层的b []byte,因为bufio要缓存一定的量才flush,所以要copy 上层的b []byte
	//ms := ipv4.Message{Buffers: [][]byte{b}, Addr: dst}
	ms := ipv4.Message{Buffers: [][]byte{buffer.Bytes()}, Addr: dst}
	ub.wms = append(ub.wms, ms)
	if len(ub.wms) == ub.batchs {
		if err := ub.Flush(); err != nil {
			return 0, err
		}
	}
	return len(b), nil
}

func (ub *UDPBufioWriter) Buffered() int {
	return len(ub.wms)
}

func (ub *UDPBufioWriter) Flush() error {
	log.Printf("%v->%v, flushing %d packet....", ub.c.LocalAddr(), ub.c.RemoteAddr(), len(ub.wms))
	if ub.err != nil {
		return ub.err
	}
	wn := len(ub.wms)
	send := 0
	for {
		n, err := ub.c.pc.WriteBatch(ub.wms[send:wn], 0)
		if err != nil {
			ub.err = err
			return err
		}
		send += n
		if send == wn {
			ub.wms = ub.wms[:0]
			return nil
		}
	}
}

/*
实现io.Reader 接口就行：
Read(buf []byte) (n int, err error)
*/
type UDPBufioReader struct {
	c          *UDPConn
	pc         *ipv4.PacketConn
	rms        []ipv4.Message
	offset     int
	have       int
	err        error
	batchs     int
	maxPktSize int
}

// 1. 对于server，所有的数据都需要经过一个goroutine 读取 listen conn 的数据 再分发给不同的自定义的UDPConn，
// 自定义的UDPConn不能自己去并发读取 listen conn 的数据，因为这样可能读到其他socket的数据，但是并发写是可以的，
// 内核有锁。 所以为了减少listen 底层socket 并发写产生的锁竞争，服务端尽量多些 udp listen socket
// 2. 如果是client dial 产生的 udp socket, 这个底层udp socket 被client独占的, client可以实现批量读取数据，
// 读到的数据都是自己的，所以可以提供bufio reader类似的功能(一次系统调用可以读取多个数据报文)
func NewBufioReader(conn net.Conn, batchs int, maxPktSize int) io.Reader {
	if v, ok := conn.(*UDPConn); ok && v.client {
		return NewUDPBufioReader(v, batchs, maxPktSize)
	}
	return nil
}

func NewUDPBufioReader(c *UDPConn, batchs int, maxPktSize int) *UDPBufioReader {
	if !c.client {
		return nil
	}
	if batchs == 0 {
		batchs = c.readBatchs
	}
	if maxPktSize == 0 {
		maxPktSize = c.maxBufSize
	}
	if batchs <= 0 || maxPktSize <= 0 {
		panic("batchs <= 0 || maxPktSize <= 0")
	}

	ub := &UDPBufioReader{c: c, batchs: batchs, maxPktSize: maxPktSize}
	ub.rms = make([]ipv4.Message, batchs)
	for i := 0; i < batchs; i++ {
		ub.rms[i] = ipv4.Message{Buffers: [][]byte{make([]byte, maxPktSize)}}
	}

	if c.pc != nil {
		ub.pc = c.pc
	} else {
		ub.pc = ipv4.NewPacketConn(c.lconn)
	}
	return ub
}

func (ub *UDPBufioReader) Read(buf []byte) (n int, err error) {
	for {
		if ub.err != nil {
			return 0, ub.err
		}
		data := ub.getData()
		if data == nil {
			ub.readBatchs()
			continue
		}
		n = copy(buf, data)
		return
	}
}

func (ub *UDPBufioReader) readBatchs() error {
	for {
		n, err := ub.pc.ReadBatch(ub.rms, 0)
		if err != nil {
			ub.err = err
			return err
		}
		log.Printf("%v<-%v, batch got n:%d, len(ms):%d\n", ub.c.LocalAddr(), ub.c.RemoteAddr(), n, len(ub.rms))

		if n > 0 {
			ub.have = n
			ub.offset = 0
			return nil
		}
	}
}

func (ub *UDPBufioReader) getData() []byte {
	if ub.have == 0 {
		return nil
	}
	ms := ub.rms[ub.offset]
	ub.offset++
	ub.have--
	return ms.Buffers[0][:ms.N]
}
