package udpx

import (
	"errors"
	"time"
)

var ErrTimerExpired = errors.New("timer expired")
var ErrTimerDeadline = errors.New("invaild timer deadline")
var ErrReadTimeout = errors.New("read timeout")
var ErrWriteTimeout = errors.New("write timeout")

// 目前server 产生的UDPConn 暂时不支持SetDeadline
// 对于server 产生的UDPConn, UDPConn中的lconn,其实是listener lconn, 即是很多UDPConn 共用的socket,
// 不能直接对c.lconn 进行设置 Deadline
// 解决方法是对于server 产生的UDPConn，自己增加readTimer writeTimer 来实现超时功能。
func (c *UDPConn) SetDeadline(t time.Time) error {
	if c.client {
		err := c.lconn.SetReadDeadline(t)
		if err != nil {
			return err
		}
		return c.lconn.SetWriteDeadline(t)
	}

	err := c.setReadDeadline(t)
	if err != nil {
		return err
	}
	return c.setWriteDeadline(t)
}

func (c *UDPConn) SetReadDeadline(t time.Time) error {
	if c.client {
		return c.lconn.SetReadDeadline(t)
	}
	//server conn SetReadDeadline
	return c.setReadDeadline(t)
}

func (c *UDPConn) SetWriteDeadline(t time.Time) error {
	if c.client {
		return c.lconn.SetWriteDeadline(t)
	}
	//server conn SetWriteDeadline
	return c.setWriteDeadline(t)
}

// 不适合频繁调用。
func (c *UDPConn) setReadDeadline(t time.Time) error {
	c.mux.Lock()
	defer c.mux.Unlock()

	if c.closed {
		return ErrConnClosed
	}

	if c.readTimer != nil {
		ok := c.readTimer.Stop()
		if !ok {
			c.readTimer = nil
			return ErrTimerExpired
		}
	}

	// t IsZero  表示取消超时, 上面的代码已经stop deadline timer
	if t.IsZero() { //cancel deadline,
		c.readTimer = nil //reset
		return nil
	}

	d := time.Until(t)
	if d <= 0 {
		return ErrTimerDeadline
	}
	c.readTimer = time.AfterFunc(d, func() { c.err = ErrReadTimeout; c.Close() })
	return nil
}

func (c *UDPConn) setWriteDeadline(t time.Time) error {
	c.mux.Lock()
	defer c.mux.Unlock()

	if c.closed {
		return ErrConnClosed
	}

	if c.writeTimer != nil {
		ok := c.writeTimer.Stop()
		if !ok {
			c.readTimer = nil
			return ErrTimerExpired
		}
	}

	// t IsZero  表示取消超时, 上面的代码已经stop deadline timer
	if t.IsZero() { //cancel deadline,
		c.writeTimer = nil //reset
		return nil
	}

	d := time.Until(t)
	if d <= 0 {
		return ErrTimerDeadline
	}

	c.writeTimer = time.AfterFunc(d, func() { c.err = ErrWriteTimeout; c.Close() })
	return nil
}
