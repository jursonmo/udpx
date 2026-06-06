package udpx

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

var (
	// DefaultDebugStatsInterval 是 UDPConn 和 Listener 周期打印监控日志的默认间隔。
	DefaultDebugStatsInterval = time.Second * 10
)

// SocketKernelStats 是 Linux 通过 /proc/net/udp 和 /proc/net/udp6 暴露的
// 单个 UDP socket 内核统计。
type SocketKernelStats struct {
	Inode   string
	RxQueue uint64
	TxQueue uint64
	Drops   uint64
}

// ConnDebugStats 把 udpx 自己的计数和内核 UDP socket 计数放在同一个快照里，
// 方便周期日志对比判断包是在 udpx 队列里丢的，还是在内核 socket 里丢的。
type ConnDebugStats struct {
	Name       string
	LocalAddr  string
	RemoteAddr string

	RxPackets  int64
	TxPackets  int64
	RxDataPkts int64
	TxDataPkts int64
	RxDropPkts int64
	TxDropPkts int64
	RxQueueLen int
	TxQueueLen int
	RxQueueCap int
	TxQueueCap int

	PeerRxPackets  uint64
	PeerTxPackets  uint64
	PeerRxDropPkts uint64
	PeerTxDropPkts uint64

	Kernel    SocketKernelStats
	KernelErr string
}

// DebugStats 返回 UDPConn 的一次监控快照。这个方法只读取统计，不发送控制帧；
// 如果需要看到 Peer* 字段，需要两端周期性调用 SendStats 或 StartDebugStatsLoop。
func (c *UDPConn) DebugStats() ConnDebugStats {
	var kernel SocketKernelStats
	var kernelErr string
	if c.lconn != nil {
		var err error
		kernel, err = readUDPKernelStats(c.lconn)
		if err != nil {
			kernelErr = err.Error()
		}
	}

	peer := c.PeerStats()
	return ConnDebugStats{
		Name:       "udpconn",
		LocalAddr:  addrString(c.LocalAddr()),
		RemoteAddr: addrString(c.RemoteAddr()),

		RxPackets:  atomic.LoadInt64(&c.rxPackets),
		TxPackets:  atomic.LoadInt64(&c.txPackets),
		RxDataPkts: atomic.LoadInt64(&c.rxDataPkts),
		TxDataPkts: atomic.LoadInt64(&c.txDataPkts),
		RxDropPkts: atomic.LoadInt64(&c.rxDropPkts),
		TxDropPkts: atomic.LoadInt64(&c.txDropPkts),
		RxQueueLen: len(c.rxqueue),
		TxQueueLen: len(c.txqueue),
		RxQueueCap: cap(c.rxqueue),
		TxQueueCap: cap(c.txqueue),

		PeerRxPackets:  peer.RxPackets,
		PeerTxPackets:  peer.TxPackets,
		PeerRxDropPkts: peer.RxDropPkts,
		PeerTxDropPkts: peer.TxDropPkts,

		Kernel:    kernel,
		KernelErr: kernelErr,
	}
}

// GetSocketKernelStats 返回 UDPConn 当前底层 UDP socket 的内核统计。
func (c *UDPConn) GetSocketKernelStats() (SocketKernelStats, error) {
	return readUDPKernelStats(c.lconn)
}

// DebugStats 返回 Listener 的一次监控快照。Listener 自身没有 peer，但是它的
// socket 仍然可能在处理首包或 IP_PKTINFO 未开启时出现内核 UDP drops。
func (l *Listener) DebugStats() ConnDebugStats {
	var kernel SocketKernelStats
	var kernelErr string
	if l.lconn != nil {
		var err error
		kernel, err = readUDPKernelStats(l.lconn)
		if err != nil {
			kernelErr = err.Error()
		}
	}

	return ConnDebugStats{
		Name:       fmt.Sprintf("listener-%d", l.id),
		LocalAddr:  addrString(l.LocalAddr()),
		RemoteAddr: "*",

		RxPackets:  atomic.LoadInt64(&l.rxPackets),
		TxPackets:  atomic.LoadInt64(&l.txPackets),
		RxDropPkts: atomic.LoadInt64(&l.rxDropPkts),
		TxDropPkts: atomic.LoadInt64(&l.txDropPkts),
		TxQueueLen: len(l.txqueue),
		TxQueueCap: cap(l.txqueue),

		Kernel:    kernel,
		KernelErr: kernelErr,
	}
}

// SocketKernelStats 返回 Listener 当前底层 UDP socket 的内核统计。
func (l *Listener) SocketKernelStats() (SocketKernelStats, error) {
	return readUDPKernelStats(l.lconn)
}

