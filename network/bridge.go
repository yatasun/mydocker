package network

import (
	"fmt"
	"net"
	"os/exec"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/vishvananda/netlink"
)

type BridgeNetworkDriver struct {
}

func (d *BridgeNetworkDriver) Name() string {
	return "bridge"
}

func (d *BridgeNetworkDriver) Create(subnet string, name string) (*Network, error) {
	ip, ipRange, _ := net.ParseCIDR(subnet)
	ipRange.IP = ip
	nw := &Network{
		Name:    name,
		IpRange: ipRange,
		Driver:  d.Name(),
	}
	err := d.initBridge(nw)
	if err != nil {
		logrus.Errorf("error init bridge: %v", err)
	}
	return nw, err
}

func (d *BridgeNetworkDriver) Delete(network Network) error {
	bridgeName := network.Name
	br, err := netlink.LinkByName(bridgeName)
	if err != nil {
		return err
	}
	return netlink.LinkDel(br)
}

// 连接一个网络和网络端点
// endpoint 是在 net namespace 的一段
func (d *BridgeNetworkDriver) Connect(network *Network, endpoint *Endpoint) error {
	bridgeName := network.Name
	// Bridge 接口的对象和接口属性
	br, err := netlink.LinkByName(bridgeName)
	if err != nil {
		return err
	}

	// 创建 Veth 接口的配置
	la := netlink.NewLinkAttrs()
	// 由于 Linux 接口名的限制, 名字取 endpoint ID 的前 5 位
	la.Name = endpoint.ID[:5]
	// 通过设置 Veth 接口的 master 属性, 设置这个 Veth 的一端挂载到网络对应的 Linux Bridge 上
	la.MasterIndex = br.Attrs().Index

	// 创建 Veth 对象, 通过 PeerName 配置 Veth 另外一端的接口明
	// 配置 Veth 另外一端的名字 cif-{endpoint ID 的前 5 位}
	endpoint.Device = netlink.Veth{
		LinkAttrs: la,
		PeerName:  "cif-" + endpoint.ID[:5],
	}

	// 调用 netlink 的 LinkAdd 方法创建出这个 Veth 接口
	// 因为上面指定了 link 的 MasterIndex 是网络对应的 Linux Bridge
	// 所以 Veth 的一端就已经挂载到了网络对应的 Linux Bridge 上
	if err = netlink.LinkAdd(&endpoint.Device); err != nil {
		return fmt.Errorf("Error Add Endpoint Device: %v", err)
	}

	// 调用 netlink 的 LinkSetUp 方法, 设置 Veth 启动
	// 相当于 ip link set xxx up 命令
	if err = netlink.LinkSetUp(&endpoint.Device); err != nil {
		return fmt.Errorf("Error Add Endpoint Device: %v", err)
	}
	return nil
}

func (d *BridgeNetworkDriver) Disconnect(network Network, endpoint *Endpoint) error {
	return nil
}

func (d *BridgeNetworkDriver) initBridge(n *Network) error {
	// 初始化 Bridge 虚拟设备
	// try to get bridge by name, if it already exists then just exit
	bridgeName := n.Name
	if err := createBridgeInterface(bridgeName); err != nil {
		return fmt.Errorf("Error add bridge: %s, Error: %v", bridgeName, err)
	}

	// 设置 Bridge 设备的地址和路由
	gatewayIP := *n.IpRange
	gatewayIP.IP = n.IpRange.IP
	if err := setInterfaceIP(bridgeName, gatewayIP.String()); err != nil {
		return fmt.Errorf("Error assigning address: %s on bridge: %s with an error of: %v", gatewayIP, bridgeName, err)
	}

	// 启动 Bridge 设备
	if err := setInterfaceUP(bridgeName); err != nil {
		return fmt.Errorf("Error set bridge up: %s, Error: %v", bridgeName, err)
	}

	// 设置 iptables 的 SNAT 规则
	if err := setupIPTables(bridgeName, n.IpRange); err != nil {
		return fmt.Errorf("Error setting iptables for %s: %v", bridgeName, err)
	}

	return nil
}

