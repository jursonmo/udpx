#### udpx (only support linux)
1. reuseport: 多个listener socket, server可以更充分利用多核进行收发; 或者服务器端开启IP_PKTINFO来生成收发独立的conn, 更加能充分利用多核。
2. read/write batchs(readmmsg, writemmsg): 减少系统调用
3. buffer复用：尽量减少copy

udpx 的服务器运行linux, 可以服务所有的udp 客户端， 如果客户端想要用udpx 的批量收发数据，只能在linux 平台使用。

#### FAQ
1. udp server 侦听 0.0.0.0时，会出现一些问题，比如client连不上，原因是服务器返回数据时，需要重新根据路由来决定源地址，这时候的源地址跟客户端发起的目的可能不一样，即返回数据的五元组和客户端发起的不一样了，这样就会出问题，客户端是接受不到数据。
   + 1.1 解决方法一：这个时候在服务器上添加明显路由，指定源地址即可，ip ro add ${client_ip} via xx  src 本地ip源地址(缺点是需要知道客户端的ip, 只适用于指定的客户端的场景)
   + 1.2 解决方法二: 侦听指定的ip, 而不是0.0.0.0(缺点是只能服务于指定的ip, 不能服务所有的ip)
   + 1.3 解决方法三: 服务器端开启IP_PKTINFO, 接受数据创建新的conn, 设置reuseport , 每个conn bind 相同的ip和port(即服务器ip和端口), 再connect()绑定远端的ip和port(即客户端ip和端口), 这样就可以服务所有的ip了。(DONE, 可以查看record.md的记录)

2. udpx 作为隧道的underlay协议, 需要注意mtu, 尽量避免分片，导致接收端rpc 负载失效和增加分片报文的重组开销。根据隧道协议头部大小来设置mtu, 可以默认设置1440. 不分片才性能最佳。遇到分片链接断开的怪异问题, 所以最好不要分片。