// StartDebugStatsLoop 周期打印 UDPConn 的本端 udpx 计数、对端 udpx 计数、
// 当前底层 UDP socket 的内核计数。
func (c *UDPConn) StartDebugStatsLoop(ctx context.Context, interval time.Duration, logger Logger) {
	if logger == nil {
		logger = c.logger
	}
	if logger == nil {
		logger = gLogger
	}
	t := time.NewTicker(normalizeStatsInterval(interval))
	defer t.Stop()

	var last ConnDebugStats
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.dead:
			return
		case <-t.C:
			_ = c.SendStats()
			now := c.DebugStats()
			logDebugStatsDelta(logger, now, last)
			last = now
		}
	}
}

// StartDebugStatsLoop 周期打印 Listener socket 和它当前管理的所有 UDPConn。
func (l *Listener) StartDebugStatsLoop(ctx context.Context, interval time.Duration, logger Logger) {
	if logger == nil {
		logger = l.logger
	}
	if logger == nil {
		logger = gLogger
	}

	t := time.NewTicker(normalizeStatsInterval(interval))
	defer t.Stop()

	last := make(map[string]ConnDebugStats)
	for {
		select {
		case <-ctx.Done():
			return
		case <-l.dead:
			return
		case <-t.C:
			s := l.DebugStats()
			logDebugStatsDelta(logger, s, last[s.Name])
			last[s.Name] = s

			for _, c := range l.ListClientConns() {
				_ = c.SendStats()
				cs := c.DebugStats()
				key := fmt.Sprintf("%p", c)
				logDebugStatsDelta(logger, cs, last[key])
				last[key] = cs
			}
		}
	}
}

// StartDebugStatsLoop 周期打印 UdpListen 下所有 Listener socket 和所有已 accept 的 UDPConn。
func (ln *UdpListen) StartDebugStatsLoop(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(normalizeStatsInterval(interval))
	defer t.Stop()

	last := make(map[string]ConnDebugStats)
	logger := ln.logger
	if logger == nil {
		logger = gLogger
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ln.dead:
			return
		case <-t.C:
			for _, l := range ln.listeners {
				if l == nil {
					continue
				}
				s := l.DebugStats()
				lkey := fmt.Sprintf("listener:%p", l)
				logDebugStatsDelta(logger, s, last[lkey])
				last[lkey] = s

				for _, c := range l.ListClientConns() {
					_ = c.SendStats()
					cs := c.DebugStats()
					ckey := fmt.Sprintf("conn:%p", c)
					logDebugStatsDelta(logger, cs, last[ckey])
					last[ckey] = cs
				}
			}
		}
	}
}

func normalizeStatsInterval(interval time.Duration) time.Duration {
	if interval <= 0 {
		return time.Second
	}
	return interval
}

func logDebugStatsDelta(logger Logger, now, last ConnDebugStats) {
	// 只有当内核丢包或 udpx 丢包时才打印日志，避免周期日志过多干扰正常使用。
	if now.Kernel.Drops == 0 && now.RxDropPkts == 0 && now.TxDropPkts == 0 {
		logger.Infof("no drops, skip debug stats log for %s local=%s remote=%s ", now.Name, now.LocalAddr, now.RemoteAddr)
		return
	}

	logger.Errorf(
		"udpx debug stats name=%s local=%s remote=%s "+
			"rxPkt=%d(+%d) txPkt=%d(+%d) rxData=%d(+%d) txData=%d(+%d) "+
			"rxDropPkt=%d(+%d) txDropPkt=%d(+%d) rxq=%d/%d txq=%d/%d "+
			"peerRx=%d peerTx=%d peerRxDrop=%d peerTxDrop=%d "+
			"kernelInode=%s kernelRxQ=%d kernelTxQ=%d kernelDrops=%d(+%d) kernelErr=%q",
		now.Name,
		now.LocalAddr,
		now.RemoteAddr,
		now.RxPackets, now.RxPackets-last.RxPackets,
		now.TxPackets, now.TxPackets-last.TxPackets,
		now.RxDataPkts, now.RxDataPkts-last.RxDataPkts,
		now.TxDataPkts, now.TxDataPkts-last.TxDataPkts,
		now.RxDropPkts, now.RxDropPkts-last.RxDropPkts,
		now.TxDropPkts, now.TxDropPkts-last.TxDropPkts,
		now.RxQueueLen, now.RxQueueCap,
		now.TxQueueLen, now.TxQueueCap,
		now.PeerRxPackets,
		now.PeerTxPackets,
		now.PeerRxDropPkts,
		now.PeerTxDropPkts,
		now.Kernel.Inode,
		now.Kernel.RxQueue,
		now.Kernel.TxQueue,
		now.Kernel.Drops, now.Kernel.Drops-last.Kernel.Drops,
		now.KernelErr,
	)
}

