package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var errUpdateDirectoryBusy = errors.New("安装目录仍被占用，旧版已保留")
var errUpdateProcessExited = errors.New("Windows 尚未释放已退出的进程。请在安装目录解除占用后重试。")

func findUpdateBlockedProcess() (*updateBlockedProcess, error) {
	dir, err := windowsInstalledDirectory()
	if err != nil {
		return nil, err
	}
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for err := windows.Process32First(snapshot, &entry); !errors.Is(err, windows.ERROR_NO_MORE_FILES); err = windows.Process32Next(snapshot, &entry) {
		if err != nil {
			return nil, err
		}
		name := windows.UTF16ToString(entry.ExeFile[:])
		if !isOtherInstalledProcess(entry.ProcessID, name) {
			continue
		}
		handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, entry.ProcessID)
		if err != nil {
			continue
		}
		process, err := identifyUpdateProcess(handle, entry.ProcessID, dir)
		windows.CloseHandle(handle)
		if err == nil {
			return process, nil
		}
	}
	return nil, nil
}

func isOtherInstalledProcess(pid uint32, name string) bool {
	if pid == uint32(os.Getpid()) {
		return false
	}
	if edgeProcess != nil && edgeProcess.Process != nil && pid == uint32(edgeProcess.Process.Pid) {
		return false
	}
	return strings.EqualFold(name, "Meerkat.exe") || strings.EqualFold(name, "meerkat-edge.exe")
}

func identifyUpdateProcess(handle windows.Handle, pid uint32, dir string) (*updateBlockedProcess, error) {
	var image [32768]uint16
	size := uint32(len(image))
	if err := windows.QueryFullProcessImageName(handle, 0, &image[0], &size); err != nil {
		return nil, err
	}
	path := filepath.Clean(windows.UTF16ToString(image[:size]))
	name := filepath.Base(path)
	if !isOtherInstalledProcess(pid, name) || !strings.EqualFold(filepath.Dir(path), dir) {
		return nil, errors.New("进程不属于当前安装")
	}
	var currentSession, processSession uint32
	if windows.ProcessIdToSessionId(uint32(os.Getpid()), &currentSession) != nil ||
		windows.ProcessIdToSessionId(pid, &processSession) != nil || currentSession != processSession {
		return nil, errors.New("进程不属于当前桌面会话")
	}
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &created, &exited, &kernel, &user); err != nil {
		return nil, err
	}
	return &updateBlockedProcess{PID: pid, Name: name, path: path, creationTime: created.Nanoseconds()}, nil
}

func endUpdateBlockedProcess(expected updateBlockedProcess) error {
	dir, err := windowsInstalledDirectory()
	if err != nil {
		return err
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, expected.PID)
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return nil
	}
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	actual, err := identifyUpdateProcess(handle, expected.PID, dir)
	if err != nil || actual.creationTime != expected.creationTime || !strings.EqualFold(actual.path, expected.path) {
		return errors.New("进程已经变化，请重新检查")
	}
	if state, err := windows.WaitForSingleObject(handle, 0); err == nil && state == windows.WAIT_OBJECT_0 {
		return errUpdateProcessExited
	}
	if err := windows.TerminateProcess(handle, 1); err != nil {
		return err
	}
	if state, err := windows.WaitForSingleObject(handle, 5_000); err != nil || state != windows.WAIT_OBJECT_0 {
		return fmt.Errorf("等待占用进程退出失败（%d，%v）", state, err)
	}
	return nil
}
