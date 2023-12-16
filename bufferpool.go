package udpx

import (
	"errors"
	"io"
	"log"
	"net"
	"sync"
)

type MyBufferPool interface {
	Get(int) MyBuffer
	Put(MyBuffer)
}

var defaultBufferPool MyBufferPool

func SetDefaultBufferPool(p MyBufferPool) {
	defaultBufferPool = p
}

func GetMyBuffer(n int) MyBuffer {
	return defaultBufferPool.Get(n)
}
func Release(b MyBuffer) {
	defaultBufferPool.Put(b)
}

type MyBuffer interface {
	io.ReadWriter
	Bytes() []byte
	Buffer() []byte
	Advance(int) //instead of AddLen(int)
	SetAddr(net.Addr)
	GetAddr() net.Addr
}

type Buffer struct {
	woffset int //write offset
	roffset int //read offset
	addr    net.Addr
	buf     []byte
}

type pool struct {
	maxBufferSize int
	sync.Pool
}

func (p *pool) Get(n int) MyBuffer {
	if n > p.maxBufferSize {
		return nil
	}
	b := p.Pool.Get().(*Buffer)
	b.Reset()

	return b
}

func (p *pool) Put(b MyBuffer) {
	p.Pool.Put(b)
}

var initPoolOnce sync.Once

func InitPool(maxBufferSize int) {
	initPoolOnce.Do(func() {
		p := &pool{maxBufferSize: maxBufferSize, Pool: sync.Pool{New: func() interface{} { return &Buffer{buf: make([]byte, maxBufferSize)} }}}
		SetDefaultBufferPool(p)
	})
}

var ErrNoSpaceEnough = errors.New("no space Enough")

func (b *Buffer) Write(buf []byte) (int, error) {
	if len(buf) > b.free() {
		return 0, ErrNoSpaceEnough
	}
	n := copy(b.buf[b.woffset:], buf)
	b.woffset += n
	return n, nil
}

func (b *Buffer) Read(buf []byte) (int, error) {
	n := copy(buf, b.buf[b.roffset:b.woffset])
	b.roffset += n
	return n, nil
}

func (b *Buffer) free() int {
	return len(b.buf) - b.woffset
}

func (b *Buffer) Bytes() []byte {
	return b.buf[b.roffset:b.woffset]
}

// 返回可用的内存空间
func (b *Buffer) Buffer() []byte {
	return b.buf[b.woffset:]
}

// Advance
func (b *Buffer) Advance(n int) {
	if b.woffset+n > len(b.buf) {
		log.Panicf("b.woffset:%d + n:%d > len(b.buf):%d ", b.woffset, n, len(b.buf))
	}
	b.woffset += n
}

func (b *Buffer) SetAddr(addr net.Addr) {
	b.addr = addr
}

func (b *Buffer) GetAddr() net.Addr {
	return b.addr
}

func (b *Buffer) Reset() {
	b.woffset = 0
	b.roffset = 0
	b.addr = nil
}
