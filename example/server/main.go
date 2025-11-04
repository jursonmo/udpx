package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"time"

	"github.com/jursonmo/udpx"
)

func main() {
	//udpx.SetMode(udpx.DebugMode) //设置debug 模式，会打印每次批量收发操作的日志，跑性能的时候不能用这种模式。

	lnAddr := "0.0.0.0:3333"
	l, err := udpx.NewUdpListen(context.Background(), "udp", lnAddr, udpx.WithListenerNum(2))
	if err != nil {
		log.Printf("err:%+v", err) //打印err堆栈
		panic(err)
	}
	time.Sleep(time.Second * 2) //让客户端先发出数据

	log.Printf("listen on:%s, start accepting .....", lnAddr)

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
			panic(fmt.Errorf("conn.Read err:%v", err))
		}
		//log.Printf("server recv data:%s", string(buf[:n]))
		wn, err := conn.Write(buf[:n])
		if err != nil {
			panic(fmt.Errorf("conn.Write err:%v", err))
		}
		if n != wn {
			log.Panicf("n:%d, wn:%d", n, wn)
		}
	}
}
