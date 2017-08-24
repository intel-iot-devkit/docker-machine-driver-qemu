package qemu

import (
	"os/exec"
	"strings"
	"syscall"

	"github.com/docker/machine/libmachine/log"
	"golang.org/x/sys/windows/registry"
)

func isHyperVInstalled() bool {
	// From Docker-Machine Virutalbox driver
	// check if hyper-v is installed
	_, err := exec.LookPath("vmms.exe")
	if err != nil {
		errmsg := "Hyper-V is not installed."
		log.Debugf(errmsg, err)
		return false
	}

	// check to see if a hypervisor is present. if hyper-v is installed and enabled,
	// display an error explaining the incompatibility between virtualbox and hyper-v.
	output, err := exec.Command("wmic", "computersystem", "get", "hypervisorpresent").Output()

	if err != nil {
		errmsg := "Could not check to see if Hyper-V is running."
		log.Debugf(errmsg, err)
		return false
	}

	enabled := strings.Contains(string(output), "TRUE")
	return enabled
}

func isVTXDisabled() bool {
	// From Docker-Machine Virutalbox driver
	errmsg := "Couldn't check that VT-X/AMD-v is enabled. Will check that the vm is properly created: %v"
	output, err := exec.Command("wmic", "cpu", "get", "VirtualizationFirmwareEnabled").Output()
	if err != nil {
		log.Debugf(errmsg, err)
		return false
	}

	disabled := strings.Contains(string(output), "FALSE")
	return disabled
}

func isHAXMNotInstalled() bool {
	_, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Services\IntelHaxm`, registry.QUERY_VALUE)
	if err != nil {
		return true
	}
	return false
}

func isDeviceGuardEnabled() bool {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\DeviceGuard`, registry.QUERY_VALUE)
	defer key.Close()
	if err != nil {
		return false
	}
	virtsec, _, erra := key.GetIntegerValue("EnableVirtualizationBasedSecurity")
	if erra != nil {
		return false
	}
	if virtsec != 0 {
		return true
	}
	return false
}

func getQemuImgCommand(d *Driver) (string, error) {
	//TODO checks for Qemu-Img Exe existing!
	return d.QemuLocation + "\\qemu-img.exe", nil
}

func getQemuCommand(d *Driver) (string, error) {
	//TODO checks for Qemu Exe existing!
	return d.QemuLocation + "\\qemu-system-x86_64.exe", nil
}

func getQemuAccel(d *Driver) string {
	//TODO Dev Check
	return "-enable-hax"
}

func setProcAttr(cmd *exec.Cmd) {
	//Windows Specific Section!
	const CreateNewProcessGroup = 0x00000200
	const DetachedProcess = 0x00000008

	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: CreateNewProcessGroup | DetachedProcess,
	}
}
