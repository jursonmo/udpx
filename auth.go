package udpx

import "net"

type AuthInfo struct {
	Token             []byte
	AuthData          []byte
	LocalAddr         *net.UDPAddr
	RemoteAddr        *net.UDPAddr
	ClientCanDataSeq  bool
	ClientWantDataSeq bool
}

type SessionPolicy struct {
	EnableDataSeq bool
}

type AuthPolicyFunc func(AuthInfo) (SessionPolicy, bool, error)

func defaultAuthPolicy(info AuthInfo) (SessionPolicy, bool, error) {
	ok, err := VerifyToken(info.Token)
	if !ok {
		return SessionPolicy{}, false, err
	}
	return SessionPolicy{EnableDataSeq: traceServerDataSeq && info.ClientCanDataSeq && info.ClientWantDataSeq}, true, nil
}

func cloneBytes(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	dst := make([]byte, len(b))
	copy(dst, b)
	return dst
}

func cloneUDPAddr(addr *net.UDPAddr) *net.UDPAddr {
	if addr == nil {
		return nil
	}
	dst := *addr
	dst.IP = cloneBytes(addr.IP)
	return &dst
}
