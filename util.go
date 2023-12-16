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
