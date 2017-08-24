package qemu

import "os/exec"

func isHyperVInstalled() bool {
	return false
}

func isVTXDisabled() bool {
	return false
}

func isHAXMNotInstalled() bool {
	return false
}

func isDeviceGuardEnabled() bool {
	return false
}

func getQemuImgCommand(d *Driver) (string, error) {
	//TODO checks for Qemu-Img existing!
	return "qemu-img", nil
}

func getQemuCommand(d *Driver) (string, error) {
	//TODO checks for Qemu Process
	return "qemu-system-x86_64", nil
}

func getQemuAccel(d *Driver) string {
	// TODO Do Check for wanted Accel
	return "-enable-kvm"
}

func setProcAttr(cmd *exec.Cmd) {

}
