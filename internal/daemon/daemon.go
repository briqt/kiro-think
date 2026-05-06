package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"github.com/briqt/kiro-think/internal/config"
)

func readPid() (int, error) {
	data, err := os.ReadFile(config.PidPath())
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(data)))
}

func writePid(pid int) error {
	return os.WriteFile(config.PidPath(), []byte(strconv.Itoa(pid)+"\n"), 0644)
}

func removePid() {
	os.Remove(config.PidPath())
}

// IsRunning checks if the daemon is alive.
func IsRunning() (int, bool) {
	pid, err := readPid()
	if err != nil {
		return 0, false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return 0, false
	}
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		return 0, false
	}
	return pid, true
}

// Start launches the daemon by re-executing with "run" subcommand.
func Start() error {
	if pid, running := IsRunning(); running {
		return fmt.Errorf("already running (pid %d)", pid)
	}

	exePath, err := os.Executable()
	if err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logFile := cfg.LogFile
	if logFile == "" {
		logFile = os.DevNull
	}

	lf, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	defer lf.Close()

	cmd := exec.Command(exePath, "run")
	cmd.Stdout = lf
	cmd.Stderr = lf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		return err
	}

	writePid(cmd.Process.Pid)
	fmt.Printf("started (pid %d)\n", cmd.Process.Pid)
	// Detach — don't wait
	cmd.Process.Release()
	return nil
}

// Stop sends SIGTERM to the daemon.
func Stop() error {
	pid, running := IsRunning()
	if !running {
		removePid()
		return fmt.Errorf("not running")
	}
	proc, _ := os.FindProcess(pid)
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return err
	}
	removePid()
	fmt.Printf("stopped (pid %d)\n", pid)
	return nil
}

// WritePidSelf writes the current process PID.
func WritePidSelf() {
	writePid(os.Getpid())
}

// RemovePid removes the PID file.
func RemovePid() {
	removePid()
}

// SendHUP sends SIGHUP to the running daemon for config reload.
func SendHUP() error {
	pid, running := IsRunning()
	if !running {
		return fmt.Errorf("not running")
	}
	proc, _ := os.FindProcess(pid)
	return proc.Signal(syscall.SIGHUP)
}
