1. 实现udp net.listener 相关接口，Listen, Accept, 方便应用层使用
2. udp listenr 一个读取报文，把报文交给自定义的对象UDPConn，UDPConn 实现net.Conn的接口方便应用层使用
3. UDPConn 每次系统调用只读写一个数据包，效率低，为了提高效率，利用ipv4.PacketConn 批量读取数据，同时提供类似bufio 的接口，方便应用层使用，例子在useBufioExample 目录下
4. listener 接受读取报文时，也是利用ipv4.PacketConn 批量读取数据，这样即使UDPConn调用read(),也不是每个系统调用值读取一个数据包，实际是listener 批量读，批量放到UDConn rxqueue里。
5. listener 批量读时，再放到UDPConn rxqueue里，这里需要创建新的内存对象，同时copy 操作一次，这样不利于对象复用。所以实现bufferpool, 实现 readLoopv2 实现 批量读和对象复用。
6. 前面说了，服务器accpet 生成的UDPConn，在UDPConn 批量写时，其实用的是listener 底层的socket, 也就是多个UDPConn 并发批量写时，其实是由内核锁来互斥的，这个是没有问题的，但是感觉不是很正规，一般的做法是，有一个线程负责发送一个socket 的数据，也就是应该把多个UDPConn的数据放在一个队列里，由一个任务取队列的数据，然后批量发送。这个在lnwritebatch.go 里实现。这样服务器accpet 生成UDPConn可以不用bufio,简单调用Write(),底层也是批量写的.
7. 2022-11-24为止，服务器accpet 生成UDPConn 简单调用Read()\Write()，底层都是listener socket 批量读写的。 client dial 生成的UDPConn, 读默认是批量读的(可以通过udp.WithRxHandler(nil)来取消默认批量读)，写还是要用自定义的bufioWriter
8. 2022-11-26,实现：client dial 生成的UDPConn 默认也是批量写，即后台默认起一个goroutine 负责批量写，业务层只需调用Write(). 如果udp.WithWriteBatchs(0)，就表示不想后台起一个goroutine 负责批量写，由业务层自己调用bufioWrite 里控制批量写。
9. client dial 生成的UDPConn，通过udp.WithReadBatchs(0)来控制是否在后台起一个goroutine 来批量读，而不是udp.WithRxHandler(nil)来控制
10. client 用readBatchLoopv2 来代替 readBatchLoop，这样可以复用内存对象，减少一次内存copy, 跟 listener readBatchLoopv2 一样
11. todo: udpx只负责高性能收发报文，不涉及到控制数据，也不涉及协议格式设置，心跳应该由上层协议来处理。tcp 可以有keepalive的配置，因为tcp 协议就是具备发送控制数据的能力，tcp 协议头部就有20个字节，但是udp只有8个字节，无法发送控制数据，所以心跳应该由上层协议来实现，但是udpx listener 也需要检查它ACCEPT的UDPConn socket 是否死掉，比如上层协议处理异常，永远不关闭udp conn，那么udpx listener就会积累很多UDPConn对象，所以udpx listener要定期查看它产生的UDPConn对象是否超过很长时间没有流量了，比如一个小时等，超过就关闭并删除UDPConn对象(finished at 2023-12-23)。(finish: 打印ln 的信息，包括其生成的所有udpConn)

12. 收发数据的流程：
request:
udpx client send --> put to udpx client udpConn's txqueue (这里可以阻塞,应该阻塞,除非设置非阻塞或超时时间) ---> udpx client pc write batchs task--> [....networt....] 
--> udpx listener --> find the udpConn, and put to the udpConn rxqueue (这里不能阻塞,因为可能会影响listener接受其他udpConn的数据)--> udpx client Read().  
listener 必须要知道client的地址，才能找到对应的udpConn. listener 是没有rxqueue的.

response:
server udpConn Write() --> put to it's listener txqueue(这里可以阻塞) --> listener write batchs task ---> [....networt....]
---> udpx client udpConn read batchs task --> put to udpx client udpConn rxqueue(阻不阻塞都可以)--> udpx client udpConn Read()

服务器这边的udpConn 是不会用到自己的txqueue channel的，因为服务器的udpConn 是由listener 生成的，统一由listener conn来写，即统一Put to listener 的 txqueue. 再由listener 批量写。

所以服务器的udpConn 只用到 rxqueue.没有用到自己txqueue的，TOOD：服务器这边的udpConn 不需要分配自己的txqueue, 节省内存。可以适当加到 listener txqueue长度。

TODO:
+ 1. udpx client send msg and  put to udpx client udpConn's txqueue , 这里可以阻塞,应该阻塞,除非设置非阻塞, 或超时时间, 非阻塞返回ErrChannelFull, 超时返回ErrTimeout. 这样上层协议就可以控制发送速度了.(done: 2025-05-28晚上10; 默认发送是阻塞的。)
+ 2. 同理， server udpConn Write() --> put to it's listener txqueue, 这里也应该阻塞。(done: 2025-05-28晚上10; 默认发送是阻塞的。)

