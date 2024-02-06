package subsystems

// 内存限制, CPU 时间片权重, CPU 核心数
type ResourceConfig struct {
	MemoryLimit string
	CpuShare    string
	CpuSet      string
}

// 将 cgroup 抽象为 path, 原因是 cgroup 在 hierarchy 的路径, 便是虚拟文件系统中的虚拟路径
type Subsystem interface {
	Name() string
	// 设置某个 cgroup 在这个 Subsystem 中的资源限制
	Set(path string, res *ResourceConfig) error
	// 将进程添加到某个 cgroup 中
	Apply(path string, pid int) error
	// 移除某个 cgroup
	Remove(path string) error
}

var (
	SubsystemsIns = []Subsystem{
		&CpusetSubSystem{},
		&MemorySubSystem{},
		&CpuSubSystem{},
	}
)
