#### udpx (only support linux)
1. reuseport: 多个listener socket, server可以更充分利用多核进行收发; 或者服务器端开启IP_PKTINFO来生成收发独立的conn, 更加能充分利用多核。
2. read/write batchs(readmmsg, writemmsg): 减少系统调用
3. buffer复用：尽量减少copy

udpx 的服务器运行linux, 可以服务所有的udp 客户端， 如果客户端想要用udpx 的批量收发数据，只能在linux 平台使用。

