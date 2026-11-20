package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"meerkat/desktop/internal/update"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const uninstallRegistryPath = `Software\Microsoft\Windows\CurrentVersion\Uninstall\Meerkat`

func windowsInstalledDirectory() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	dir, _, err := queryInstalledLocation()
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(filepath.Join(dir, "Meerkat.exe"), exe) {
		return "", fmt.Errorf("当前程序路径与 NSIS 安装记录不一致")
	}
	return dir, checkUpdateDirectory(dir)
}

func queryInstalledLocation() (dir, version string, err error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, uninstallRegistryPath, registry.QUERY_VALUE)
	if err != nil {
		return "", "", err
	}
	defer key.Close()
	dir, _, err = key.GetStringValue("InstallLocation")
	if err != nil || !filepath.IsAbs(dir) {
		return "", "", fmt.Errorf("NSIS 安装记录路径无效")
	}
	version, _, err = key.GetStringValue("DisplayVersion")
	return filepath.Clean(dir), version, err
}

func checkUpdateDirectory(dir string) error {
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil || !strings.EqualFold(dir, resolved) {
		return fmt.Errorf("安装目录不可访问或经过链接：%s", dir)
	}
	for _, name := range []string{"Meerkat.exe", "meerkat-edge.exe", "Uninstall.exe"} {
		info, err := os.Lstat(filepath.Join(dir, name))
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("安装目录缺少普通文件：%s", name)
		}
	}
	return nil
}

func finishUpdateBackup(backup string) error {
	entries, err := os.ReadDir(backup)
	if err != nil {
		return err
	}
	// Archive the whole directory if it contains user files or files from
	// another installer, without loading them into the new installation.
	if len(entries) != 3 {
		archive, err := os.MkdirTemp(filepath.Dir(backup), "Meerkat-update-backup-")
		if err != nil {
			return err
		}
		if err := renameUpdatePath(backup, filepath.Join(archive, "Meerkat")); err != nil {
			_ = os.Remove(archive)
			return err
		}
		return nil
	}
	return os.RemoveAll(backup)
}

func validUpdateStaging(staging string) bool {
	root, err := filepath.Abs(updateDir())
	return err == nil && strings.EqualFold(filepath.Dir(staging), root) && strings.HasPrefix(filepath.Base(staging), "download-")
}

func updateGuardPath() string { return filepath.Join(updateDir(), "installer-lock") }

func checkUpdateGuard() error {
	if state, err := readPendingUpdate(); err == nil && state != nil && state.isNewApp() {
		return nil
	}
	lock, err := acquireInstanceLock(updateGuardPath())
	if err != nil {
		return fmt.Errorf("Meerkat 正在安装更新：%w", err)
	}
	return lock.Close()
}

func startWindowsUpdate(staging string) error {
	dir, err := windowsInstalledDirectory()
	if err != nil {
		return err
	}
	if _, err := os.Lstat(dir + ".old"); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("上一次更新的备份仍在：%s.old", dir)
	}
	if _, err := os.Lstat(pendingUpdatePath()); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("上一次更新仍待恢复")
	}
	// Keep recovery executable across reboots, independent of TEMP cleanup.
	helperPath := updateHelperPath()
	if err := os.Remove(helperPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	helper, err := os.OpenFile(helperPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
	if err != nil {
		return err
	}
	accepted := false
	defer func() {
		if !accepted {
			_ = os.Remove(helper.Name())
		}
	}()
	source, err := os.Open(filepath.Join(dir, "Meerkat.exe"))
	if err != nil {
		_ = helper.Close()
		return err
	}
	_, copyErr := io.Copy(helper, source)
	_ = source.Close()
	if err := errors.Join(copyErr, helper.Close()); err != nil {
		return err
	}
	processIDs := []int{os.Getpid()}
	if edgeProcess != nil && edgeProcess.Process != nil {
		select {
		case <-edgeDone:
		default:
			processIDs = append(processIDs, edgeProcess.Process.Pid)
		}
	}
	// Inherit open handles instead of reopening by PID after the parent exits.
	// Windows retains the process objects even when the PID can be recycled.
	var handles []syscall.Handle
	args := []string{"--apply-update", dir, staging}
	for _, pid := range processIDs {
		handle, err := windows.OpenProcess(windows.SYNCHRONIZE, true, uint32(pid))
		if err != nil {
			return fmt.Errorf("打开旧进程 %d 失败：%w", pid, err)
		}
		defer windows.CloseHandle(handle)
		handles = append(handles, syscall.Handle(handle))
		args = append(args, strconv.FormatUint(uint64(handle), 10))
	}
	cmd := exec.Command(helper.Name(), args...)
	cmd.Dir = filepath.Dir(helper.Name())
	hideConsole(cmd)
	cmd.SysProcAttr.AdditionalInheritedHandles = handles
	if err := cmd.Start(); err != nil {
		return err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case err := <-exited:
			return fmt.Errorf("更新辅助程序未接收交接：%v", err)
		case <-timer.C:
			_ = cmd.Process.Kill()
			<-exited
			return fmt.Errorf("更新辅助程序接收交接超时")
		case <-tick.C:
			if _, err := os.Stat(filepath.Join(staging, "accepted")); err == nil {
				accepted = true
				return nil
			}
		}
	}
}

