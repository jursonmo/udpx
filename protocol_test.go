package udpx

import (
	"bytes"
	"context"
	"net"
	"testing"
	"time"
)

func TestFrameRoundTrip(t *testing.T) {
	payload := []byte{0x55, 0x58, makeVerType(frameVersion, frameTypeHello), 1, 2, 3, 4}
	raw := encodeFrame(frameTypeData, payload)

	version, typ, gotPayload, ok := decodeFrame(raw)
	if !ok {
		t.Fatal("decodeFrame failed")
	}
	if version != frameVersion {
		t.Fatalf("version=%d, want %d", version, frameVersion)
	}
	if typ != frameTypeData {
		t.Fatalf("typ=%d, want %d", typ, frameTypeData)
	}
	if !bytes.Equal(gotPayload, payload) {
		t.Fatalf("payload=%v, want %v", gotPayload, payload)
	}
}

func TestStatsPayloadRoundTrip(t *testing.T) {
	stats := ConnStats{
		TxPackets:   11,
		RxPackets:   22,
		RxDropPkts:  33,
		TxDropPkts:  44,
		TimestampMs: 55,
	}

	got, err := decodeStatsPayload(encodeStatsPayload(stats))
	if err != nil {
		t.Fatal(err)
	}
	if got != stats {
		t.Fatalf("stats=%+v, want %+v", got, stats)
	}
}

func TestUDPXEchoWithSharedListener(t *testing.T) {
	oldPktInfo := IP_PKTINFO_ENABLE
	IP_PKTINFO_ENABLE = false
	defer func() { IP_PKTINFO_ENABLE = oldPktInfo }()

	testUDPXEcho(t, 0)
}

func TestUDPXEchoWithStandaloneConn(t *testing.T) {
	oldPktInfo := IP_PKTINFO_ENABLE
	IP_PKTINFO_ENABLE = true
	defer func() { IP_PKTINFO_ENABLE = oldPktInfo }()

	testUDPXEcho(t, 8)
}

func TestUDPXStatsControlFrame(t *testing.T) {
	SetGlobalLogger(NoopLogger{})
	oldPktInfo := IP_PKTINFO_ENABLE
	IP_PKTINFO_ENABLE = true
	defer func() { IP_PKTINFO_ENABLE = oldPktInfo }()

	addr := freeUDPAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ln, err := NewUdpListen(ctx, "udp", addr, WithListenerNum(1), Batchs(8), MaxPacketSize(1700))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	accepted := make(chan *UDPConn, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		accepted <- conn.(*UDPConn)
	}()

	c, err := DialWithOpt(context.Background(), "udp", "", addr, WithBatchs(8), WithMaxPacketSize(1700))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var serverConn *UDPConn
	select {
	case serverConn = <-accepted:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for accepted conn")
	}

	if err := c.SendStats(); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(3 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for peer stats")
		case <-tick.C:
			if serverConn.PeerStats().TimestampMs != 0 {
				return
			}
		}
	}
}

func testUDPXEcho(t *testing.T, batchs int) {
	SetGlobalLogger(NoopLogger{})
	addr := freeUDPAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ln, err := NewUdpListen(ctx, "udp", addr, WithListenerNum(1), Batchs(batchs), MaxPacketSize(1700))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	payloads := [][]byte{
		[]byte("hello"),
		[]byte{0x01, 0x02, 0x03, 0x04},
		encodeFrame(frameTypeHello, []byte{0x01, 0x02, 0x03, 0x04}),
		bytes.Repeat([]byte("x"), 256),
	}

	serverErr := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			serverErr <- err
			return
		}
		buf := make([]byte, 1700)
		for range payloads {
			n, err := conn.Read(buf)
			if err != nil {
				serverErr <- err
				return
			}
			wn, err := conn.Write(buf[:n])
			if err != nil {
				serverErr <- err
				return
			}
			if wn != n {
				serverErr <- ErrShortRead
				return
			}
		}
		serverErr <- nil
	}()

	c, err := Dial(context.Background(), "", addr, WithBatchs(batchs), WithMaxPacketSize(1700))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	buf := make([]byte, 1700)
	for _, payload := range payloads {
		wn, err := c.Write(payload)
		if err != nil {
			t.Fatal(err)
		}
		if wn != len(payload) {
			t.Fatalf("Write returned %d, want %d", wn, len(payload))
		}
		n, err := c.Read(buf)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(buf[:n], payload) {
			t.Fatalf("echo payload=%v, want %v", buf[:n], payload)
		}
	}

	select {
	case err := <-serverErr:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for echo server")
	}
}

func freeUDPAddr(t *testing.T) string {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	return conn.LocalAddr().String()
}
