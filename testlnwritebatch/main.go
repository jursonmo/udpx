package main

import (
	"context"
	"log"
	"net"
	"time"

	"github.com/jursonmo/udpx"
)

// 主要是测试服务端是否可以批量flush 发送数据
func main() {
	udpx.SetMode(udpx.DebugMode) //设置debug 模式，会打印每次批量收发操作的日志，跑性能的时候不能用这种模式。

	go server()
	time.Sleep(time.Second)

	data := []byte("12345678")
	client("udp", "127.0.0.1:3333", data, 10)
}

func server() {
	//如果只是开启reuseport, 没有指明listener 个数，那么按CPU个数来设置listener 个数
	//l, err := udp.NewUdpListen(context.Background(), "udp", "0.0.0.0:3333", udp.WithReuseport(true))
	//只需设置WithListenerNum 大于 1 就表示开启reuseport
	l, err := udpx.NewUdpListen(context.Background(), "udp", "0.0.0.0:3333", udpx.WithListenerNum(2))
	if err != nil {
		log.Printf("err:%+v", err) //打印err堆栈
		panic(err)
	}
	//time.Sleep(time.Second * 2) //让客户端先发出数据

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
		//客户端发一个数据，服务器echo 10分数据,用于测试，10分数据都统一交给listener 批量写。
		for i := 0; i < 10; i++ {
			log.Printf("server write back %d, data:%s", i, string(buf[:n]))
			wn, err := conn.Write(buf[:n])
			if err != nil {
				panic(err)
			}
			if n != wn {
				log.Panicf("n:%d, wn:%d", n, wn)
			}
		}
	}
}

// 客户端发一个数据，服务器echo 10分数据,
func client(network, raddr string, data []byte, write int) {
	ra, err := net.ResolveUDPAddr(network, raddr)
	if err != nil {
		panic(err)
	}
	uconn, err := net.DialUDP(network, nil, ra)
	if err != nil {
		panic(err)
	}

	n, err := uconn.Write(data)
	if err != nil {
		panic(err)
	}
	log.Printf("conn local:%s->%s, write data len:%d", uconn.LocalAddr().String(),
		uconn.RemoteAddr().String(), n)

	buf := make([]byte, 1600)
	i := 0
	for {
		n, err := uconn.Read(buf)
		if err != nil {
			panic(err)
		}
		log.Printf("i:%d, client recv data:%s", i, string(buf[:n]))
		i++
	}
}
