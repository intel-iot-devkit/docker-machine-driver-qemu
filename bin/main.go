package main

import (
	"github.com/docker/machine/libmachine/drivers/plugin"
	"github.com/intel-iot-devkit/docker-machine-driver-qemu"
)

func main() {
	plugin.RegisterDriver(new(qemu.Driver))
}