13. TODO: udpx socket 缓冲区大小(Done at 2025-06-14晚上10)，以及 udp 相关信息，比如丢包等(Done at 2025-06-15)。
```
 cat /proc/sys/net/core/rmem_default  // sysctl net.core.rmem_default
 cat /proc/sys/net/core/wmem_default  // sysctl net.core.wmem_default
 cat /proc/sys/net/core/rmem_max
 cat /proc/sys/net/core/wmem_max
 cat /proc/sys/net/ipv4/udp_mem

echo "12000000" > /proc/sys/net/core/wmem_max //最大12MB
echo "12000000" > /proc/sys/net/core/rmem_max

```

```
ss -uampn
选项说明：
-u：只显示 UDP sockets
-a：显示所有 sockets（包括监听和非监听）
-m：显示内存使用情况（包括缓冲区大小）
-p：显示使用该 socket 的进程
-n：以数字形式显示地址和端口（不解析域名）
```
skmem:(r0,rb212992,t0,tb212992,f0,w0,o0,bl0,d0) //默认208KB（212992 字节

ss -uampn |grep xxx -A 1
ESTAB    0         0             192.168.x.x:43672       192.168.4.xx:12347    users:(("mvnet_smem",pid=15275,fd=9))
	 skmem:(r0,rb4194304,t0,tb4194304,f4096,w0,o0,bl0,d0)


查看具体udp socket 统计信息？
netstat -su //查看系统udp 统计信息
cat /proc/net/udp

14. iperf 发送方 的pprof heap type alloc_objects 显示(*writeBatchMsg).addMsg() 分配很多对象（Done)
  curl http://127.0.0.1:6061/debug/pprof/heap?seconds=5 > heap.out
  下载heap.out到本地
  go tool pprof -http=:9998 heap.out, 如果本地安装了graphviz, 会自动打开浏览器，点击：
  SAMPLE-->alloc_objects, 
  再点击VIEW -->flame Graph火焰图, 可以查看addMsg() 分配很多对象
  
15. TODO: iperf接收端 gc 次数依然很多(Done: readBatchLoopv2()也减少内存对象的分配。 从火焰图上看,只剩下golang.org/x/net/internal/socket.parseInetAddr 分配的对象暂时没办法处理)(git 仓库:https://github.com/golang/net/)

16. TODO: client 正在发送数据，服务端重启了，服务端接受到非magic包，是不创建udpConn的，这时client无法感知到，继续发数据，只能等心跳超时后自己断开，这样有点慢。服务端可以在尝试创建新的UDPConn时，如果发现不是magic 握手报文，可以回应一个关闭的控制报文，client 收到后，就知道服务端创建失败，就可以断开连接了。(Done: 经过测试，如果服务端程序退出了，client 在发送数据时，会收到错误 recvmmsg: connection refused，原因是服务端会回应icmp, client才感知到的，所以client 很快就重连了，重连成功后，为啥隧道为啥ping 不通，是因为服务端重启后，mvnet的mac地址变化了，但是client的arp缓存没有更新，所以ping 不同，arp -d 10.10.10.1 清空下缓存, 马上可以ping通，可以通过在tun.mac写死来解决。退一步，如果服务器重启非常快，即服务器的udp端口马上恢复到侦听的状态，那么client 是收不到错误 recvmmsg: connection refused, 等待超时再重连也可以接受。 还有一种可能，网络不通，client 也是收不到connection refused错误，等待重连也没有问题； 结论是暂时不去实现)

17. TODO: 发现一个问题，由于listener产生是UDPConn是有listener conn发送数据的，但是listener conn发送数据时，是不绑定源地址的，是由系统路由表决定最终的源地址，这样有个问题，如果udpx server 回应client数据时，走另一个网口出去，即来回路径不一致，那么server回应的数据的源地址跟client最初发送的目的地址不一样的(即不是同一个五元组的连接)，对client udp socket来说，是接受不到服务器回应的数据的。有两种方法解决这种问题：
  + 1. udp server 侦听时，不是侦听0.0.0.0:12345, 而是指定ip, 比如192.168.1.1:12345
  + 2. 服务器创建UDPConn时, 需要读取到报文的目的ip地址(IP_PKTINFO)，这样服务端创建UDPConn是就可以绑定源地址和目的地址，以后直接用这个UDPConn来收发数据，不再用listener conn 来收发数据，这样可不可行，如果可行，那么再多的client, listener conn 都没有收发数据的压力。因为如果按现在的方式，完全由listener conn来收发数据，性能上容易出现瓶颈，而且它需要收到每个报文都要判断是哪个UDPConn的，这个也是消耗性能的。(经过测试，这种方法不行，如果已经有udp server 侦听0.0.0.0:12345, 再想通过net.DialUDP(network, la, ra) 绑定源地址x.x.x.x:12345, 会提示错误bind: address already in use。可以考虑listen x.x.x.x:12345 同时设置REUSEPORT,生成conn 后再绑定远端地址,比如unix.Connect()来绑定, 这样就能生成独立的一对一的udp socket, 而udp listener socket 是一对多client的。Done:2025-06-22)

