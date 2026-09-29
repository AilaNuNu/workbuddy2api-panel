package runtimepaths

import (
	"net"
	"sort"
	"strings"
)

// virtualIfaceMarkers 虚拟/隧道网卡的名称特征（小写匹配）。
//
// 为什么必须按名字排除：虚拟网卡普遍落在私有网段，光靠 IP 段分不出来。
// 本机实测有 WSL(172.19.224.1)、VMware VMnet1/8(192.168.111.1/192.168.252.1)，
// 它们的地址都是合法的 172.16/12 或 192.168/16 —— 直接取「第一个私有地址」
// 会把这些根本连不通的地址展示给用户。
var virtualIfaceMarkers = []string{
	"wsl", "vmware", "vmnet", "virtualbox", "vbox", "hyper-v", "hyperv",
	"loopback", "tap", "tun", "tailscale", "zerotier", "clash", "utun",
	"docker", "veth", "bridge", "npcap", "bluetooth", "virtual",
}

// IsVirtualIfaceName 判断网卡名是否属于虚拟/隧道类。
func IsVirtualIfaceName(name string) bool {
	n := strings.ToLower(name)
	for _, m := range virtualIfaceMarkers {
		if strings.Contains(n, m) {
			return true
		}
	}
	return false
}

// UsableLANIPv4 从候选地址里挑一个可以在局域网内使用的 IPv4。
//
// 规则：
//   - 排除回环、链路本地（169.254/16，Windows 上表示"网线没插好/无 DHCP"）、
//     组播、以及非私有网段（公网地址不该被当成"局域网地址"展示）。
//   - 排除虚拟网卡上的地址（见 virtualIfaceMarkers）。
//
// 输入是 (网卡名, IP) 对，返回挑中的地址；没有可用地址时返回空串。
// 抽成纯函数是为了能测选择逻辑，不必依赖跑测试那台机器的网卡。
func UsableLANIPv4(ifaces []struct{ Name, IP string }) string {
	type cand struct {
		ip    string
		score int
	}
	var cands []cand
	for _, it := range ifaces {
		ip := net.ParseIP(it.IP)
		if ip == nil || ip.To4() == nil {
			continue
		}
		v4 := ip.To4()
		if !v4.IsPrivate() || v4.IsLoopback() || v4.IsLinkLocalUnicast() {
			continue
		}
		if IsVirtualIfaceName(it.Name) {
			continue
		}
		// 打分：家用/办公室最常见的 192.168/16 优先，其次 10/8，最后 172.16/12。
		// 只是想给用户一个"最可能连得通"的默认值，不追求绝对正确。
		s := 2
		switch {
		case v4[0] == 192 && v4[1] == 168:
			s = 0
		case v4[0] == 10:
			s = 1
		}
		cands = append(cands, cand{ip: v4.String(), score: s})
	}
	if len(cands) == 0 {
		return ""
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].score < cands[j].score })
	return cands[0].ip
}

// LANIPv4 返回本机可供局域网访问的 IPv4，没有则空串。
//
// 先取「系统默认出口地址」（UDP dial 不会真正发包，只让内核选路由），
// 若它恰好是一个可用的局域网地址就直接用——这比枚举顺序更准。
// 否则退化为按网卡名/IP 段挑选（TUN 代理场景下出口地址往往是虚拟地址，
// 因此这条回退路径是常态而非例外）。
func LANIPv4() string {
	var pairs []struct{ Name, IP string }
	ifaces, err := net.Interfaces()
	if err == nil {
		for _, ifi := range ifaces {
			if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 {
				continue
			}
			addrs, err := ifi.Addrs()
			if err != nil {
				continue
			}
			for _, a := range addrs {
				if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil {
					pairs = append(pairs, struct{ Name, IP string }{ifi.Name, ipn.IP.To4().String()})
				}
			}
		}
	}

	if out := defaultRouteIPv4(); out != "" {
		ip := net.ParseIP(out).To4()
		// 出口地址也必须通过可用性检查：TUN/代理下它常常是虚拟地址。
		for _, p := range pairs {
			if p.IP == out && ip != nil && ip.IsPrivate() &&
				!ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !IsVirtualIfaceName(p.Name) {
				return out
			}
		}
	}
	return UsableLANIPv4(pairs)
}

// defaultRouteIPv4 取系统默认出口地址；失败返回空串。
func defaultRouteIPv4() string {
	// 8.8.8.8 只是用来触发路由查找，UDP 不会真的建立连接或发包。
	c, err := net.Dial("udp4", "8.8.8.8:53")
	if err != nil {
		return ""
	}
	defer c.Close()
	if a, ok := c.LocalAddr().(*net.UDPAddr); ok && a.IP.To4() != nil {
		return a.IP.To4().String()
	}
	return ""
}
