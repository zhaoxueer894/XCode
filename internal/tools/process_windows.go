//go:build windows && (amd64 || arm64)

package tools

import (
	"fmt"
	"os/exec"
	"syscall"
	"unsafe"
)

// Windows 内核 API 只来自系统 KnownDLL，不从工作区加载动态库。
var (
	jobKernel    = syscall.NewLazyDLL("kernel32.dll")
	createJob    = jobKernel.NewProc("CreateJobObjectW")
	setJobInfo   = jobKernel.NewProc("SetInformationJobObject")
	assignJob    = jobKernel.NewProc("AssignProcessToJobObject")
	terminateJob = jobKernel.NewProc("TerminateJobObject")
	threadFirst  = jobKernel.NewProc("Thread32First")
	threadNext   = jobKernel.NewProc("Thread32Next")
	openThread   = jobKernel.NewProc("OpenThread")
	resumeThread = jobKernel.NewProc("ResumeThread")
)

// jobBasicLimit 与 Windows 64 位 JOBOBJECT_BASIC_LIMIT_INFORMATION 的布局对应。
type jobBasicLimit struct {
	ProcessTime     int64   // 单个进程的用户态时间限制，当前不设置。
	JobTime         int64   // 整个 Job 的用户态时间限制，当前不设置。
	Flags           uint32  // 保存限制开关；启用关闭 Job 时终止全部进程。
	MinWorkingSet   uintptr // 最小工作集，当前不设置。
	MaxWorkingSet   uintptr // 最大工作集，当前不设置。
	ActiveProcesses uint32  // 活动进程数量限制，当前不设置。
	Affinity        uintptr // CPU 亲和性，当前不设置。
	Priority        uint32  // 优先级限制，当前不设置。
	Scheduling      uint32  // 调度级别，当前不设置。
}

// jobExtendedLimit 保存传入 Windows 内核的完整限制结构。
type jobExtendedLimit struct {
	Basic             jobBasicLimit // 保存基本限制与 KILL_ON_JOB_CLOSE 标志。
	IO                [6]uint64     // 保存 Windows 保留的六个 I/O 计数字段。
	ProcessMemory     uintptr       // 单进程内存限制，当前不设置。
	JobMemory         uintptr       // Job 总内存限制，当前不设置。
	PeakProcessMemory uintptr       // 内核返回的峰值进程内存。
	PeakJobMemory     uintptr       // 内核返回的峰值 Job 内存。
}

// threadEntry 对应 THREADENTRY32，用于找到尚未运行的主线程。
type threadEntry struct {
	Size          uint32 // 保存结构体字节数，调用枚举 API 前必须设置。
	Usage         uint32 // Windows 保留字段。
	ThreadID      uint32 // 保存可用于 OpenThread 的线程标识。
	ProcessID     uint32 // 保存所属进程标识，用于匹配刚启动的命令。
	BasePriority  int32  // 保存线程基础优先级。
	DeltaPriority int32  // Windows 保留字段。
	Flags         uint32 // Windows 保留字段。
}

// configureProcess 先以挂起方式创建命令，加入禁止逃离的 Job 后才恢复主线程。
// 这样命令在受管控之前没有机会创建后代；任一步失败都会拒绝启动用户代码。
func configureProcess(command *exec.Cmd) (func() error, func(), error) {
	job, _, err := createJob.Call(0, 0)
	if job == 0 {
		return nil, nil, fmt.Errorf("create job object: %w", err)
	}
	cleanup := func() { _ = syscall.CloseHandle(syscall.Handle(job)) }
	limits := jobExtendedLimit{Basic: jobBasicLimit{Flags: 0x2000}}
	if ok, _, err := setJobInfo.Call(job, 9, uintptr(unsafe.Pointer(&limits)), unsafe.Sizeof(limits)); ok == 0 {
		cleanup()
		return nil, nil, fmt.Errorf("configure job object: %w", err)
	}
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000004, HideWindow: true}
	command.Cancel = func() error {
		// 启动挂起进程与 Job 绑定之间也可能收到取消，因此同时终止直接子进程。
		_, _, _ = terminateJob.Call(job, 1)
		if command.Process != nil {
			return command.Process.Kill()
		}
		return nil
	}
	start := func() error {
		process, err := syscall.OpenProcess(0x0101, false, uint32(command.Process.Pid))
		if err != nil {
			return fmt.Errorf("open suspended process: %w", err)
		}
		defer syscall.CloseHandle(process)
		if ok, _, err := assignJob.Call(job, uintptr(process)); ok == 0 {
			return fmt.Errorf("assign process to job: %w", err)
		}
		return resumeMainThread(uint32(command.Process.Pid))
	}
	return start, cleanup, nil
}

// resumeMainThread 枚举刚创建进程的挂起线程，仅在 Job 绑定成功后恢复执行。
func resumeMainThread(pid uint32) error {
	snapshot, err := syscall.CreateToolhelp32Snapshot(0x00000004, 0)
	if err != nil {
		return err
	}
	defer syscall.CloseHandle(snapshot)
	entry := threadEntry{}
	entry.Size = uint32(unsafe.Sizeof(entry))
	ok, _, scanErr := threadFirst.Call(uintptr(snapshot), uintptr(unsafe.Pointer(&entry)))
	for ok != 0 {
		if entry.ProcessID == pid {
			thread, _, err := openThread.Call(0x0002, 0, uintptr(entry.ThreadID))
			if thread == 0 {
				return fmt.Errorf("open suspended thread: %w", err)
			}
			count, _, err := resumeThread.Call(thread)
			_ = syscall.CloseHandle(syscall.Handle(thread))
			if uint32(count) == 0xffffffff {
				return fmt.Errorf("resume suspended thread: %w", err)
			}
			return nil
		}
		entry.Size = uint32(unsafe.Sizeof(entry))
		ok, _, scanErr = threadNext.Call(uintptr(snapshot), uintptr(unsafe.Pointer(&entry)))
	}
	return fmt.Errorf("suspended main thread not found: %w", scanErr)
}
