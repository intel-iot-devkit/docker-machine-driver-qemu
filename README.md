# QEMU Docker Machine Driver Plugin
The Docker Machine plugin for QEMU enables the use of the QEMU hypervisor with Docker-Machine

## Requirements
#### Linux
* QEMU (qemu-system-x86_64 & qemu-img) in path -2.5.0+ Tested but other expected to work
* KVM available

#### Windows
* QEMU 2.9.0+
* [Intel HAXM driver](https://software.intel.com/en-us/android/articles/intel-hardware-accelerated-execution-manager)

## Install from Binary
Please see the [release tab](https://github.com/intel-iot-devkit/docker-machine-driver-qemu/releases) and place the plugin in your PATH

## Install from Source
```bash
go get github.com/intel-iot-devkit/docker-machine-driver-qemu
cd <GO-ROOT>/src/github.com/intel-iot-devkit/docker-machine-driver-qemu
GOOS=windows go build -i -o docker-machine-driver-qemu.exe ./bin
#OR
GOOS=linux go build -i -o docker-machine-driver-qemu ./bin
```
An place the binary in your path!

## Usage
The usual Docker Machine commands apply:
```bash
docker-machine create --driver qemu qemumachine
docker-machine env qemumachine
```
On Windows `QEMU_LOCATION` must be set to the location where the

## Limitations
* **Ports**: QEMU will not generally respect forwarding the network traffic to the docker-machine.
During creation, you need to explicitly state the port ranges you wish to use
For example:
``` --qemu-open-ports 8022,1111,1231-1235 ```
* **Mounts**: Using mounts into containers is not supported.
* **Concurrent usage**: One instance of a machine using QEMU driver is possible at this time. The provisioner does not handle NATd Docker Ports.


# CLI Options/Environment variables and defaults:

| CLI option                        | Environment variable   | Default                                |
|-----------------------------------|------------------------|----------------------------------------|
| `--qemu-vcpu-count`               | `QEMU_CPU_COUNT`       | `2`                                    |
| `--qemu-memory-size`              | `QEMU_MEMORY_SIZE`     | `1024`                                 |
| `--qemu-disk-size`                | `QEMU_DISK_SIZE`       | `18000` Grows with qcow2 to this limit |
| `--qemu-boot2docker-url`          | `QEMU_BOOT2DOCKER_URL` | *boot2docker URL*                      |
| `--qemu-open-ports`               | -                      | -                                      |
