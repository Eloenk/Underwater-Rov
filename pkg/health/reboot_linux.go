package health

import (
	"os/exec"
	"syscall"
)

func sysReboot() error {
	err := syscall.Reboot(syscall.LINUX_REBOOT_CMD_RESTART)
	if err != nil {
		return exec.Command("reboot").Run()
	}
	return nil
}

func sysShutdown() error {
	err := syscall.Reboot(syscall.LINUX_REBOOT_CMD_POWER_OFF)
	if err != nil {
		return exec.Command("shutdown", "-h", "now").Run()
	}
	return nil
}

