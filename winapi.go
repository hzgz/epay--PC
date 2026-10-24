package main

// Windows API 封装（只用 syscall，无外部依赖）

import (
	"fmt"
	"runtime"
	"syscall"
	"unsafe"
)

var (
	kernel32          = syscall.NewLazyDLL("kernel32.dll")
	procOpenProcess   = kernel32.NewProc("OpenProcess")
	procCloseHandle   = kernel32.NewProc("CloseHandle")
	procVirtualAllocEx = kernel32.NewProc("VirtualAllocEx")
	procVirtualFreeEx = kernel32.NewProc("VirtualFreeEx")
	procWriteProcessMemory = kernel32.NewProc("WriteProcessMemory")
	procReadProcessMemory  = kernel32.NewProc("ReadProcessMemory")
	procVirtualProtectEx   = kernel32.NewProc("VirtualProtectEx")
	procCreateSnapshot     = kernel32.NewProc("CreateToolhelp32Snapshot")
	procProcess32First     = kernel32.NewProc("Process32FirstW")
	procProcess32Next      = kernel32.NewProc("Process32NextW")
	procGetModuleHandle    = kernel32.NewProc("GetModuleHandleW")
	procCreateThread       = kernel32.NewProc("CreateThread")
	procSleep              = kernel32.NewProc("Sleep")
)

const (
	PROCESS_ALL_ACCESS     = 0x1F0FFF
	PROCESS_VM_READ        = 0x0010
	PROCESS_QUERY_INFO     = 0x0400
	MEM_COMMIT_RESERVE     = 0x3000
	PAGE_READWRITE         = 0x04
	PAGE_EXECUTE_READWRITE = 0x40
	TH32CS_SNAPPROCESS     = 0x00000002
)

type PROCESSENTRY32W struct {
	Size            uint32
	CntUsage        uint32
	ProcessID       uint32
	DefaultHeapID   uintptr
	ModuleID        uint32
	CntThreads      uint32
	ParentProcessID uint32
	PriClassBase    int32
	Flags           uint32
	ExeFile         [260]uint16
}

func PidByName(name string) uint32 {
	snap, _, _ := procCreateSnapshot.Call(TH32CS_SNAPPROCESS, 0)
	if snap == 0 || snap == uintptr(syscall.InvalidHandle) {
		return 0
	}
	defer procCloseHandle.Call(snap)

	var pe PROCESSENTRY32W
	pe.Size = uint32(unsafe.Sizeof(pe))
	r, _, _ := procProcess32First.Call(snap, uintptr(unsafe.Pointer(&pe)))
	for r != 0 {
		exe := syscall.UTF16ToString(pe.ExeFile[:])
		if equalFold(exe, name) {
			return pe.ProcessID
		}
		r, _, _ = procProcess32Next.Call(snap, uintptr(unsafe.Pointer(&pe)))
	}
	return 0
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 32
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 32
		}
		if ca != cb {
			return false
		}
	}
	return true
}

type Handle uintptr

func OpenProcess(pid uint32, access uint32) (Handle, error) {
	h, _, err := procOpenProcess.Call(uintptr(access), 0, uintptr(pid))
	if h == 0 {
		return 0, fmt.Errorf("OpenProcess(%d) 失败: %v", pid, err)
	}
	return Handle(h), nil
}

func (h Handle) Close() { procCloseHandle.Call(uintptr(h)) }

func (h Handle) Alloc(size int, protect uint32) (uintptr, error) {
	a, _, err := procVirtualAllocEx.Call(uintptr(h), 0, uintptr(size), MEM_COMMIT_RESERVE, uintptr(protect))
	if a == 0 {
		return 0, fmt.Errorf("VirtualAllocEx 失败: %v", err)
	}
	return a, nil
}

func (h Handle) Write(addr uintptr, data []byte) error {
	var written uintptr
	r, _, err := procWriteProcessMemory.Call(uintptr(h), addr,
		uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)), uintptr(unsafe.Pointer(&written)))
	runtime.KeepAlive(data)
	if r == 0 {
		return fmt.Errorf("WriteProcessMemory 失败: %v", err)
	}
	if int(written) != len(data) {
		return fmt.Errorf("只写入 %d/%d 字节", written, len(data))
	}
	return nil
}

func (h Handle) Read(addr uintptr, size int) ([]byte, error) {
	buf := make([]byte, size)
	var got uintptr
	r, _, err := procReadProcessMemory.Call(uintptr(h), addr,
		uintptr(unsafe.Pointer(&buf[0])), uintptr(size), uintptr(unsafe.Pointer(&got)))
	// 必须：GC 不知道系统调用仍在引用 buf
	runtime.KeepAlive(buf)
	if r == 0 {
		return nil, fmt.Errorf("ReadProcessMemory 失败(addr=0x%X,size=%d): %v", addr, size, err)
	}
	if got > uintptr(size) {
		got = uintptr(size)
	}
	return buf[:got], nil
}

func (h Handle) Protect(addr uintptr, size int, newProt uint32) (uint32, error) {
	var old uint32
	r, _, err := procVirtualProtectEx.Call(uintptr(h), addr, uintptr(size),
		uintptr(newProt), uintptr(unsafe.Pointer(&old)))
	if r == 0 {
		return 0, fmt.Errorf("VirtualProtectEx 失败: %v", err)
	}
	return old, nil
}

// ModuleBase 通过读 PE 头从 PEB 拿模块基址（简化：用 CreateToolhelp32Snapshot 的模块枚举）
func (h Handle) FindModuleBase(dllName string) (uintptr, uint32, error) {
	return findModule(uint32(h), dllName)
}

func Sleep(ms int) { procSleep.Call(uintptr(ms)) }

