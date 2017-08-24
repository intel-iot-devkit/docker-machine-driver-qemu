package qemu

import (
	"bufio"
	"errors"
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/docker/machine/libmachine/drivers"
	"github.com/docker/machine/libmachine/log"
	"github.com/docker/machine/libmachine/mcnflag"
	"github.com/docker/machine/libmachine/mcnutils"
	"github.com/docker/machine/libmachine/ssh"
	"github.com/docker/machine/libmachine/state"
	"github.com/qeedquan/iso9660"
)

//Driver driver struct
type Driver struct {
	*drivers.BaseDriver

	MonitorPort    int
	Disk           string
	DiskSize       int
	Cpus           int
	Mem            int
	QemuLocation   string
	EnginePort     int
	OpenPorts      []int
	Boot2DockerURL string
}

//DriverName name
func (d *Driver) DriverName() string {
	return "qemu"
}

//GetCreateFlags Create flags
func (d *Driver) GetCreateFlags() []mcnflag.Flag {
	return []mcnflag.Flag{
		mcnflag.IntFlag{
			Name:   "qemu-memory",
			EnvVar: "QEMU_MEMORY_SIZE",
			Usage:  "Size of memory for host in MB",
			Value:  1024,
		},
		mcnflag.IntFlag{
			Name:   "qemu-disk-size",
			EnvVar: "QEMU_DISK_SIZE",
			Usage:  "Size of disk in MB",
			Value:  18000,
		},
		mcnflag.IntFlag{
			Name:   "qemu-cpu-count",
			EnvVar: "QEMU_CPU_COUNT",
			Usage:  "Number of CPUs",
			Value:  2,
		},
		mcnflag.IntFlag{
			Name:  "qemu-monitor-port",
			Usage: "Port which Qemu monitor will be opened on.",
		},
		mcnflag.StringFlag{
			EnvVar: "QEMU_LOCATION",
			Name:   "qemu-location",
			Usage:  "The location of the qemu tools if not in Path",
		},
		mcnflag.StringSliceFlag{
			Name:  "qemu-open-ports",
			Usage: "Make the specified port number accessible from the host",
		},
		mcnflag.StringFlag{
			Name:   "qemu-boot2docker-url",
			Usage:  "URL of the boot2docker ISO. Defaults to the latest available version.",
			EnvVar: "QEMU_BOOT2DOCKER_URL",
		},
	}
}

// PreCreateCheck checks that the machine creation process can be started safely.
func (d *Driver) PreCreateCheck() error {
	//CHECK FOR haxm
	if isHAXMNotInstalled() {
		return fmt.Errorf("Intel HAXM not installed, please install it to use this driver")
	}
	//Check for VT instructions
	if isVTXDisabled() {
		return fmt.Errorf("VT-X instructions are disabled, please enabled them to use this driver")
	}
	//Check for Hyper-V
	if isHyperVInstalled() {
		return fmt.Errorf("Hyper-V is installed, please disable it to use this driver")
	}
	//Check for Windows DeviceGuard
	if isDeviceGuardEnabled() {
		return fmt.Errorf("Windows Device Credential Guard is enabled, driver cannot run")
	}

	// Downloading boot2docker to cache should be done here to make sure
	// that a download failure will not leave a machine half created.
	b2dutils := mcnutils.NewB2dUtils(d.StorePath)
	if err := b2dutils.UpdateISOCache(d.Boot2DockerURL); err != nil {
		return err
	}

	return nil
}

//Create the machiene
func (d *Driver) Create() error {

	//Copy ISO into machine directory
	b2dutils := mcnutils.NewB2dUtils(d.StorePath)
	if err := b2dutils.CopyIsoToMachineDir("", d.GetMachineName()); err != nil {
		return err
	}
	log.Infof("Creating SSH key...")
	if err := ssh.GenerateSSHKey(d.GetSSHKeyPath()); err != nil {
		return err
	}

	log.Infof("Creating Disk...")
	gen := d.ResolveStorePath("disk.raw")
	disk := d.ResolveStorePath("disk.qcow2")
	tarBuf, err := mcnutils.MakeDiskImage(d.GetSSHKeyPath() + ".pub")
	if err != nil {
		return err
	}
	file, err := os.OpenFile(gen, os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		return err
	}
	defer file.Close()

	file.Seek(0, os.SEEK_SET)
	_, err = file.Write(tarBuf.Bytes())
	if err != nil {
		return err
	}
	file.Close()

	qemuImg, err := getQemuImgCommand(d)
	if err != nil {
		return err
	}

	convert := exec.Command(qemuImg, "convert", "-f", "raw", "-O", "qcow2", gen, disk)
	err = convert.Run()
	if err != nil {
		return err
	}
	os.Remove(gen)

	var resizeString string
	resizeString = fmt.Sprintf("+%dM", d.DiskSize)
	resize := exec.Command(qemuImg, "resize", disk, resizeString)
	err = resize.Run()
	if err != nil {
		return err
	}
	d.Disk = disk

	return d.Start()
}

