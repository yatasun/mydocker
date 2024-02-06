package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"mydocker/cgroups"
	"mydocker/cgroups/subsystems"
	"mydocker/container"
	"mydocker/network"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
)

func Run(tty bool, comArray []string, res *subsystems.ResourceConfig, containerName, volume, imageName string, envSlice []string, nw string, portmapping []string) {
	containerID := randStringBytes(10)
	if containerName == "" {
		containerName = containerID
	}

	parent, writePipe := container.NewParentProcess(tty, containerName, volume, imageName, envSlice)
	if parent == nil {
		logrus.Errorf("New parent process error")
		return
	}
	// Start 开始进入子进程
	if err := parent.Start(); err != nil {
		logrus.Error(err)
	}

	containerName, err := recordContainerInfo(parent.Process.Pid, comArray, containerName)
	if err != nil {
		logrus.Errorf("Record container info error %v", err)
		return
	}

	cgroupManager := cgroups.NewCgroupManager("mydocker-cgroup")
	defer cgroupManager.Destroy()
	cgroupManager.Set(res)
	cgroupManager.Apply(parent.Process.Pid)

	if nw != "" {
		network.Init()
		containerInfo := &container.ContainerInfo{
			Id:          containerID,
			Pid:         strconv.Itoa(parent.Process.Pid),
			Name:        containerName,
			PortMapping: portmapping,
		}
		if err := network.Connect(nw, containerInfo); err != nil {
			logrus.Errorf("Error Connect Network %v", err)
			return
		}
	}

	sendInitCommand(comArray, writePipe)
	if tty {
		parent.Wait()
		deleteContainerInfo(containerName)
		container.DeleteWorkSpace(volume, containerName)
	}
}

func sendInitCommand(comArray []string, writePipe *os.File) {
	command := strings.Join(comArray, " ")
	logrus.Infof("command all is %s", command)
	// 供后面子进程读取
	_, _ = writePipe.WriteString(command)
	writePipe.Close()
}

func recordContainerInfo(containerPID int, commandArray []string, containerName string) (string, error) {
	id := randStringBytes(10)
	createTime := time.Now().Format("2006-01-02 15:04:05")
	command := strings.Join(commandArray, "")
	if containerName == "" {
		containerName = id
	}
	containerInfo := &container.ContainerInfo{
		Id:          id,
		Pid:         strconv.Itoa(containerPID),
		Command:     command,
		CreatedTime: createTime,
		Status:      container.RUNNING,
		Name:        containerName,
	}

	jsonBytes, err := json.Marshal(containerInfo)
	if err != nil {
		logrus.Errorf("Record container info error %v", err)
		return "", err
	}
	jsonStr := string(jsonBytes)

	dirUrl := fmt.Sprintf(container.DefaultInfoLocation, containerName)
	if err := os.MkdirAll(dirUrl, 0622); err != nil {
		logrus.Errorf("Mkdir error %s error %v", dirUrl, err)
		return "", err
	}

	fileName := dirUrl + "/" + container.ConfigName
	file, err := os.Create(fileName)
	defer file.Close()
	if err != nil {
		logrus.Errorf("Create file %s error %v", fileName, err)
		return "", err
	}

	if _, err := file.WriteString(jsonStr); err != nil {
		logrus.Errorf("File write string error %v", err)
		return "", err
	}

	return containerName, nil
}

func deleteContainerInfo(containerId string) {
	dirURL := fmt.Sprintf(container.DefaultInfoLocation, containerId)
	if err := os.RemoveAll(dirURL); err != nil {
		logrus.Errorf("Remove dir %s error %v", dirURL, err)
	}
}

func randStringBytes(n int) string {
	letterBytes := "1234567890"
	rand.Seed(time.Now().UnixNano())
	b := make([]byte, n)
	for i := range b {
		b[i] = letterBytes[rand.Intn(len(letterBytes))]
	}
	return string(b)
}
