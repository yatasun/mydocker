package network

import (
	"encoding/json"
	"fmt"
	"mydocker/container"
	"net"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"text/tabwriter"

	"github.com/sirupsen/logrus"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
)

var (
	defaultNetworkPath = "/var/run/mydocker/network/network/"
	drivers            = map[string]NetworkDriver{}
	networks           = map[string]*Network{}
)

type Network struct {
	Name    string
	IpRange *net.IPNet
	Driver  string
}

type Endpoint struct {
	ID          string           `json:"id"`
	Device      netlink.Veth     `json:"dev"`
	IPAddress   net.IP           `json:"ip"`
	MacAddress  net.HardwareAddr `json:"mac"`
	Network     *Network
	PortMapping []string
}

type NetworkDriver interface {
	Name() string
	Create(subnet string, name string) (*Network, error)
	Delete(network Network) error
	Connect(network *Network, endpoint *Endpoint) error
	Disconnect(network Network, endpoint *Endpoint) error
}

func (nw *Network) dump(dumpPath string) error {
	if _, err := os.Stat(dumpPath); err != nil {
		if os.IsNotExist(err) {
			os.Mkdir(dumpPath, 0644)
		} else {
			return err
		}
	}

	nwPath := path.Join(dumpPath, nw.Name)
	nwFile, err := os.OpenFile(nwPath, os.O_TRUNC|os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		logrus.Errorf("error: %v", err)
		return err
	}
	defer nwFile.Close()
	nwJson, err := json.Marshal(nw)
	if err != nil {
		logrus.Errorf("error: %v", err)
		return err
	}

	_, err = nwFile.Write(nwJson)
	if err != nil {
		logrus.Errorf("error: %v", err)
		return err
	}
	return nil
}

func (nw *Network) remove(dumpPath string) error {
	if _, err := os.Stat(path.Join(dumpPath, nw.Name)); err != nil {
		if os.IsNotExist(err) {
			return nil
		} else {
			return err
		}
	} else {
		return os.Remove(path.Join(dumpPath, nw.Name))
	}
}

func (nw *Network) load(dumpPath string) error {
	nwConfigFile, err := os.Open(dumpPath)
	defer nwConfigFile.Close()
	if err != nil {
		return err
	}
	nwJson := make([]byte, 2000)
	n, err := nwConfigFile.Read(nwJson)
	if err != nil {
		return err
	}

	err = json.Unmarshal(nwJson[:n], nw)
	if err != nil {
		logrus.Errorf("Error load nw info: %v", err)
		return err
	}
	return nil
}

func Init() error {
	// 加载网络驱动
	var bridgeDriver = BridgeNetworkDriver{}
	drivers[bridgeDriver.Name()] = &bridgeDriver
	// 判断网络的配置目录是否存在, 不存在则创建
	if _, err := os.Stat(defaultNetworkPath); err != nil {
		if os.IsNotExist(err) {
			os.MkdirAll(defaultNetworkPath, 0644)
		} else {
			return err
		}
	}

	// 检查网络配置目录中的所有文件
	// filepath.Walk(path, func(string, os.FileInfo, error)) 函数会遍历指定的 path 目录
	// 并执行第二个参数中的函数指针去处理目录下的每一个文件
	filepath.Walk(defaultNetworkPath, func(nwPath string, info os.FileInfo, err error) error {
		if info.IsDir() {
			return nil
		}

		// 加载文件名作为网络名
		_, nwName := path.Split(nwPath)
		nw := &Network{
			Name: nwName,
		}

		if err := nw.load(nwPath); err != nil {
			logrus.Errorf("error load netword: %s", err)
		}

		networks[nwName] = nw
		return nil
	})

	return nil
}

