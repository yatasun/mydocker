package main

import (
	"encoding/json"
	"fmt"
	"mydocker/container"
	"os"
	"os/exec"
	"strings"

	_ "mydocker/nsenter"

	"github.com/sirupsen/logrus"
)

const ENV_EXEC_PID = "mydocker_pid"
const ENV_EXEC_CMD = "mydocker_cmd"

func ExecContainer(containerName string, comArray []string) {
	pid, err := GetContainerPidByName(containerName)
	if err != nil {
		logrus.Errorf("Exec container getContainerPidByName %s error %v", containerName, err)
		return
	}
	cmdStr := strings.Join(comArray, " ")
	logrus.Infof("container pid %s", pid)
	logrus.Infof("command %s", cmdStr)

	// 目的是 nsenter, 进入目标容器的 namespace
	cmd := exec.Command("/proc/self/exe", "exec")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	os.Setenv(ENV_EXEC_PID, pid)
	os.Setenv(ENV_EXEC_CMD, cmdStr)
	containerEnvs := getEnvsByPid(pid)
	cmd.Env = append(os.Environ(), containerEnvs...)

	if err := cmd.Run(); err != nil {
		logrus.Errorf("Exec container %s error %v", containerName, err)
	}
}

func GetContainerPidByName(containerName string) (string, error) {
	dirURL := fmt.Sprintf(container.DefaultInfoLocation, containerName)
	configFilePath := dirURL + container.ConfigName
	contentBytes, err := os.ReadFile(configFilePath)
	if err != nil {
		return "", err
	}
	var containerInfo container.ContainerInfo
	if err := json.Unmarshal(contentBytes, &containerInfo); err != nil {
		return "", err
	}
	return containerInfo.Pid, nil
}

func getEnvsByPid(pid string) []string {
	path := fmt.Sprintf("/proc/%s/environ", pid)
	contentBytes, err := os.ReadFile(path)
	if err != nil {
		logrus.Errorf("Read file %s error %v", path, err)
		return nil
	}
	// env split by \u0000
	envs := strings.Split(string(contentBytes), "\u0000")
	return envs
}
