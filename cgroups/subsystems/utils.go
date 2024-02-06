package subsystems

import (
	"bufio"
	"fmt"
	"os"
	"path"
	"strings"
)

// TODO: private ?
func FindCgroupMountpoint(subsystem string) string {
	// 当前进程相关的 mount 信息
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		txt := scanner.Text()
		fields := strings.Split(txt, " ")
		// 最后一个字段是用逗号隔开的各种资源
		for _, opt := range strings.Split(fields[len(fields)-1], ",") {
			if opt == subsystem {
				// 这个字段是路径, 这个文件夹是对应的 cgroup
				return fields[4]
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return ""
	}

	return ""
}

// 得到 cgroup 在文件系统中的绝对路径
// GetCgroupPath 的作用是获取当前 subsystem 在虚拟文件系统中的路径
func GetCgroupPath(subsystem string, cgroupPath string, autoCreate bool) (string, error) {
	cgroupRoot := FindCgroupMountpoint(subsystem)
	if _, err := os.Stat(path.Join(cgroupRoot, cgroupPath)); err == nil || (autoCreate && os.IsNotExist(err)) {
		if os.IsNotExist(err) {
			// autoCreate
			if err := os.Mkdir(path.Join(cgroupRoot, cgroupPath), 0755); err == nil {
			} else {
				return "", fmt.Errorf("error crate cgroup %v", err)
			}
		}
		return path.Join(cgroupRoot, cgroupPath), nil
	} else {
		return "", fmt.Errorf("cgroup path error %v", err)
	}
}