func CreateNetwork(driver, subnet, name string) error {
	_, cidr, _ := net.ParseCIDR(subnet)
	// 通过 IPAM 分配网关 IP, 获取到网段中第一个 IP 作为网关的 IP
	gatawayIp, err := ipAllocator.Allocate(cidr)
	if err != nil {
		return err
	}

	cidr.IP = gatawayIp
	// 调用指定的网络驱动创建网络, 这里的 drivers 字典是各个网络驱动的实例字典
	// 通过调用网络驱动的 Create 方法创建网络, 后面会以 Bridge 驱动为例介绍它的实现
	nw, err := drivers[driver].Create(cidr.String(), name)
	if err != nil {
		return err
	}

	// 保存网络信息， 将网络的信息保存在文件系统中，以便查询和在网络上连接网络端点
	return nw.dump(defaultNetworkPath)
}

func ListNetwork() {
	w := tabwriter.NewWriter(os.Stdout, 12, 1, 3, ' ', 0)
	fmt.Fprint(w, "NAME\tIpRange\tDriver\n")
	for _, nw := range networks {
		fmt.Fprintf(w, "%s\t%s\t%s\n",
			nw.Name,
			nw.IpRange.String(),
			nw.Driver,
		)
	}
	if err := w.Flush(); err != nil {
		logrus.Errorf("Flush error %v", err)
		return
	}
}

func DeleteNetwork(networkName string) error {
	nw, ok := networks[networkName]
	if !ok {
		return fmt.Errorf("No Such Network: %s", networkName)
	}

	if err := ipAllocator.Release(nw.IpRange, &nw.IpRange.IP); err != nil {
		return fmt.Errorf("Error Remove Network gateway ip: %s", err)
	}

	if err := drivers[nw.Driver].Delete(*nw); err != nil {
		return fmt.Errorf("Error Remove Network DriveError: %s", err)
	}

	// 从网络的配置目录中删除该网络对应的配置文件
	return nw.remove(defaultNetworkPath)
}

// 将容器的网络端点加入到容器的网络空间中
// 并锁定当前程序所执行的线程, 使当前线程进入到容器的网络空间
// 返回值是一个函数指针， 执行这个返回函数才会退出容器的网络空间, 回归到宿主机的网络空间
// 这个函数中引用了之前介绍的 github.com/vishvananda/netns 类库来做 Namespace 操作
func enterContainerNetns(enLink *netlink.Link, cinfo *container.ContainerInfo) func() {
	// 而 ContaInerinfo 中的 PID, 即容器在宿主机上映射的进程 ID
	f, err := os.OpenFile(fmt.Sprintf("/proc/%s/ns/net", cinfo.Pid), os.O_RDONLY, 0)
	if err != nil {
		logrus.Errorf("error get container net namespace, %v", err)
	}

	nsFD := f.Fd()
	runtime.LockOSThread()

	// 修改 veth peer 另外一端移到容器的 namespace 中
	if err = netlink.LinkSetNsFd(*enLink, int(nsFD)); err != nil {
		logrus.Errorf("error set link netns , %v", err)
	}

	// 获取当前的网络 namespace
	origns, err := netns.Get()
	if err != nil {
		logrus.Errorf("error get current netns, %v", err)
	}

	// 设置当前进程到新的网络 namespace，并在函数执行完成之后再恢复到之前的 namespace
	if err = netns.Set(netns.NsHandle(nsFD)); err != nil {
		logrus.Errorf("error set netns, %v", err)
	}
	return func() {
		netns.Set(origns)
		origns.Close()
		runtime.UnlockOSThread()
		f.Close()
	}
}