type readyUpdate struct {
	PID     int    `json:"pid"`
	Version string `json:"version"`
}

func runUpdateHelper() (bool, error) {
	if len(os.Args) >= 2 && os.Args[1] == "--recover-update" {
		return true, runRecoveryHelper()
	}
	if len(os.Args) < 2 || os.Args[1] != "--apply-update" {
		return false, nil
	}
	if len(os.Args) < 5 || len(os.Args) > 6 || !validUpdateStaging(os.Args[3]) {
		return true, fmt.Errorf("更新辅助程序的参数无效")
	}
	dir, staging := os.Args[2], os.Args[3]
	guard, err := acquireInstanceLock(updateGuardPath())
	if err != nil {
		return true, err
	}
	defer guard.Close()
	logFile, err := os.OpenFile(filepath.Join(updateDir(), "install.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return true, err
	}
	defer logFile.Close()
	logger := log.New(logFile, "", log.LstdFlags)
	// Rollback starts the old app after the update guard is released.
	restartOld := false
	defer func() {
		_ = guard.Close()
		if restartOld {
			if cmd, err := launchDesktop(dir); err != nil {
				logger.Printf("restart old: %v", err)
			} else {
				_ = cmd.Process.Release()
			}
		}
	}()
	manifestData, err := os.ReadFile(filepath.Join(staging, "latest.json"))
	if err != nil {
		return true, err
	}
	manifest, err := update.Parse(manifestData, updateKey())
	if err != nil || !update.Newer(manifest.Version, appVersion) {
		return true, fmt.Errorf("更新包清单无效或版本未提高")
	}
	result := func(err error) {
		message := "已更新到 " + manifest.Version
		if err != nil {
			message = "更新 " + manifest.Version + " 失败：" + err.Error()
			_ = os.WriteFile(filepath.Join(updateDir(), "failed-version"), []byte(manifest.Version), 0o600)
		} else {
			_ = os.Remove(filepath.Join(updateDir(), "failed-version"))
		}
		logger.Print(message)
		_ = os.WriteFile(filepath.Join(updateDir(), "last-result.txt"), []byte(message), 0o600)
	}
	var handles []windows.Handle
	for _, raw := range os.Args[4:] {
		value, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || value == 0 {
			return true, fmt.Errorf("旧进程句柄无效")
		}
		handle := windows.Handle(value)
		handles = append(handles, handle)
		defer windows.CloseHandle(handle)
	}
	if err := os.WriteFile(filepath.Join(staging, "accepted"), nil, 0o600); err != nil {
		return true, err
	}
	defer func() {
		if _, err := os.Stat(pendingUpdatePath()); errors.Is(err, os.ErrNotExist) {
			_ = os.RemoveAll(staging)
		}
	}()
	deadline := time.Now().Add(30 * time.Second)
	for _, handle := range handles {
		remaining := max(0, time.Until(deadline).Milliseconds())
		state, err := windows.WaitForSingleObject(handle, uint32(remaining))
		if err != nil || state != windows.WAIT_OBJECT_0 {
			result(fmt.Errorf("等待旧进程退出超时或失败，旧版文件保留 (wait=%d, err=%v)", state, err))
			return true, nil
		}
	}
	lock, err := acquireInstanceLock(configDir())
	if err != nil {
		restartOld = false
		result(fmt.Errorf("旧版实例锁仍被占用，取消更新：%w", err))
		return true, nil
	}
	_ = lock.Close()
	restartOld = true
	logger.Print("old Meerkat and Edge exited; instance lock available")
	transaction, err := beginUpdateRecovery(dir, staging, manifest.Version)
	if err != nil {
		result(fmt.Errorf("无法设置更新恢复：%w", err))
		return true, nil
	}
	commit := func() error {
		if err := setInstalledVersion(manifest.Version); err != nil {
			return err
		}
		marker, err := os.OpenFile(filepath.Join(staging, "committed"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			err = errors.Join(marker.Sync(), marker.Close())
		}
		if err != nil {
			_ = os.Remove(filepath.Join(staging, "committed"))
			return errors.Join(err, setInstalledVersion(transaction.Old))
		}
		return nil
	}
	loadConfig()
	installErr := applyWindowsUpdate(dir, staging, manifest, logger, launchDesktop, commit)
	if _, err := os.Stat(dir + ".old"); errors.Is(err, os.ErrNotExist) {
		if err := clearPendingUpdate(); err != nil {
			logger.Printf("清理更新恢复记录失败：%v", err)
		}
	} else if installErr != nil {
		// Leave both the backup and recovery record for a later attempt.
		restartOld = false
	}
	if installErr == nil {
		restartOld = false
	}
	result(installErr)
	return true, nil
}

func applyWindowsUpdate(dir, staging string, manifest update.Manifest, logger *log.Logger,
	launch func(string, ...string) (*exec.Cmd, error), commit func() error,
) (err error) {
	if err := checkUpdateDirectory(dir); err != nil {
		return err
	}
	platform, err := manifest.Package("windows", "amd64")
	if err != nil {
		return err
	}
	installer := update.PackagePath(staging)
	if err := update.VerifyFile(installer, platform.SHA256); err != nil {
		return err
	}
	backup := dir + ".old"
	if _, err := os.Lstat(backup); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("旧版备份已存在：%s", backup)
	}
	if err := renameUpdatePath(dir, backup); err != nil {
		return fmt.Errorf("无法搬移旧安装目录，旧版文件保留：%w", err)
	}
	logger.Printf("backup created: %s", backup)
	newAppStarted := false
	defer func() {
		if err == nil {
			if cleanupErr := finishUpdateBackup(backup); cleanupErr != nil {
				logger.Printf("新版本已启动，清理旧版备份失败：%v", cleanupErr)
			}
			return
		}
		if newAppStarted {
			if waitErr := waitForUpdateEdgeExit(); waitErr != nil {
				err = errors.Join(err, waitErr)
				return
			}
		}
		if rollbackErr := restoreOldUpdate(dir, backup); rollbackErr != nil {
			err = errors.Join(err, fmt.Errorf("回滚失败，旧版备份位于 %s：%w", backup, rollbackErr))
			return
		}
		logger.Print("rollback: old application restored")
	}()
	cmd := exec.Command(installer)
	hideConsole(cmd)
	// NSIS parses /D itself. Its final argument must remain unquoted, even
	// when the directory contains spaces.
	cmd.SysProcAttr.CmdLine = syscall.EscapeArg(installer) + " /S /UPDATE /D=" + dir
	cmd.Dir = staging
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("NSIS 安装失败：%w", err)
	}
	logger.Print("installer exited successfully")
	if err := checkUpdateDirectory(dir); err != nil {
		return err
	}
	child, err := launch(dir, "--update-ready", staging)
	if err != nil {
		return err
	}
	newAppStarted = true
	exited := make(chan error, 1)
	go func() { exited <- child.Wait() }()
	deadline := time.NewTimer(60 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case err := <-exited:
			return fmt.Errorf("新版尚未就绪便退出：%v", err)
		case <-deadline.C:
			_ = child.Process.Kill()
			<-exited
			return fmt.Errorf("等待新版就绪超时")
		case <-tick.C:
			var ready readyUpdate
			data, err := os.ReadFile(filepath.Join(staging, "ready.json"))
			if err == nil && json.Unmarshal(data, &ready) == nil && ready.PID == child.Process.Pid && ready.Version == manifest.Version {
				if err := commit(); err != nil {
					_ = child.Process.Kill()
					<-exited
					return err
				}
				logger.Printf("ready: version=%s pid=%d", ready.Version, ready.PID)
				return nil
			}
		}
	}
}

