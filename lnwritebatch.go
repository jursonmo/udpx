package udpx

import (
	"errors"
	"fmt"
	"log"

	pkgerr "github.com/pkg/errors"
	"golang.org/x/net/ipv4"
)

var ErrTooBig = errors.New("bigger than Buffer MaxSize")

//var ErrTxQueueFull = errors.New("Err txqueueu is full")

// use listener write batch, 把data 转换成MyBuffer, 然后放到tx队列里
func (c *UDPConn) WriteWithBatch(data []byte) (n int, err error) {
	b := GetMyBuffer(len(data))
	if b == nil {
		//data too bigger?
		panic(fmt.Errorf("GetMyBuffer fail for data len:%d", len(data)))
		//return 0, pkgerr.WithMessagef(ErrTooBig, "GetMyBuffer fail for data len:%d", len(data))
	}
	n, err = b.Write(data)
	if err != nil {
		//panic(err)
		err = fmt.Errorf("put data in buffer fail, err:%w", err)
		return
	}
	if n != len(data) {
		panic(fmt.Errorf("n:%d, len(data):%d", n, len(data)))
	}

	if c.ln != nil {
		b.SetAddr(c.raddr)
		err = c.ln.PutTxQueue(b, c.txBlocked)
		if err != nil {
			c.txDropPkts++
		} else {
			c.txPackets++
		}
	} else {
		//b.SetAddr(c.raddr) // ?? pc write 时，底层net.UDPConn 已经通过DialUDP() bind raddr ?
		err = c.PutTxQueue(b, c.txBlocked)
	}
	if err != nil {
		return 0, err
	}
	return
}

// 返回的error 应该实现net.Error temporary(), 这样上层Write可以认为Eagain,再次调用Write
func (l *Listener) PutTxQueue(b MyBuffer, blocked bool) error {
	if blocked {
		l.txqueue <- b
		l.txPackets++ //统计发送的包数,但是不是特别严谨, 因为这里不代表已经发送出去了
		return nil
	}

	//non-blocked
	select {
	case l.txqueue <- b:
		l.txPackets++ //统计发送的包数,但是不是特别严谨, 因为这里不代表已经发送出去了
	default:
		l.txDropPkts++
		//l.txDropBytes += int64(len(b.Bytes()))
		if l.txDropPkts&127 == 0 {
			//panic(fmt.Errorf("notice listener:%v, txDropPkts:%d\n", l, l.txDropPkts))
			l.logger.Warnf("notice listener:%v, txDropPkts:%d\n", l, l.txDropPkts)
		}
		Release(b)
		return ErrTxQueueFull
	}
	return nil
}

func (l *Listener) WriteBatchAble() bool {
	return l.writeBatchAble
}

func (l *Listener) writeBatchLoop() (err error) {
	bw, _ := NewPCBioWriter(l.pc, l.batchs)
	l.writeBatchAble = true
	defer func() { l.writeBatchAble = false }()
	l.logger.Infof("%v, writeBatchLoop started", l)
	defer func() { l.logger.Errorf("%v, writeBatchLoop quit, err:%+v", l, err) }()

	if l.txqueue == nil {
		l.txqueue = make(chan MyBuffer, l.txqueuelen)
	}

	err = bw.WriteBatchLoop(l.txqueue)
	return
	/*
		var err error
		for b := range l.txqueue {
			//为什么不把"data[]byte 转换成Mybuffer" 放在WriteWithBatch()实现,而不放在这里实现呢,
			//如果放在这里实现，PCBufioWriter 就可以实现bufioer 接口了
			//因为上层调用write(data []byte)后，默认是data 被发送出去了,并认为可以重用这个data的
			//如果把[]byte 放在txqueue 队列里, 那么这个data []byte 在生成MyBuffer前，可能被修改了.
			_, err = bw.Write(b)
			if err != nil {
				log.Println(err)
				return
			}
			if len(l.txqueue) == 0 && bw.Buffered() > 0 {
				err = bw.Flush()
				if err != nil {
					log.Println(err)
					return
				}
			}
		}
	*/
}

type writeBatchMsg struct {
	offset  int //send offset
	wms     []ipv4.Message
	buffers []MyBuffer
}

// PacketConnBufioWriter
type PCBufioWriter struct {
	pc     *ipv4.PacketConn
	batchs int
	writeBatchMsg
	err error
}

func NewPCBioWriter(pc *ipv4.PacketConn, batchs int) (*PCBufioWriter, error) {
	if batchs == 0 {
		batchs = defaultBatchs
	}
	bw := &PCBufioWriter{pc: pc, batchs: batchs}

	// bw.wms = make([]ipv4.Message, 0, batchs)
	// bw.buffers = make([]MyBuffer, 0, batchs)
	bw.writeBatchMsg.init(batchs)
	return bw, nil
}

//由于*PCBufioWriter 只能实现Write(b MyBuffer)，而不是Write([]byte) (n int, err error)
//所以*PCBufioWriter 并没有实现 Bufioer 接口。
/*
func (bw *PCBufioWriter) Write(b MyBuffer) (n int, err error) {
	if bw.err != nil {
		return 0, bw.err
	}

	ms := ipv4.Message{Buffers: [][]byte{b.Bytes()}, Addr: b.GetAddr()}
	bw.wms = append(bw.wms, ms)
	bw.buffers = append(bw.buffers, b)
	if len(bw.wms) == bw.batchs {
		if err := bw.Flush(); err != nil {
			return 0, err
		}
	}
	return len(b.Bytes()), nil
}

func (bw *PCBufioWriter) Buffered() int {
	return len(bw.wms)
}

func (bw *PCBufioWriter) Flush() error {
	log.Printf("listener %v, flushing %d packet....", bw.pc.LocalAddr(), len(bw.wms))
	if bw.err != nil {
		return bw.err
	}
	wn := len(bw.wms)
	send := 0
	for {
		n, err := bw.pc.WriteBatch(bw.wms[send:wn], 0)
		if err != nil {
			bw.err = err
			return err
		}
		bw.ReleaseMyBuffer(send, send+n)
		send += n
		if send == wn {
			bw.wms = bw.wms[:0]
			bw.buffers = bw.buffers[:0]
			return nil
		}
	}
}

func (bw *PCBufioWriter) ReleaseMyBuffer(from, to int) {
	for i := from; i < to; i++ {
		Release(bw.buffers[i])
	}
}
*/

