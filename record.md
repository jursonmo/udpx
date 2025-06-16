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

TODO: udpx socket 缓冲区大小(Done at 2025-06-14晚上10)，以及 udp 相关信息，比如丢包等(Done at 2025-06-15)。
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