// Kill  machine
func (d *Driver) Kill() (err error) {
	monconn, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(d.MonitorPort))
	if err != nil {
		return err
	}
	defer monconn.Close()
	w := bufio.NewWriter(monconn)
	fmt.Fprint(w, "\nq\n")
	w.Flush()
	time.Sleep(500 * time.Millisecond)
	err = monconn.Close()
	if err != nil {
		return err
	}
	return nil
}

//Remove the machine
func (d *Driver) Remove() error {
	s, err := d.GetState()
	if err != nil {
		return err
	}
	if s != state.Stopped && s != state.Saved {
		if err := d.Kill(); err != nil {
			return err
		}

	}
	return nil
}

func getFileOutofFS(iso *iso9660.FileSystem, file string, output string) error {
	isoFile, err := iso.Open(file)
	if err != nil {
		return err
	}

	fileStat, err := isoFile.Stat()
	if err != nil {
		return err
	}
	fileBytes := make([]byte, fileStat.Size())
	readbytes, err := isoFile.Read(fileBytes)
	if err != nil {
		return err
	}
	if int64(readbytes) != fileStat.Size() {
		return errors.New("bytes read does not equal length of file")
	}

	err = ioutil.WriteFile(output, fileBytes, 0644)
	if err != nil {
		return err
	}
	return nil
}

// This function tries to extract the kernel and initrd from the ISO
func extractKernel(d *Driver) error {
	//Windows
	//Remove Kernel and initrd. //Failing is ok!
	os.Remove(d.ResolveStorePath("vmlinuz64"))
	os.Remove(d.ResolveStorePath("initrd.img"))

	isofs, err := iso9660.Open(d.ResolveStorePath("boot2docker.iso"))
	if err != nil {
		return err
	}
	getFileOutofFS(isofs, "BOOT/VMLINUZ64.;1", d.ResolveStorePath("vmlinuz64"))
	if err != nil {
		return err
	}
	getFileOutofFS(isofs, "BOOT/INITRD.IMG;1", d.ResolveStorePath("initrd.img"))
	if err != nil {
		return err
	}

	return nil

}

//Start the machine
func (d *Driver) Start() error {
	log.Debugf("Starting VM %s", d.MachineName)
	//CHECK FOR haxm
	if isHAXMNotInstalled() {
		return fmt.Errorf("Intel HAXM not installed, please install it to use this driver")
	}
	//Check for VT instructions
	if isVTXDisabled() {
		return fmt.Errorf("VT-X instructions are disabled, please enabled them to use this driver")
	}
	//Check for Hyper-V
	if isHyperVInstalled() {
		return fmt.Errorf("Hyper-V is installed, please disable it to use this driver")
	}
	//Check for Windows DeviceGuard
	if isDeviceGuardEnabled() {
		return fmt.Errorf("Windows Device Credential Guard is enabled, driver cannot run")
	}
	err := extractKernel(d)
	if err != nil {
		return err
	}

	var netString string
	netString = fmt.Sprintf("user,id=mynet0,net=192.168.76.0/24,dhcpstart=192.168.76.9,hostfwd=tcp:127.0.0.1:%d-:22,hostfwd=tcp:127.0.0.1:%d-:2376",
		d.SSHPort,
		d.EnginePort)
	for _, port := range d.OpenPorts {
		netString = fmt.Sprintf("%s,hostfwd=tcp:127.0.0.1:%d-:%d", netString, port, port)
	}

	var monString string
	monString = fmt.Sprintf("telnet:127.0.0.1:%d,server,nowait", d.MonitorPort)

	var diskString string
	diskString = fmt.Sprintf("file=%s,if=virtio", d.Disk)

	qemuCmd, err := getQemuCommand(d)
	if err != nil {
		return nil
	}

	cmd := exec.Command(qemuCmd,
		"-netdev", netString,
		"-device", "virtio-net,netdev=mynet0",
		"-boot", "d",
		"-kernel", d.ResolveStorePath("vmlinuz64"),
		"-initrd", d.ResolveStorePath("initrd.img"),
		"-append", `loglevel=3 user=docker console=ttyS0 noembed nomodeset norestore base`,
		"-m", strconv.Itoa(d.Mem),
		"-smp", strconv.Itoa(d.Cpus),
		"-drive", diskString,
		"-monitor", monString, getQemuAccel(d), "-nographic",
		"-D", d.ResolveStorePath("qemu.log"),
		"-serial", fmt.Sprintf("file:%s", d.ResolveStorePath("kern.log")))

	//Set CMD process flags
	setProcAttr(cmd)
	log.Infof("Starting VM...")
	cmd.Start()

	d.IPAddress = "127.0.0.1"
	d.SSHUser = "docker"

	//Give Qemu a few changes to get started!
	for i := 0; i < 50; i++ {
		time.Sleep(200 * time.Millisecond)
		sshconn, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(d.SSHPort))
		defer sshconn.Close()
		if err == nil {
			return nil
		}
	}
	return fmt.Errorf("Failed to startup QEMU")
}

