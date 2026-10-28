package storage

import (
	"slices"
	"strings"
)

// Job 状态集合的唯一来源（§3.1-11）。状态既被 Go 代码判断，也被 SQL（触发器与对账）
// 使用：SQLite 的触发器是 DDL，无法绑定参数，因此这里把同一份集合同时用于生成触发器
// 文本与查询里的 IN 子句。新增/调整状态时只需要改这一处 + models.go 的常量，并有
// TestJobStatusSetsAreSingleSource 断言集合仍然严密划分全部状态。
//
// 注意：本文件的变量只应被读取，不要从外部修改（导出是为了让 jobs/httpapi 等包的
// 状态判断不各自硬编码）。
var (
	// ActiveJobStatuses 是"任务仍在进行中"的状态集合：占用调度槽位、计入 Dashboard
	// 的 active 计数、不允许取消/重试。
	ActiveJobStatuses = []JobStatus{JobPending, JobResolving, JobDownloading}
	// InterruptedJobStatuses 是进程重启后应该回退为 pending 的状态。
	InterruptedJobStatuses = []JobStatus{JobResolving, JobDownloading}
	// CompletedJobStatuses 是"成功结束"的状态集合。
	CompletedJobStatuses = []JobStatus{JobCompleted}
	// FailedJobStatuses 是"失败结束"的状态集合（partially failed 也算失败）。
	FailedJobStatuses = []JobStatus{JobFailed, JobCompletedWithErrors}
	// TerminalJobStatuses 是全部终态：进入终态后状态字段不可再被常规更新覆盖。
	TerminalJobStatuses = []JobStatus{JobCompleted, JobCompletedWithErrors, JobFailed, JobCanceled}
	// AllJobStatuses 是全部已定义状态，用于断言集合划分的完整性。
	AllJobStatuses = []JobStatus{
		JobPending, JobResolving, JobDownloading,
		JobCompleted, JobCompletedWithErrors, JobFailed, JobCanceled,
	}
)

// AutomaticFailedRetryInput 是"自动重试失败推文"任务的去重键（job.input）。jobs 包与
// storage.ReserveAutomaticRetryJob / PruneAutomaticRetryJobs 共用同一常量，改名不会再
// 静默破坏任务复用与去重语义。
const AutomaticFailedRetryInput = "automatic-failed-tweets"

// jobStatusesSQL 把状态集合渲染为 SQL 字面量列表（'pending', 'resolving', ...）。
// 仅用于拼接内部常量；状态值是本包固定的枚举，不来自用户输入。
func jobStatusesSQL(statuses []JobStatus) string {
	parts := make([]string, len(statuses))
	for i, status := range statuses {
		parts[i] = "'" + strings.ReplaceAll(string(status), "'", "''") + "'"
	}
	return strings.Join(parts, ", ")
}

// 预渲染的 SQL 片段。触发器（DDL）无法绑定参数，这里生成一次复用，避免多处手写字面量。
var (
	activeJobStatusesSQL   = jobStatusesSQL(ActiveJobStatuses)
	interruptedStatusesSQL = jobStatusesSQL(InterruptedJobStatuses)
	completedStatusesSQL   = jobStatusesSQL(CompletedJobStatuses)
	failedJobStatusesSQL   = jobStatusesSQL(FailedJobStatuses)
	terminalStatusesSQL    = jobStatusesSQL(TerminalJobStatuses)
)

// IsActiveJobStatus 报告任务是否仍处于活动状态。
func IsActiveJobStatus(status JobStatus) bool {
	return slices.Contains(ActiveJobStatuses, status)
}

// IsTerminalJobStatus 报告任务是否已进入终态。
func IsTerminalJobStatus(status JobStatus) bool {
	return slices.Contains(TerminalJobStatuses, status)
}
