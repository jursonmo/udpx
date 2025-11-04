package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jursonmo/udpx"
)

func main() {
	if len(os.Args) < 2 {
		println("usage: client <server ip:port>")
		os.Exit(1)
	}
	serverAddr := os.Args[1]
	println("client start connect:%v ...", serverAddr)
	c, err := udpx.Dial(context.Background(), "", serverAddr)
	if err != nil {
		println("dial err:", err)
		os.Exit(1)
	}
	println("client connected to:%v", c.RemoteAddr())
	data := bytes.Repeat([]byte("helloworld12345"), 100)
	times := 0
	sum := 0
	buf := make([]byte, 1600)
	for {
		c.Write(data)

		n, err := c.Read(buf)
		if err != nil {
			println("read err:", err)
			os.Exit(1)
		}
		if n != len(data) {
			println("read n:%d, expect:%d", n, len(data))
			os.Exit(1)
		}
		sum += n
		times++

		if times%10000 == 0 {
			fmt.Printf("client recv tiems %d, total:%d\n", times, sum)
			time.Sleep(time.Second)
		}
	}
}