// 配置容器网络端点的地址和路由
func configEndpointIpAddressAndRoute(ep *Endpoint, cinfo *container.ContainerInfo) error {
	// 通过网络端点中 "Veth" 的另一端
	peerLink, err := netlink.LinkByName(ep.Device.PeerName)
	if err != nil {
		return fmt.Errorf("fail config endpoint: %v", err)
	}

	// 将容器的网络端点加入到容器的网络空间中
	// 并使这个函数下面的操作都在这个网络空间中进行
	// 执行完函数后, 恢复为默认的网络空间
	defer enterContainerNetns(&peerLink, cinfo)()

	// 获取到容器的 IP 地址及网段，用于配置容器内部接口地址
	// 比如容器 IP 是 192.168.1.2 ， 而网络的网段是 192.168.1.0/24
	// 那么这里产出的 IP 字符串就是 192.168.1.2/24, 用于容器内 Veth 端点配置
	interfaceIP := *ep.Network.IpRange
	interfaceIP.IP = ep.IPAddress

	// 调用 setinterfaceIP 函数设置容器内 Veth 端点的 IP
	// 这个函数， 在上一节配置 Bridge 时有介绍其实现
	if err = setInterfaceIP(ep.Device.PeerName, interfaceIP.String()); err != nil {
		return fmt.Errorf("%v,%s", ep.Network, err)
	}

	// 启动容器内的 Veth 端点
	if err = setInterfaceUP(ep.Device.PeerName); err != nil {
		return err
	}

	// Net Namespace 中默认本地地址 127.0.0.1 的 "lo" 网卡是关闭状态的
	if err = setInterfaceUP("lo"); err != nil {
		return err
	}

	// 设置容器内的外部请求都通过容器内 的 Veth 端点访问
	// 0.0.0.0/0 的网段, 表示所有的 IP 地址段
	_, cidr, _ := net.ParseCIDR("0.0.0.0/0")

	// 构建要添加的路由数据， 包括网络设备, 网关 IP 及目的网段
	// 相当于 route add -net 0.0.0.0/0 gw {Bridge 网桥地址｝dev｛容器内的 Veth 端点设备｝
	defaultRoute := &netlink.Route{
		LinkIndex: peerLink.Attrs().Index,
		Gw:        ep.Network.IpRange.IP,
		Dst:       cidr,
	}

	// 调用 netlink 的 RouteAdd，添加路由到容器的网络空间
	// RouteAdd 函数相当于 route add 命令
	if err = netlink.RouteAdd(defaultRoute); err != nil {
		return err
	}

	return nil
}

func configPortMapping(ep *Endpoint, cinfo *container.ContainerInfo) error {
	for _, pm := range ep.PortMapping {
		portMapping := strings.Split(pm, ":")
		if len(portMapping) != 2 {
			logrus.Errorf("port mapping format error, %v", pm)
			continue
		}
		iptablesCmd := fmt.Sprintf("-t nat -A PREROUTING -p tcp -m tcp --dport %s -j DNAT --to-destination %s:%s",
			portMapping[0], ep.IPAddress.String(), portMapping[1])
		cmd := exec.Command("iptables", strings.Split(iptablesCmd, " ")...)
		output, err := cmd.Output()
		if err != nil {
			logrus.Errorf("iptables Output, %v", output)
			continue
		}
	}
	return nil
}

func Connect(networkName string, cinfo *container.ContainerInfo) error {
	network, ok := networks[networkName]
	if !ok {
		return fmt.Errorf("No such Network: %s", networkName)
	}

	// 分配容器 IP 地址
	ip, err := ipAllocator.Allocate(network.IpRange)
	if err != nil {
		return err
	}

	// 创建网络端点
	ep := &Endpoint{
		ID:          fmt.Sprintf("%s-%s", cinfo.Id, networkName),
		IPAddress:   ip,
		Network:     network,
		PortMapping: cinfo.PortMapping,
	}

	// 调用网络驱动挂载和配置网络端点
	if err = drivers[network.Driver].Connect(network, ep); err != nil {
		return err
	}

	// 到容器的 namespace 配置容器网络设备 IP 地址
	if err = configEndpointIpAddressAndRoute(ep, cinfo); err != nil {
		return err
	}

	// 配置端口映射信息, 例如 mydocker run -p 8080 : 80
	return configPortMapping(ep, cinfo)
}