func addrString(addr net.Addr) string {
	if addr == nil {
		return ""
	}
	return addr.String()
}

func readUDPKernelStats(conn *net.UDPConn) (SocketKernelStats, error) {
	inode, err := udpSocketInode(conn)
	if err != nil {
		return SocketKernelStats{}, err
	}
	return readUDPKernelStatsByInode(inode)
}

func udpSocketInode(conn *net.UDPConn) (string, error) {
	if conn == nil {
		return "", fmt.Errorf("nil UDPConn")
	}
	raw, err := conn.SyscallConn()
	if err != nil {
		return "", err
	}

	var inode string
	var opErr error
	err = raw.Control(func(fd uintptr) {
		// Linux 会在 /proc/self/fd 下给当前进程的每个 fd 创建一个符号链接。
		// 对 socket fd 来说，链接目标通常是 "socket:[123456]"，中括号里的
		// 数字就是内核 socket inode。
		//
		// /proc/net/udp 和 /proc/net/udp6 中也会记录同一个 inode，因此 inode
		// 是把 Go 的 *net.UDPConn 和内核 per-socket UDP 统计关联起来的稳定 key。
		link, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", fd))
		if err != nil {
			opErr = err
			return
		}
		if !strings.HasPrefix(link, "socket:[") || !strings.HasSuffix(link, "]") {
			opErr = fmt.Errorf("fd %d is not a socket link: %s", fd, link)
			return
		}
		inode = strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]")
	})
	if err != nil {
		return "", err
	}
	if opErr != nil {
		return "", opErr
	}
	if inode == "" {
		return "", fmt.Errorf("empty socket inode")
	}
	return inode, nil
}

func readUDPKernelStatsByInode(inode string) (SocketKernelStats, error) {
	var readErrs []string
	for _, path := range []string{"/proc/net/udp", "/proc/net/udp6"} {
		stats, ok, err := readUDPKernelStatsFromProc(path, inode)
		if err != nil {
			readErrs = append(readErrs, fmt.Sprintf("%s: %v", path, err))
			continue
		}
		if ok {
			return stats, nil
		}
	}
	if len(readErrs) > 0 {
		return SocketKernelStats{Inode: inode}, fmt.Errorf("socket inode %s not found; %s", inode, strings.Join(readErrs, "; "))
	}
	return SocketKernelStats{Inode: inode}, fmt.Errorf("socket inode %s not found in /proc/net/udp or /proc/net/udp6", inode)
}

func readUDPKernelStatsFromProc(path, inode string) (SocketKernelStats, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return SocketKernelStats{}, false, err
	}

	lines := strings.Split(string(data), "\n")
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) < 13 {
			return SocketKernelStats{}, false, fmt.Errorf("unexpected %s line: %q", path, line)
		}

		// /proc/net/udp 的每行由内核生成，常见字段顺序如下：
		//
		//   sl local_address rem_address st tx_queue:rx_queue tr:tm->when
		//   retrnsmt uid timeout inode ref pointer drops
		//
		// 其中 tx_queue:rx_queue 是十六进制，表示当前 socket 发送队列和接收
		// 队列里积压的字节数。如果 rx_queue 持续增长，说明 udpx 从内核接收队列
		// 取包不够快，后续可能触发内核丢包。
		//
		// 最后一列 drops 是内核维护的 per-socket 接收丢包计数。典型场景是 UDP
		// receive buffer 已满，用户态还没来得及读取，新到的数据报就会在内核里
		// 被丢掉。如果 kernel drops 增长而 udpx rxDropPkts 不增长，说明包在
		// 进入 udpx 之前就已经被内核丢了。
		if fields[9] != inode {
			continue
		}

		txq, rxq, err := parseKernelQueueField(fields[4])
		if err != nil {
			return SocketKernelStats{}, false, fmt.Errorf("%s inode %s: %w", path, inode, err)
		}
		drops, err := strconv.ParseUint(fields[len(fields)-1], 10, 64)
		if err != nil {
			return SocketKernelStats{}, false, fmt.Errorf("%s inode %s bad drops %q: %w", path, inode, fields[len(fields)-1], err)
		}

		return SocketKernelStats{
			Inode:   inode,
			TxQueue: txq,
			RxQueue: rxq,
			Drops:   drops,
		}, true, nil
	}
	return SocketKernelStats{}, false, nil
}

func parseKernelQueueField(field string) (txq, rxq uint64, err error) {
	parts := strings.Split(field, ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("bad tx_queue:rx_queue field %q", field)
	}
	txq, err = strconv.ParseUint(parts[0], 16, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("bad tx_queue %q: %w", parts[0], err)
	}
	rxq, err = strconv.ParseUint(parts[1], 16, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("bad rx_queue %q: %w", parts[1], err)
	}
	return txq, rxq, nil
}