// 由于*PCBufioWriter 只能实现Write(b MyBuffer)，而不是Write([]byte) (n int, err error)
// 所以*PCBufioWriter 并没有实现 Bufioer 接口。
func (bw *PCBufioWriter) Write(b MyBuffer) (n int, err error) {
	if bw.err != nil {
		return 0, bw.err
	}
	if flush := bw.addMsg(b); flush {
		if err := bw.Flush(); err != nil {
			return 0, err
		}
	}
	return len(b.Bytes()), nil
}

func (bw *PCBufioWriter) Buffered() int {
	return bw.buffered()
}

// not return until flush all msg; 直到发送完缓存中的所有数据才返回
func (bw *PCBufioWriter) Flush() error {
	if gMode == DebugMode {
		log.Printf("local %v, flushing %d packet....", bw.pc.LocalAddr(), len(bw.wms))
	}
	if bw.err != nil {
		return bw.err
	}

	//用于检查是否发送完所有数据
	sended := 0
	needToSend := bw.buffered()

	for {
		msgs := bw.msgBuffered()
		if len(msgs) == 0 {
			//已经发完了，检查下
			if sended != needToSend {
				log.Panicf("sended:%d, needToSend:%d", sended, needToSend)
			}
			return nil
		}
		n, err := bw.pc.WriteBatch(msgs, 0) //如果不是linux 平台，会报错：sendmsg invaild parameter
		if err != nil {
			bw.err = err
			return err
		}
		// 流量大的时候,经常出现一次write 系统调用没能发送完，导致n<len(msgs)
		// if n != len(msgs) {
		// 	log.Printf("-------n:%d, len(msgs):%d--------\n", n, len(msgs))
		// }

		sended += n

		bw.commit(n)
	}
}

func (w *writeBatchMsg) init(capability int) {
	w.offset = 0
	w.wms = make([]ipv4.Message, capability)
	for i := 0; i < capability; i++ {
		w.wms[i].Buffers = make([][]byte, 1)
	}
	w.wms = w.wms[:0]

	w.buffers = make([]MyBuffer, 0, capability)
}

func (w *writeBatchMsg) buffered() int {
	return len(w.wms) - w.offset
	//return len(w.msgBuffered())
}

func (w *writeBatchMsg) addMsg(b MyBuffer) (flush bool) {
	// ms := ipv4.Message{Buffers: [][]byte{b.Bytes()}, Addr: b.GetAddr()} //给Buffers赋值的这种方式产生很多小对象，频繁触发gc
	// w.wms = append(w.wms, ms)
	//w.buffers = append(w.buffers, b)

	if len(w.wms) == cap(w.wms) {
		//添加数据是, batch不可能是满的。
		panic(fmt.Errorf("len(w.wms) =%d, cap(w.wms):%d", len(w.wms), cap(w.wms)))
	}
	i := len(w.wms)
	w.wms = w.wms[:i+1]
	w.wms[i].Buffers[0] = b.Bytes()
	w.wms[i].Addr = b.GetAddr()

	w.buffers = append(w.buffers, b)
	return len(w.wms) == cap(w.wms)
}

// 获取需要发送的消息
func (w *writeBatchMsg) msgBuffered() []ipv4.Message {
	return w.wms[w.offset:]
}

func (w *writeBatchMsg) commit(sended int) {
	if sended == 0 {
		return
	}
	//已经发送的消息，可以释放
	for i := w.offset; i < w.offset+sended; i++ {
		w.wms[i].Buffers[0] = nil //set nil for gc
		w.wms[i].Addr = nil
		Release(w.buffers[i]) //release buffer to pool
		w.buffers[i] = nil
	}

	//update offset
	w.offset += sended

	//just check
	if w.offset > len(w.wms) {
		log.Panicf("w.offset:%d, > len(w.wms):%d", w.offset, len(w.wms))
	}

	//已经全部发完了，重置
	if w.offset == len(w.wms) {
		//log.Printf("--------- w.offset:%d, 已经全部发完了，重置\n", w.offset) //test ok
		w.offset = 0
		w.wms = w.wms[:0]
		w.buffers = w.buffers[:0]
	}
}

func (bw *PCBufioWriter) WriteBatchLoop(fromCh chan MyBuffer) error {
	var err error
	for b := range fromCh {
		//为什么不把"data[]byte 转换成Mybuffer" 放在WriteWithBatch()实现,而不放在这里实现呢,
		//如果放在这里实现，PCBufioWriter 就可以实现bufioer 接口了
		//因为上层调用write(data []byte)后，默认是data 被发送出去了,并认为可以重用这个data的
		//如果把[]byte 放在txqueue 队列里, 那么这个data []byte 在生成MyBuffer前，可能被修改了.
		_, err = bw.Write(b)
		if err != nil {
			return pkgerr.Wrap(err, "bw.Write() fail")
		}
		if len(fromCh) == 0 && bw.Buffered() > 0 {
			err = bw.Flush()
			if err != nil {
				return pkgerr.Wrap(err, "bw.Flush() fail")
			}
		}
	}
	return pkgerr.New("channel closed in WriteBatchLoop")
}