//Stop the machine
func (d *Driver) Stop() error {
	_, err := drivers.RunSSHCommandFromDriver(d, "sudo poweroff")
	if err != nil {
		return err
	}
	time.Sleep(2 * time.Second)
	d.IPAddress = ""
	return nil
}

//SetConfigFromFlags Set the config from the flags
func (d *Driver) SetConfigFromFlags(flags drivers.DriverOptions) error {
	d.QemuLocation = flags.String("qemu-location")
	d.MonitorPort = flags.Int("qemu-monitor-port")
	d.DiskSize = flags.Int("qemu-disk-size")
	d.Cpus = flags.Int("qemu-cpu-count")
	d.Mem = flags.Int("qemu-memory")
	d.Boot2DockerURL = flags.String("qemu-boot2docker-url")

	for _, v := range flags.StringSlice("qemu-open-ports") {
		s := strings.Split(v, "-")
		if l := len(s); l == 0 || l > 2 {
			log.Errorf("defined port or range \"%s\" is not valid", v)
			break
		}
		if len(s) == 1 {
			port, err := strconv.ParseUint(v, 10, 16)
			if err != nil {
				log.Errorf("defined port \"%s\" is not valid", v)
			}
			d.OpenPorts = append(d.OpenPorts, int(port))
		}
		if len(s) == 2 {
			start, err := strconv.ParseUint(s[0], 10, 16)
			if err != nil {
				log.Errorf("defined start port range \"%s\" is not valid", s[0])
				break
			}
			stop, err := strconv.ParseUint(s[1], 10, 16)
			if err != nil {
				log.Errorf("defined start port range \"%s\" is not valid", s[1])
				break
			}
			if start >= stop {
				log.Errorf("defined port range \"%s\" is not valid", v)
				break
			}
			for i := start; i <= stop; i++ {
				d.OpenPorts = append(d.OpenPorts, int(i))
			}
		}
	}
	//Get Some ports for use to use for SSH and the QEMU MonitorPort
	sshP, err := getTCPPort(d)
	if err != nil {
		return err
	}
	d.SSHPort = sshP
	//	dockerP, err := getTCPPort(d)
	//	if err != nil {
	//		return err
	//	}
	d.EnginePort = 2376
	monP, err := getTCPPort(d)
	if err != nil {
		return err
	}
	d.MonitorPort = monP
	return nil
}

// Restart this docker-machine
func (d *Driver) Restart() error {
	_, err := drivers.RunSSHCommandFromDriver(d, "sudo shutdown -r now")
	if err != nil {
		return err
	}
	return nil
}

//GetSSHHostname get the hostname for ssh
func (d *Driver) GetSSHHostname() (string, error) {
	return d.IPAddress, nil
}

// GetState return instance status
func (d *Driver) GetState() (state.State, error) {
	sshconn, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(d.SSHPort))
	if err == nil {
		sshconn.Close()
		return state.Running, nil
	}
	monconn, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(d.MonitorPort))
	if err == nil {
		monconn.Close()
		return state.Starting, nil
	}
	d.IPAddress = ""
	return state.Stopped, nil
}

// GetURL returns docker daemon URL on this machine
func (d *Driver) GetURL() (string, error) {
	if d.IPAddress == "" {
		return "", nil
	}
	s, err := d.GetState()
	if err != nil {
		return "", err
	}
	if s != state.Running {
		return "", drivers.ErrHostIsNotRunning
	}
	return fmt.Sprintf("tcp://%s:%d", d.IPAddress, d.EnginePort), nil
}

func (d *Driver) publicSSHKeyPath() string {
	return d.GetSSHKeyPath() + ".pub"
}

//Check port is avaible.
func checkTCPPort(port int) bool {
	if (port == 0) || (port > 65535) {
		return false
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	ln.Close()
	if err != nil {
		log.Errorf("can not listen on port TCP/%d", port)
		return false
	}
	return true
}

func contains(a []int, v int) int {
	for i, iv := range a {
		if iv == v {
			return i
		}
	}
	return -1
}

// Get a TCP Port and one that the user is going to use
func getTCPPort(d *Driver) (int, error) {
	for i := 0; i <= 5; i++ {
		ln, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", 0))
		if err != nil {
			return 0, err
		}
		defer ln.Close()
		addr := ln.Addr().String()
		addrParts := strings.SplitN(addr, ":", 2)
		p, err := strconv.Atoi(addrParts[1])
		if err != nil {
			return 0, err
		}

		if contains(d.OpenPorts, p) >= 0 {
			p = 0
		}
		if p != 0 {
			return p, nil
		}
		time.Sleep(1)
	}
	return 0, fmt.Errorf("unable to allocate tcp port")
}
