// Package runner launches executors and classifies their output. Design §5 and §13.
package runner

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/OttoPrua/Cardex/internal/model"
)

// ErrNotImplemented is returned by the stub runner. The fake runner does not return it.
var ErrNotImplemented = errors.New("not implemented")

// LaunchSpec is the dispatch-time description of one attempt.
type LaunchSpec struct {
	strongFixtureInput []byte         // frozen only by the authenticated local Fake child
	AttemptID          string         // 尝试标识。
	AttemptIndex       int            // 尝试序号，fake 脚本按它选择行为。
	WorkDir            string         // 本次 worktree。
	PromptPath         string         // prompt 文件路径。
	Model              string         // 选定的模型。
	Script             []byte         // fake 执行器的 JSON 脚本；空则读 ScriptPath。
	ScriptPath         string         // JSON 脚本路径。
	SessionID          string         // goal 模式本次会话；空则不传 -s。
	CardKind           model.CardKind // API 渠道的卡类型，用来证明工具权限；旧渠道忽略。
	ConfigDir          string         // API 渠道的空 CLAUDE_CONFIG_DIR；旧渠道为空。
	DropEnv            []string       // Provider env reference names never passed to executor children.
	RelayURL           string         // API 渠道每次运行绑定的本机地址。
	RelayKey           string         // 本次运行的临时本机访问能力；不是供应商凭据。
}

// RunResult is what a fake attempt produced.
type RunResult struct {
	ExitCode  int           // 脚本要求的退出码。
	Output    []byte        // 执行器输出，可以是残缺事件流或错误文本。
	TailAfter time.Duration // 子进程写完 Output 后再等多久写 Tail。
	Tail      []byte        // Output 之后才产生的字节。
	Hold      time.Duration // 写完 Tail 后继续占着进程组的时间。
}

// Runner is one executor adapter.
type Runner interface {
	Name() string
	Capabilities() []string
	Build(spec LaunchSpec) (command []string, env []string, err error)
	NeedsPTY() bool
	Drive(ctx context.Context, rw io.ReadWriter) error
	Classify(exit int, output []byte) model.ErrorClass
	ParseUsage(output []byte) model.Usage
	WritablePaths() []string
}

// Stub is the unimplemented runner.
type Stub struct{}

func (Stub) Name() string            { return "" }
func (Stub) Capabilities() []string  { return nil }
func (Stub) NeedsPTY() bool          { return false }
func (Stub) WritablePaths() []string { return nil }
func (Stub) Classify(int, []byte) model.ErrorClass {
	return ""
}
func (Stub) ParseUsage([]byte) model.Usage { return model.Usage{} }

func (Stub) Build(LaunchSpec) ([]string, []string, error) {
	return nil, nil, ErrNotImplemented
}

func (Stub) Drive(context.Context, io.ReadWriter) error { return ErrNotImplemented }