func waitForUpdateEdgeExit() error {
	if firstRunPending() {
		return nil
	}
	file, err := os.OpenFile(filepath.Join(edgeStateDirectory(), "run.lock"), os.O_RDWR, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	deadline := time.Now().Add(15 * time.Second)
	for {
		locked, err := lockFileExclusiveNB(file)
		if err != nil {
			return err
		}
		if locked {
			return unlockFile(file)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("新版 Edge 尚未退出，保留旧版备份等待恢复")
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func setInstalledVersion(version string) error {
	key, err := registry.OpenKey(registry.CURRENT_USER, uninstallRegistryPath, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	return key.SetStringValue("DisplayVersion", version)
}

func restoreOldUpdate(dir, backup string) error {
	if _, err := os.Lstat(dir); err == nil {
		// Quarantine the failed install before restoring the old one. If the
		// rename is blocked the original backup remains intact.
		failed, err := os.MkdirTemp(filepath.Dir(dir), "Meerkat-update-failed-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(failed)
		if err := renameUpdatePath(dir, filepath.Join(failed, "app")); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return renameUpdatePath(backup, dir)
}

func renameUpdatePath(from, to string) error {
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		if err = os.Rename(from, to); err == nil {
			return nil
		}
		if attempt < 4 {
			time.Sleep(time.Second)
		}
	}
	return err
}

func launchDesktop(dir string, args ...string) (*exec.Cmd, error) {
	cmd := exec.Command(filepath.Join(dir, "Meerkat.exe"), args...)
	cmd.Dir = configDir()
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}

func automaticUpdateReady() bool {
	if firstRunPending() {
		// Nothing has been started in this workspace yet.
		return true
	}
	lastInput := struct{ size, tick uint32 }{size: 8}
	user := windows.NewLazySystemDLL("user32.dll")
	ok, _, _ := user.NewProc("GetLastInputInfo").Call(uintptr(unsafe.Pointer(&lastInput)))
	kernel := windows.NewLazySystemDLL("kernel32.dll")
	now, _, _ := kernel.NewProc("GetTickCount").Call()
	return ok != 0 && uint32(now)-lastInput.tick >= 120_000 && noTrackedUpdateWork()
}
