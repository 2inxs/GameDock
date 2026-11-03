package network

import (
	"encoding/json"
	"fmt"
	"strings"

	"gamedock/internal/rooms"
)

const EngineVersion = "2.6.4"

func quoted(s string) string { value, _ := json.Marshal(s); return string(value) }

// EasyTier 2.6.4's no-TUN TCP proxy translates the node's overlay address to
// 127.0.0.1. Its ACL executes before proxy delivery. Explicit default-drop chains
// are necessary: an empty UDP whitelist means unrestricted UDP, not deny-all.
func coreConfig(c rooms.Config, listeners []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "instance_name = %s\nhostname = %s\n", quoted("gamedock-"+c.ID), quoted("GameDock-"+c.Role+"-"+c.ID[:8]))
	if c.Role == "host" {
		fmt.Fprintf(&b, "ipv4 = %s\ndhcp = false\n", quoted(c.HostVirtualIP+"/24"))
	} else {
		b.WriteString("dhcp = true\n")
	}
	b.WriteString("listeners = [")
	for i, value := range listeners {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(quoted(value))
	}
	b.WriteString("]\nexit_nodes = []\nroutes = []\nproxy_network = []\nipv6_public_addr_auto = false\nipv6_public_addr_provider = false\n")
	fmt.Fprintf(&b, "\n[network_identity]\nnetwork_name = %s\nnetwork_secret = %s\n", quoted(c.NetworkName), quoted(c.Secret))
	for _, peer := range c.BootstrapPeers {
		fmt.Fprintf(&b, "\n[[peer]]\nuri = %s\n", quoted(peer))
	}
	if c.Role == "guest" {
		fmt.Fprintf(&b, "\n[[port_forward]]\nproto = \"tcp\"\nbind_addr = \"127.0.0.1:%d\"\ndst_addr = \"%s:%d\"\n", c.LocalPort, c.HostVirtualIP, c.GamePort)
	}
	fmt.Fprintf(&b, `
[flags]
no_tun = true
bind_device = false
use_smoltcp = true
enable_encryption = true
encryption_algorithm = "aes-gcm"
enable_ipv6 = false
accept_dns = false
enable_exit_node = false
proxy_forward_by_system = false
disable_upnp = true
enable_udp_broadcast_relay = false
p2p_only = %t
disable_p2p = false
lazy_p2p = false
latency_first = false
private_mode = true
relay_network_whitelist = ""
disable_relay_data = true
disable_relay_kcp = true
disable_relay_quic = true
disable_kcp_input = true
disable_quic_input = true
enable_kcp_proxy = false
enable_quic_proxy = false
`, !c.AllowRelay)
	// ACL applies to overlay data only; bootstrap/control-plane traffic is not
	// subjected to these IP packet rules. No subnet or exit-node route is exported.
	b.WriteString(`
[[acl.acl_v1.chains]]
name = "gamedock-inbound"
chain_type = 1
enabled = true
default_action = 2
`)
	if c.Role == "host" {
		fmt.Fprintf(&b, `
[[acl.acl_v1.chains.rules]]
name = "minecraft-only"
priority = 100
enabled = true
protocol = 1
ports = ["%d"]
destination_ips = ["%s/32"]
action = 1
stateful = true
`, c.GamePort, c.HostVirtualIP)
	}
	b.WriteString(`
[[acl.acl_v1.chains]]
name = "gamedock-forward-deny"
chain_type = 3
enabled = true
default_action = 2
`)
	return b.String()
}
