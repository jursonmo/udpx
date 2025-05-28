package udpx

var defaultBatchs = 8

// 个人觉得, udp 应用层不应该发送超过mtu 1500的报文, 那样导致ip分片，
// 丢任意一个分片都导致整个报文丢弃，特别是udp, 运营商特别容易丢弃udp的报文。
// 发大包，是为了一次系统调用可以发更多的数据，但是用了sendmmsg,本来就可以一次系统调用能发多个报文了
// 就没必要发送一个大小超过mtu 值的报文。
var defaultMaxPacketSize = 1600

var txqueueBlocked = true //全局默认值, 意思是批量发送时, 发送队列满了，是否阻塞
