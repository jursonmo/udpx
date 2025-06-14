package udpx

import (
	"encoding/binary"
	"fmt"
	"net"
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
func setSocketBuf(conn *net.UDPConn, bufSize int) error {
	// echo "12000000" > /proc/sys/net/core/wmem_max //最大12MB
	// echo "12000000" > /proc/sys/net/core/rmem_max

	err := conn.SetReadBuffer(bufSize)
	if err != nil {
		return err
	}
	err = conn.SetWriteBuffer(bufSize)
	if err != nil {
		return err
	}
	return nil
}