// deleteBridge deletes the bridge
func (d *BridgeNetworkDriver) deleteBridge(n *Network) error {
	bridgeName := n.Name

	// get the link
	l, err := netlink.LinkByName(bridgeName)
	if err != nil {
		return fmt.Errorf("Getting link with name %s failed: %v", bridgeName, err)
	}

	// delete the link
	if err := netlink.LinkDel(l); err != nil {
		return fmt.Errorf("Failed to remove bridge interface %s delete: %v", bridgeName, err)
	}

	return nil
}

// 创建 Bridge 设备
func createBridgeInterface(bridgeName string) error {
	// 先检查是否已经存在同名的 Bridge 设备
	_, err := net.InterfaceByName(bridgeName)
	if err == nil || !strings.Contains(err.Error(), "no such network interface") {
		return err
	}

	// 初始化一个 netlink 的 Link 基础对象, Link 的名字即 Bridge 设备的名字
	// create *netlink.Bridge object
	la := netlink.NewLinkAttrs()
	la.Name = bridgeName

	// 使用刚才创建的 Link 的属性创建 netlink 的 Bridge 对象
	br := &netlink.Bridge{
		LinkAttrs: la,
	}
	// 调用 netlink 的 LinkAdd 方法, 创建 Bridge 虚拟网络设备
	// netlink 的 LinkAdd 方法是用来创建虚拟网络设备的, 相当于 ip link add xxx
	if err := netlink.LinkAdd(br); err != nil {
		return fmt.Errorf("Bridge creation failed for bridge %s: %v", bridgeName, err)
	}
	return nil
}

// 设置网络接口为 up 状态
func setInterfaceUP(interfaceName string) error {
	iface, err := netlink.LinkByName(interfaceName)
	if err != nil {
		return fmt.Errorf("Error retrieving a link named [ %s ]: %v", iface.Attrs().Name, err)
	}

	if err := netlink.LinkSetUp(iface); err != nil {
		return fmt.Errorf("Error enabling interface for %s: %v", interfaceName, err)
	}
	return nil
}

// 设置 Bridge 设备的地址和路由, eg, setInterfaceIP("testbridge", "192.168.0.1/24")
func setInterfaceIP(name string, rawIP string) error {
	retries := 2
	var iface netlink.Link
	var err error
	for i := 0; i < retries; i++ {
		iface, err = netlink.LinkByName(name)
		if err == nil {
			break
		}
		logrus.Debugf("error retrieving new bridge netlink link [ %s ]... retrying", name)
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		return fmt.Errorf("Abandoning retrieving the new bridge link from netlink, Run [ ip link ] to troubleshoot the error: %v", err)
	}
	// 由于 netlink.ParseIPNet 是对 net.ParseCIDR 的一个封装, 因此可以将 net.ParseCIDR 的返回值中的 IP 和 net 整合
	// 返回值中的 ipNet 既包含网段的信息, 192.168.0.0/24, 也包含了原始的 ip 192.168.0.1
	ipNet, err := netlink.ParseIPNet(rawIP)
	if err != nil {
		return err
	}
	// 通过 netlink.AddrAdd 给网络接口配置地址, 相当于 ip addr add xxx
	// 同时如果配置了地址所在网段的信息, 例如 192.168.0.0/24
	// 还会配置路由表 192.168.0.0/24 转发到这个 testbridge 的网络接口上
	addr := &netlink.Addr{
		IPNet:       ipNet,
		Label:       "",
		Flags:       0,
		Scope:       0,
		Peer:        &net.IPNet{},
		Broadcast:   []byte{},
		PreferedLft: 0,
		ValidLft:    0,
	}
	return netlink.AddrAdd(iface, addr)
}

// 设置 iptables Linux Bridge SNAT 规则
func setupIPTables(bridgeName string, subnet *net.IPNet) error {
	iptablesCmd := fmt.Sprintf("-t nat -A POSTROUTING -s %s ! -o %s -j MASQUERADE", subnet.String(), bridgeName)
	cmd := exec.Command("iptables", strings.Split(iptablesCmd, " ")...)
	//err := cmd.Run()
	output, err := cmd.Output()
	if err != nil {
		logrus.Errorf("iptables Output, %v", output)
	}
	return err
}
