package udpx

import (
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
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

	rmemDefault, _ := getRmemDefault()
	//如果比默认值大，才设置。 如果系统的rmem默认值比较大,就不用设置，以默认值为准
	if bufSize > rmemDefault {
		err := conn.SetReadBuffer(bufSize)
		if err != nil {
			return err
		}
	}

	wmemDefault, _ := getWmemDefault()
	//如果比默认值大，才设置。 如果系统的wmem默认值比较大,就不用设置，以默认值为准
	if bufSize > wmemDefault {
		err := conn.SetWriteBuffer(bufSize)
		if err != nil {
			return err
		}
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
