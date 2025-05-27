package udpx

import "net"

var ErrTxQueueFull = &QueueFullError{"Err txqueueu is full"}
var ErrRxQueueFull = &QueueFullError{"Err rxqueueu is full"}
var _ net.Error = ErrTxQueueFull //for compile check

/* net.Error
type Error interface {
	error
	Timeout() bool // Is the error a timeout?

	// Deprecated: Temporary errors are not well-defined.
	// Most "temporary" errors are timeouts, and the few exceptions are surprising.
	// Do not use this method.
	Temporary() bool
}
*/
//QueueFullError 实现net.Error, 这样上次就可以判断是Temporary()，判断是否断开UDPConn还是 try again

type QueueFullError struct {
	s string
}

func (e *QueueFullError) New(s string) {
	e.s = s
}

// 实现 error 接口的方法: Error() string
func (e *QueueFullError) Error() string {
	return e.s
}
func (e *QueueFullError) Timeout() bool {
	return false
}
func (e *QueueFullError) Temporary() bool {
	return true
}
