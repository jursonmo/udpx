package udpx

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"sync/atomic"
)

type MyBufferPool interface {
	Get(int) MyBuffer
	Put(MyBuffer)
	Show() string
}

func ShowMyBufferPool() string {
	if defaultBufferPool == nil {
		return "defaultBufferPool == nil"
	}
	return defaultBufferPool.Show()
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
	Hold()
	Release() int32
	Reference() int32
	Reset()
}

type Buffer struct {
	ref     int32
	woffset int //write offset
	roffset int //read offset
	addr    net.Addr
	buf     []byte
}

type pool struct {
	sync.Pool
	maxBufferSize int
	newAlloc      int64
	alloc         int64
	putback       int64
}

func (p *pool) Get(n int) MyBuffer {
	if n > p.maxBufferSize {
		log.Panicf("n:%d > p.maxBufferSize:%d", n, p.maxBufferSize)
		return nil
	}
	b := p.Pool.Get().(*Buffer)
	atomic.AddInt64(&p.alloc, 1)

	b.Check()
	b.Hold()
	return b
}

func (p *pool) Put(b MyBuffer) {
	if b.Release() != 0 {
		log.Panicf("b.Release()!= 0")
	}
	b.Reset()
	atomic.AddInt64(&p.putback, 1)
	p.Pool.Put(b)
}

func (p *pool) Show() string {
	return fmt.Sprintf("newAlloc:%d, alloc:%d, putback:%d", atomic.LoadInt64(&p.newAlloc), atomic.LoadInt64(&p.alloc), atomic.LoadInt64(&p.putback))
}

var initPoolOnce sync.Once

func InitPool(maxBufferSize int) {
	initPoolOnce.Do(func() {
		p := &pool{maxBufferSize: maxBufferSize}
		p.Pool = sync.Pool{New: func() interface{} { atomic.AddInt64(&p.newAlloc, 1); return &Buffer{buf: make([]byte, maxBufferSize)} }}
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

func (b *Buffer) Reference() int32 {
	return atomic.LoadInt32(&b.ref)
}

func (b *Buffer) Hold() {
	atomic.AddInt32(&b.ref, 1)
}

func (b *Buffer) Release() int32 {
	return atomic.AddInt32(&b.ref, -1)
}

func (b *Buffer) Check() {
	if b.Reference() != 0 {
		log.Panicf("b.ref:%d != 0", b.ref)
	}
	if b.woffset != 0 || b.roffset != 0 || b.addr != nil {
		log.Panicf("b.woffset:%d, b.roffset:%d, b.addr:%v", b.woffset, b.roffset, b.addr)
	}
}
