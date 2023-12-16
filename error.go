package udpx

import "net"

var ErrTxQueueFull = &TxQueueFullError{"Err txqueueu is full"}
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
//TxQueueFullError 实现net.Error, 这样上次就可以判断是Temporary()，判断是否断开UDPConn还是 try again

type TxQueueFullError struct {
	s string
}

func (e *TxQueueFullError) New(s string) {
	e.s = s
}

// 实现 error 接口的方法: Error() string
func (e *TxQueueFullError) Error() string {
	return e.s
}
func (e *TxQueueFullError) Timeout() bool {
	return false
}
func (e *TxQueueFullError) Temporary() bool {
	return true
}
