package runtimepaths

import "testing"

type iface struct{ Name, IP string }

// TestUsableLANIPv4 是本机真实网卡布局的回归：机器上有 WSL、两块 VMware 虚拟网卡、
// 若干 APIPA(169.254) 地址。天真的"取第一个非回环地址"会返回 172.19.224.1（WSL），
// 用户拿去连必然失败。
func TestUsableLANIPv4(t *testing.T) {
	cases := []struct {
		name   string
		ifaces []iface
		want   string
	}{
		{
			name: "真实布局：WSL 与 VMware 必须被跳过，选中以太网",
			ifaces: []iface{
				{"vEthernet (WSL (Hyper-V firewall))", "172.19.224.1"},
				{"VMware Network Adapter VMnet8", "192.168.111.1"},
				{"VMware Network Adapter VMnet1", "192.168.252.1"},
				{"以太网 2", "192.168.18.233"},
				{"以太网", "192.168.3.2"},
			},
			// 两块物理网卡同为 192.168 段，按枚举顺序取先出现的那个。
			// 谁才是真正的"局域网"无法从地址判断（都要先在链路层连上才算数），
			// 所以这里只保证「不是虚拟网卡」；精确定位交给 LANIPv4 的默认路由判断。
			want: "192.168.18.233",
		},
		{
			name: "只有虚拟网卡 → 不猜，返回空",
			ifaces: []iface{
				{"vEthernet (WSL)", "172.19.224.1"},
				{"VMware Network Adapter VMnet8", "192.168.111.1"},
			},
			want: "",
		},
		{
			name:   "APIPA 链路本地地址不可用",
			ifaces: []iface{{"以太网", "169.254.134.121"}, {"WLAN", "169.254.141.55"}},
			want:   "",
		},
		{
			name:   "回环不算局域网地址",
			ifaces: []iface{{"Loopback", "127.0.0.1"}},
			want:   "",
		},
		{
			name:   "公网地址不被当作局域网地址",
			ifaces: []iface{{"以太网", "203.0.113.7"}},
			want:   "",
		},
		{
			name: "10 网段可用，但优先级低于 192.168",
			ifaces: []iface{
				{"以太网 2", "10.0.0.5"},
				{"以太网", "192.168.1.9"},
			},
			want: "192.168.1.9",
		},
		{
			name:   "只有 10 网段时用它",
			ifaces: []iface{{"以太网", "10.8.0.3"}},
			want:   "10.8.0.3",
		},
		{
			name:   "只有 172.16/12 时用它",
			ifaces: []iface{{"以太网", "172.20.5.5"}},
			want:   "172.20.5.5",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := make([]struct{ Name, IP string }, len(c.ifaces))
			for i, it := range c.ifaces {
				in[i] = struct{ Name, IP string }{it.Name, it.IP}
			}
			if got := UsableLANIPv4(in); got != c.want {
				t.Errorf("UsableLANIPv4 = %q, want %q", got, c.want)
			}
		})
	}
}

func TestIsVirtualIfaceName(t *testing.T) {
	virtual := []string{
		"vEthernet (WSL (Hyper-V firewall))", "VMware Network Adapter VMnet8",
		"VirtualBox Host-Only Network", "Tailscale", "Docker0", "tun0", "utun3",
	}
	for _, n := range virtual {
		if !IsVirtualIfaceName(n) {
			t.Errorf("%q 应被判定为虚拟网卡", n)
		}
	}
	physical := []string{"以太网", "以太网 2", "WLAN", "Ethernet", "Wi-Fi", "en0"}
	for _, n := range physical {
		if IsVirtualIfaceName(n) {
			t.Errorf("%q 是物理网卡，不该被排除", n)
		}
	}
}
