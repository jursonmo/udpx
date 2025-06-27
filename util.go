package udpx

import (
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

type AddrKey struct {
	ip   uint32
	port uint32
}

// func udpAddrTrans(addr *net.UDPAddr) interface{} {//用interface{}导致AddrKey{} escape to heap
func udpAddrTrans(addr *net.UDPAddr) (AddrKey, bool) {
	ip4 := addr.IP.To4()
	if ip4 == nil {
		//it is ipv6, just support ipv4 for now
		return AddrKey{}, false
	}
	ip := binary.BigEndian.Uint32(ip4)
	return AddrKey{ip: ip, port: uint32(addr.Port)}, true
}

func (addr AddrKey) String() string {
	return fmt.Sprintf("%d.%d.%d.%d:%d", byte(addr.ip>>24), byte(addr.ip>>16), byte(addr.ip>>8), byte(addr.ip), addr.port)
}

// 有点奇怪，我设置2M = 1024*1024*2, 但是skmem 显示4M: rb4194304,tb4194304
// ss -uampn |grep xxx -A 1
// ESTAB    0         0             192.168.x.x:43672       192.168.4.xx:12347    users:(("mvnet_smem",pid=15275,fd=9))
// skmem:(r0,rb4194304,t0,tb4194304,f4096,w0,o0,bl0,d0)
// 我设置1M = 1024*1024, 结果skmem:(r0,rb2097152,t0,tb2097152,f4096,w0,o0,bl0,d0)
// 难道是某些系统或驱动可能为套接字维护两个独立的缓冲区（一个用于接收数据，一个用于处理数据），导致总大小是用户设置的 2 倍。

// 查看侦听的socket的buf大小, 命令选项里加l
// ss -unmlp |grep vnet -A 1
// UNCONN    0         0                        *:12347                  *:*        users:(("mvnet_IP_PKTNFO",pid=3980,fd=14))
// 	 skmem:(r0,rb10485760,t0,tb10485760,f4096,w0,o0,bl0,d0)
// UNCONN    0         0                        *:12347                  *:*        users:(("mvnet_IP_PKTNFO",pid=3980,fd=15))
// 	 skmem:(r0,rb10485760,t0,tb10485760,f4096,w0,o0,bl0,d0)
// UNCONN    0         0                        *:12347                  *:*        users:(("mvnet_IP_PKTNFO",pid=3980,fd=16))
// 	 skmem:(r0,rb10485760,t0,tb10485760,f0,w0,o0,bl0,d0)
// UNCONN    0         0                        *:12347                  *:*        users:(("mvnet_IP_PKTNFO",pid=3980,fd=17))
// 	 skmem:(r0,rb10485760,t0,tb10485760,f4096,w0,o0,bl0,d0)

func setSocketBuf(conn *net.UDPConn, bufSize int) error {
	// echo "12000000" > /proc/sys/net/core/wmem_max //最大12MB
	// echo "12000000" > /proc/sys/net/core/rmem_max

	//rmemDefault, _ := getRmemDefault()
	rmemDefault, _ := getSocketReceiveBufferSize(conn) //直接读当前socket缓冲区, 比读/proc/sys/net/core/rmem_default 要准确
	gLogger.Infof("socket rmemDefault:%d, need to set bufSize:%d", rmemDefault, bufSize)
	//如果比默认值大，才设置。 如果系统的rmem默认值比较大,就不用设置，以默认值为准
	if bufSize > rmemDefault {
		gLogger.Infof("c:%s->%s SetReadBuffer bufSize:%d", conn.LocalAddr(), conn.RemoteAddr(), bufSize)
		err := conn.SetReadBuffer(bufSize)
		if err != nil {
			return err
		}
		// 查看设置后的buf大小
		size, err := getSocketReceiveBufferSize(conn)
		gLogger.Infof("c:%s->%s getSocketReceiveBufferSize:%d, err:%v", conn.LocalAddr(), conn.RemoteAddr(), size, err)
	}

	//wmemDefault, _ := getWmemDefault()
	wmemDefault, _ := getSocketSendBufferSize(conn) //直接读当前socket缓冲区, 比读/proc/sys/net/core/wmem_default 要准确
	gLogger.Infof("socket wmemDefault:%d, need to set bufSize:%d", wmemDefault, bufSize)
	//如果比默认值大，才设置。 如果系统的wmem默认值比较大,就不用设置，以默认值为准
	if bufSize > wmemDefault {
		gLogger.Infof("c:%s->%s SetWriteBuffer bufSize:%d", conn.LocalAddr(), conn.RemoteAddr(), bufSize)
		err := conn.SetWriteBuffer(bufSize)
		if err != nil {
			return err
		}
		// 查看设置后的buf大小
		size, err := getSocketSendBufferSize(conn)
		gLogger.Infof("c:%s->%s getSocketSendBufferSize:%d, err:%v", conn.LocalAddr(), conn.RemoteAddr(), size, err)
	}
	return nil
}

func getWmemDefault() (int, error) {
	return getMemDefault("/proc/sys/net/core/wmem_default")
}

func getRmemDefault() (int, error) {
	return getMemDefault("/proc/sys/net/core/rmem_default")
}

func getMemDefault(filePath string) (int, error) {
	// 读取文件内容
	data, err := os.ReadFile(filePath)
	if err != nil {
		return 0, err
	}

	// 去除空白字符并转换为整数
	valueStr := strings.TrimSpace(string(data))
	value, err := strconv.Atoi(valueStr)
	if err != nil {
		return 0, err
	}

	return value, nil
}

func getSocketReceiveBufferSize(conn *net.UDPConn) (int, error) {
	// 获取文件描述符
	rawConn, err := conn.SyscallConn()
	if err != nil {
		return 0, err
	}

	var size int
	var geterr error
	err = rawConn.Control(func(fd uintptr) {
		// 使用 syscall.GetsockoptInt 获取 SO_RCVBUF
		size, geterr = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF)
	})
	if err != nil {
		return 0, err
	}
	if geterr != nil {
		return 0, geterr
	}

	return size, nil
}

func getSocketSendBufferSize(conn *net.UDPConn) (int, error) {
	rawConn, err := conn.SyscallConn()
	if err != nil {
		return 0, err
	}

	var size int
	var geterr error
	err = rawConn.Control(func(fd uintptr) {
		// 使用 syscall.GetsockoptInt 获取 SO_SNDBUF
		size, geterr = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_SNDBUF)
	})
	if err != nil {
		return 0, err
	}
	if geterr != nil {
		return 0, geterr
	}

	return size, nil
}
func getUDPSocketLen(conn *net.UDPConn) (int, error) {
	rawConn, err := conn.SyscallConn()
	if err != nil {
		return 0, err
	}

	var queueLen int
	var geterr error
	err = rawConn.Control(func(fd uintptr) {
		// 使用 SIOCINQ 获取接收缓冲区中的字节数
		_, _, errno := syscall.Syscall(
			syscall.SYS_IOCTL,
			fd,
			//syscall.SIOCINQ,
			unix.SIOCINQ,
			uintptr(unsafe.Pointer(&queueLen)),
		)
		if errno != 0 {
			geterr = fmt.Errorf("ioctl SIOCINQ failed: %v", errno)
		}
	})
	if err != nil {
		return 0, err
	}
	if geterr != nil {
		return 0, geterr
	}

	return queueLen, nil
}
