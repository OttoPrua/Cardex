// Package harvest turns an attempt's workspace evidence into an outcome. Design §5.
package harvest

import (
	"errors"

	"cardex/internal/model"
)

// ErrNotImplemented is returned until R2-C fills this package in.
var ErrNotImplemented = errors.New("not implemented")

// Input is everything the harvester may look at. Event streams are best-effort.
type Input struct {
	Attempt    model.Attempt    // 正在收割的尝试。
	Card       model.Card       // 所属卡，含声明和验收命令。
	Exit       int              // 进程退出码。
	Output     []byte           // 执行器输出。
	ErrorClass model.ErrorClass // runner.Classify 的结果。
	Repo       string           // 用于比较符号的仓库。
}

// Result is the outcome and the evidence behind it.
type Result struct {
	Outcome    model.Outcome    // 由工作区证据决定的结果。
	ErrorClass model.ErrorClass // 错误类。
	Excerpt    string           // 错误或失败摘录。
	Claims     []model.Claim    // 按实际改动更新后的声明；超出声明不算失败。
	Evidence   string           // 判定依据。
	ResultText string           // 宽松解析前的结果文件原文。
}

// Harvest reads the exit code, the event stream, the diff, acceptance, and the result file.
// The implementation lives in harvest.go.
