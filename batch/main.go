package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"runtime"
	"time"

	"github.com/jursonmo/udpx"
)

// 测试服务器和client 是否批量读写数据。
func main() {
	udpx.SetMode(udpx.DebugMode) //设置debug 模式，会打印每次批量收发操作的日志，跑性能的时候不能用这种模式。

	log.Printf("runtime.GOMAXPROCS(0):%d\n", runtime.GOMAXPROCS(0))
	go server()
	time.Sleep(time.Second)

	data := []byte("12345678")
	client("udp", "127.0.0.1:3333", data)
}

func server() {
	//以 udpx.WithListenerNum(2)为准，或者开启Reuseport:udpx.WithReuseport(true)，让系统根据GOMAXPROCS来决定
	l, err := udpx.NewUdpListen(context.Background(), "udp", "0.0.0.0:3333", udpx.WithListenerNum(2))
	//l, err := udpx.NewUdpListen(context.Background(), "udp", "0.0.0.0:3333", udpx.WithReuseport(true))
	if err != nil {
		log.Printf("err:%+v", err) //打印err堆栈
		panic(err)
	}
	//time.Sleep(time.Second * 2) //让客户端先发出数据
	go func() {
		time.Sleep(time.Second * 2)
		ShowLnDetail(l)
	}()

	log.Printf("start accepting .....")
	for {
		conn, err := l.Accept()
		if err != nil {
			panic(err)
		}
		go handle(conn)
	}

}

func ShowLnDetail(ul *udpx.UdpListen) {
	fmt.Printf("LnDetail:%s\n", string(ul.Detail()))
}

func handle(conn net.Conn) {
	buf := make([]byte, 1600)
	i := 0
	for {
		n, err := conn.Read(buf)
		if err != nil {
			panic(err)
		}
		log.Printf("server, i:%d, recv data:%s, and write back", i, string(buf[:n]))
		i++
		wn, err := conn.Write(buf[:n])
		if err != nil {
			if e, ok := err.(net.Error); ok && e.Temporary() {
				continue
			}
			panic(err)
		}
		if n != wn {
			log.Panicf("n:%d, wn:%d", n, wn)
		}
	}
}

func client(network, raddr string, data []byte) {
	uconn, err := udpx.Dial(context.Background(), "", "127.0.0.1:3333")
	if err != nil {
		log.Panic(err)
	}
	for i := 0; i < 10; i++ {
		n, err := uconn.Write(data)
		if err != nil {
			if e, ok := err.(net.Error); ok && e.Temporary() {
				continue
			}
			panic(err)
		}
		log.Printf("client:%d, conn local:%s->%s, write data len:%d", i, uconn.LocalAddr().String(),
			uconn.RemoteAddr().String(), n)
	}
	buf := make([]byte, 1600)
	i := 0
	for {
		n, err := uconn.Read(buf)
		if err != nil {
			panic(err)
		}
		log.Printf("client, i:%d, recv data:%s", i, string(buf[:n]))
		i++
	}
}
