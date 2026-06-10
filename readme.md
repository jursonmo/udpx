#### udpx (only support linux)
1. reuseport: 多个listener socket, server可以更充分利用多核进行收发; 或者服务器端开启IP_PKTINFO来生成收发独立的conn, 更加能充分利用多核。
2. read/write batchs(readmmsg, writemmsg): 减少系统调用
3. buffer复用：尽量减少copy

udpx 的服务器运行linux, 可以服务所有的udp 客户端， 如果客户端想要用udpx 的批量收发数据，只能在linux 平台使用。

#### FAQ
1. udp server 侦听 0.0.0.0时，会出现一些问题，比如client连不上，原因是服务器返回数据时，需要重新根据路由来决定源地址，这时候的源地址跟客户端发起的目的可能不一样，即返回数据的五元组和客户端发起的不一样了，这样就会出问题，客户端是接受不到数据。解决方式：先保证源地址的对的，然后保证出口是源地址对应的接口。
   + 1.1 解决方法一：这个时候在服务器上添加明显路由，指定源地址即可，ip ro add ${client_ip} via xx src 本地ip源地址, 这样保证源地址是正确的 (缺点是需要知道客户端的ip, 只适用于指定的客户端的场景)
   + 1.2 解决方法二: 侦听指定的ip, 而不是0.0.0.0(缺点是只能服务于指定的ip, 不能服务所有的ip, 修改侦听接口,支持侦听多个ip也能临时解决)
   + 1.3 解决方法三: 服务器端开启IP_PKTINFO(为了拿到数据的目的地址,即服务器的ip和端口，用于bind), 接受数据创建新的conn, 设置reuseport , 每个conn bind 相同的ip和port(即服务器ip和端口), 再connect()绑定远端的ip和port(即客户端ip和端口), 这样就可以服务所有的ip了。(DONE, 可以查看record.md的记录)
   + 源地址正确后，如果多出口，需要添加基于源地址的策略路由来保证出口正确, ip ro add from ${指定的本地ip源地址} lookup 100, 100里包含指定出口的默认路由。

2. 在udp server 侦听 0.0.0.0时的使用场景，可能会出现bug， 因为自定义UDPConn的查找，仅仅是根据对方的ip和port来查找的，而不是根据五元组来查找的。如果一个客户端同时服务器的多个ip, 那么就会出现问题，两个连接会被错误的路由到同一个conn上，导致数据错乱。

3. udpx 作为隧道的underlay协议, 需要注意mtu, 尽量避免分片，导致接收端rpc 负载失效和增加分片报文的重组开销。根据隧道协议头部大小来设置mtu, 可以默认设置1440. 不分片才性能最佳, 且分片会导致softirq不能负载。遇到分片链接断开的怪异问题, 所以最好不要分片。(增加了magic 和协议头部后，最好设置1430)

4. 跑iperf3 出现丢包时，先查看程序有没有统计到rxDropPkts和txDropPkts，tc -s qdisc show dev ${underlay 网卡}，查看网卡是否有丢包, 接受端查看/proc/net/udp 是否有丢包, nstat -az | egrep 'UdpInErrors|UdpRcvbufErrors|UdpInCsumErrors|TcpExtTCPOFOQueue|TCPFastRetrans|TCPTimeouts' 统计情况, 