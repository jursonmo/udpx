//go:build !linux
// +build !linux

package udpx

import "syscall"

func ReuseportControl(fs ...func(network, address string, c syscall.RawConn) error) func(network, address string, c syscall.RawConn) error {
	return nil
}

func ReuseaddrControl(fs ...func(network, address string, c syscall.RawConn) error) func(network, address string, c syscall.RawConn) error {
	return nil
}

func IpPktInfoControl(fs ...func(network, address string, c syscall.RawConn) error) func(network, address string, c syscall.RawConn) error {
	return nil
}

func MustIpPktInfoControl(fs ...func(network, address string, c syscall.RawConn) error) func(network, address string, c syscall.RawConn) error {
	return nil
}

func SetSocketBufControl(fs ...func(network, address string, c syscall.RawConn) error) func(network, address string, c syscall.RawConn) error {
	return nil
}

func SetSockControl(fs ...func(network, address string, c syscall.RawConn) error) func(network, address string, c syscall.RawConn) error {
	return nil
}
