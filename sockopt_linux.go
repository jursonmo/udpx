package udpx

import (
	"log"
	"syscall"

	"golang.org/x/sys/unix"
)

func ReuseportControl(fs ...func(network, address string, c syscall.RawConn) error) func(network, address string, c syscall.RawConn) error {
	return SetSockControl(func(fd uintptr) error {
		err := unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
		if err != nil {
			log.Printf("SetsockoptInt SO_REUSEPORT failed, err:%v", err)
		}
		return err
	}, fs...)

	// return func(network, address string, c syscall.RawConn) error {
	// 	for _, f := range fs {
	// 		if err := f(network, address, c); err != nil {
	// 			return err
	// 		}
	// 	}

	// 	var syscallErr error
	// 	controlErr := c.Control(func(fd uintptr) {
	// 		syscallErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
	// 	})
	// 	if syscallErr != nil {
	// 		return syscallErr
	// 	}
	// 	return controlErr
	// }
}

func ReuseaddrControl(fs ...func(network, address string, c syscall.RawConn) error) func(network, address string, c syscall.RawConn) error {
	return SetSockControl(func(fd uintptr) error {
		return unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEADDR, 1)
	}, fs...)
}

func IpPktInfoControl(fs ...func(network, address string, c syscall.RawConn) error) func(network, address string, c syscall.RawConn) error {
	return SetSockControl(func(fd uintptr) error {
		err := unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_PKTINFO, 1)
		log.Printf("SetsockoptInt IP_PKTINFO, err:%v", err)
		return err
	}, fs...)
}

func MustIpPktInfoControl(fs ...func(network, address string, c syscall.RawConn) error) func(network, address string, c syscall.RawConn) error {
	return SetSockControl(func(fd uintptr) error {
		err := unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_PKTINFO, 1)
		if err != nil {
			log.Fatalf("SetsockoptInt IP_PKTINFO, err:%v", err)
		}
		return err
	}, fs...)
}

func SetSocketBufControl(bufSize int, fs ...func(network, address string, c syscall.RawConn) error) func(network, address string, c syscall.RawConn) error {
	return SetSockControl(func(fd uintptr) error {
		err := unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_RCVBUF, bufSize)
		if err != nil {
			return err
		}
		return unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_SNDBUF, bufSize)
	}, fs...)
}

// setOpt是设置socket选项的具体函数,setOpt的参数是fd,返回值是error, 任何一个setOpt的错误都会导致Control链条中断不再执行下一个Control并返回错误
func SetSockControl(setOpt func(fd uintptr) error, fs ...func(network, address string, c syscall.RawConn) error) func(network, address string, c syscall.RawConn) error {
	return func(network, address string, c syscall.RawConn) error {
		for _, f := range fs {
			if err := f(network, address, c); err != nil {
				return err
			}
		}

		var syscallErr error
		controlErr := c.Control(func(fd uintptr) {
			syscallErr = setOpt(fd)
		})
		if syscallErr != nil {
			return syscallErr
		}
		return controlErr
	}
}
