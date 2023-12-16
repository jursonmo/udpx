package main

import (
	"context"
	"log"
	"net"
	"time"

	"github.com/jursonmo/udpx"
)

func main() {
	go server()
	time.Sleep(time.Second)

	data := []byte("12345678")
	client("udp", "127.0.0.1:3333", data, 2)
}

func server() {
	l, err := udpx.NewUdpListen(context.Background(), "udp", "0.0.0.0:3333", udpx.WithReuseport(true), udpx.WithListenerNum(2))
	if err != nil {
		panic(err)
	}
	time.Sleep(time.Second * 2) //让客户端先发出数据

	log.Printf("start accepting .....")

	for {
		conn, err := l.Accept()
		if err != nil {
			panic(err)
		}
		go handle(conn)
	}
}

func handle(conn net.Conn) {
	buf := make([]byte, 1600)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			panic(err)
		}
		log.Printf("server recv data:%s", string(buf[:n]))
		wn, err := conn.Write(buf[:n])
		if err != nil {
			panic(err)
		}
		if n != wn {
			log.Panicf("n:%d, wn:%d", n, wn)
		}
	}
}

func client(network, raddr string, data []byte, write int) {
	ra, err := net.ResolveUDPAddr(network, raddr)
	if err != nil {
		panic(err)
	}
	uconn, err := net.DialUDP(network, nil, ra)
	if err != nil {
		panic(err)
	}

	for i := 0; i < write; i++ {
		n, err := uconn.Write(data)
		if err != nil {
			panic(err)
		}
		log.Printf("conn local:%s->%s, i:%d, write data len:%d", uconn.LocalAddr().String(),
			uconn.RemoteAddr().String(), i, n)
	}

	buf := make([]byte, 1600)
	for {
		n, err := uconn.Read(buf)
		if err != nil {
			panic(err)
		}
		log.Printf("client recv data:%s", string(buf[:n]))
	}
}
