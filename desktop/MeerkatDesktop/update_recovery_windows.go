package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const updateRunOnceKey = `Software\Microsoft\Windows\CurrentVersion\RunOnce`
const updateRunOnceValue = "!MeerkatUpdateRecovery"

type pendingUpdate struct {
	Directory string `json:"directory"`
	Helper    string `json:"helper"`
	Staging   string `json:"staging"`
	Old       string `json:"old_version"`
	New       string `json:"new_version"`
}

func pendingUpdatePath() string { return filepath.Join(updateDir(), "pending.json") }
func updateHelperPath() string  { return filepath.Join(updateDir(), "meerkat-update-helper.exe") }

func beginUpdateRecovery(dir, staging, newVersion string) (*pendingUpdate, error) {
	helper, err := os.Executable()
	if err != nil {
		return nil, err
	}
	registered, oldVersion, err := queryInstalledLocation()
	if err != nil || !strings.EqualFold(registered, dir) {
		return nil, fmt.Errorf("安装位置在更新前发生改变")
	}
	state := &pendingUpdate{Directory: dir, Helper: helper, Staging: staging, Old: oldVersion, New: newVersion}
	data, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(pendingUpdatePath(), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	_, writeErr := file.Write(data)
	if err := errors.Join(writeErr, file.Sync(), file.Close()); err != nil {
		_ = os.Remove(pendingUpdatePath())
		return nil, err
	}
	if err := setRecoveryLaunch(syscall.EscapeArg(helper) + " --recover-update"); err != nil {
		_ = os.Remove(pendingUpdatePath())
		return nil, err
	}
	return state, nil
}

func setRecoveryLaunch(command string) error {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, updateRunOnceKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	if command != "" {
		return key.SetStringValue(updateRunOnceValue, command)
	}
	if err := key.DeleteValue(updateRunOnceValue); !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}

func readPendingUpdate() (*pendingUpdate, error) {
	data, err := os.ReadFile(pendingUpdatePath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var state pendingUpdate
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	dir, _, err := queryInstalledLocation()
	if err != nil || !strings.EqualFold(dir, state.Directory) || !validUpdateStaging(state.Staging) ||
		!strings.EqualFold(state.Helper, updateHelperPath()) {
		return nil, fmt.Errorf("待恢复更新与当前安装记录不一致")
	}
	return &state, nil
}

func clearPendingUpdate() error {
	if err := setRecoveryLaunch(""); err != nil {
		return err
	}
	return os.Remove(pendingUpdatePath())
}

func (state *pendingUpdate) isNewApp() bool {
	if len(os.Args) != 3 || os.Args[1] != "--update-ready" || !strings.EqualFold(os.Args[2], state.Staging) || appVersion != state.New {
		return false
	}
	exe, err := os.Executable()
	return err == nil && strings.EqualFold(exe, filepath.Join(state.Directory, "Meerkat.exe"))
}

func (state *pendingUpdate) committed() bool {
	_, err := os.Stat(filepath.Join(state.Staging, "committed"))
	return err == nil
}

func (state *pendingUpdate) recover() error {
	backup := state.Directory + ".old"
	if _, err := os.Lstat(backup); err == nil {
		if state.committed() {
			// Neither the fact that a new EXE exists nor the registry version
			// alone proves this installation was verified.
			if err := checkUpdateDirectory(state.Directory); err != nil {
				return err
			}
			if err := finishUpdateBackup(backup); err != nil {
				return err
			}
		} else {
			if err := checkUpdateDirectory(backup); err != nil {
				return err
			}
			loadConfig()
			if err := waitForUpdateEdgeExit(); err != nil {
				return err
			}
			if err := restoreOldUpdate(state.Directory, backup); err != nil {
				return err
			}
			if err := setInstalledVersion(state.Old); err != nil {
				return err
			}
			_ = os.WriteFile(filepath.Join(updateDir(), "failed-version"), []byte(state.New), 0o600)
			_ = os.WriteFile(filepath.Join(updateDir(), "last-result.txt"), []byte("更新中断，已恢复旧版 "+state.Old), 0o600)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	} else if err := checkUpdateDirectory(state.Directory); err != nil {
		return err
	}
	if err := clearPendingUpdate(); err != nil {
		return err
	}
	_ = os.RemoveAll(state.Staging)
	return nil
}

// Recovery is also registered at login while the old directory is moved, so it
// can run even when an interruption left the ordinary shortcut without an EXE.
func runRecoveryHelper() error {
	guard, err := acquireInstanceLock(updateGuardPath())
	if err != nil {
		return err
	}
	defer guard.Close()
	state, err := readPendingUpdate()
	if err != nil || state == nil {
		return err
	}
	if len(os.Args) == 3 {
		value, err := strconv.ParseUint(os.Args[2], 10, 64)
		if err != nil || value == 0 {
			return fmt.Errorf("无效的恢复进程句柄")
		}
		handle := windows.Handle(value)
		defer windows.CloseHandle(handle)
		status, err := windows.WaitForSingleObject(handle, 30_000)
		if err != nil || status != windows.WAIT_OBJECT_0 {
			return fmt.Errorf("恢复前等待应用退出失败 (wait=%d, err=%v)", status, err)
		}
	} else if len(os.Args) != 2 {
		return fmt.Errorf("恢复命令参数无效")
	}
	lock, err := acquireInstanceLock(configDir())
	if err != nil {
		return err
	}
	_ = lock.Close()
	if err := state.recover(); err != nil {
		return err
	}
	_ = guard.Close()
	cmd, err := launchDesktop(state.Directory)
	if err == nil {
		_ = cmd.Process.Release()
	}
	return err
}

func recoverInterruptedUpdate() (bool, error) {
	state, err := readPendingUpdate()
	if err != nil || state == nil {
		return false, err
	}
	// The normal new process provides a readiness receipt for the active helper.
	if state.isNewApp() {
		return false, nil
	}
	exe, err := os.Executable()
	if err != nil || !strings.EqualFold(exe, filepath.Join(state.Directory, "Meerkat.exe")) {
		return false, err
	}
	guard, err := acquireInstanceLock(updateGuardPath())
	if err != nil {
		return false, err
	}
	defer guard.Close()
	if state.committed() {
		if err := state.recover(); err != nil {
			log.Printf("已完成更新的备份清理失败：%v", err)
		}
		return false, nil
	}
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, true, uint32(os.Getpid()))
	if err != nil {
		return false, err
	}
	defer windows.CloseHandle(handle)
	// Let the helper take the guard after this process relinquishes it.
	_ = guard.Close()
	cmd := exec.Command(state.Helper, "--recover-update", strconv.FormatUint(uint64(handle), 10))
	hideConsole(cmd)
	cmd.SysProcAttr.AdditionalInheritedHandles = []syscall.Handle{syscall.Handle(handle)}
	if err := cmd.Start(); err != nil {
		return false, err
	}
	_ = cmd.Process.Release()
	return true, nil
}
