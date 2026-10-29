package e2e

import "testing"

func TestS01(t *testing.T) {
	// 单卡闭环，经过复核和合并。
	t.Skip("R4-D")
}

func TestS02(t *testing.T) {
	// 同一文件、不同函数的两张卡并行，都能合并。
	t.Skip("R3-C")
}

func TestS03(t *testing.T) {
	// 同一函数的两张卡，第二张等待后再跑。
	t.Skip("R3-C")
}

func TestS04(t *testing.T) {
	// 未声明的重叠改动产生冲突，派变基卡后合并。
	t.Skip("R3-C")
}

func TestS05(t *testing.T) {
	// 合并后测试变红，派修复卡。
	t.Skip("R3-C")
}

func TestS06(t *testing.T) {
	// 事件流不完整，但工作区有改动且测试通过，按 passed 处理。
	t.Skip("R2-C")
}

func TestS07(t *testing.T) {
	// 没有产出，换渠道重派一次，仍然没有就派待拍板问题。
	t.Skip("R3-B")
}

func TestS08(t *testing.T) {
	// 撞额度，渠道封锁，换渠道从卡分支续做。
	t.Skip("R3-B")
}

func TestS09(t *testing.T) {
	// 认证失败，渠道标记不健康并推送通知，卡换渠道。
	t.Skip("R3-B")
}

func TestS10(t *testing.T) {
	// 拒答，不换渠道，派待拍板问题。
	t.Skip("R3-B")
}

func TestS11(t *testing.T) {
	// 结果格式乱，派整理卡。
	t.Skip("R3-B")
}

func TestS12(t *testing.T) {
	// 设计期，safe 模式下 module 层冻结需要问题确认。
	t.Skip("R3-A")
}

func TestS13(t *testing.T) {
	// fast 模式下自动冻结。
	t.Skip("R3-A")
}

func TestS14(t *testing.T) {
	// 开发卡报告设计问题，经确认重开节点，下游卡暂停，派重规划卡。
	t.Skip("R3-A")
}

func TestS15(t *testing.T) {
	// 节点熔断，问题投递到 origin（用 file 适配器）。
	t.Skip("R3-B")
}

func TestS16(t *testing.T) {
	// 目标预算触顶，暂停目标。
	t.Skip("R3-B")
}

func TestS17(t *testing.T) {
	// 同层同源按 Pace 排序，低于红线的渠道被排除，显式指定不受红线限制。
	t.Skip("R2-D")
}

func TestS18(t *testing.T) {
	// 分级成功写入 tier；分级超时退回规则。
	t.Skip("R2-D")
}

func TestS19(t *testing.T) {
	// 余量填充，临近重置的渠道拿到 backlog 卡。
	t.Skip("R3-C")
}

func TestS20(t *testing.T) {
	// manual 目标不自动生成卡，但复核和合并照常。
	t.Skip("R3-C")
}

func TestS21(t *testing.T) {
	// 导入 v1 数据，按映射规则落位。
	t.Skip("R4-B")
}

func TestS22(t *testing.T) {
	// 看板只接受 GET，默认视图不含历史。
	t.Skip("R4-A")
}

func TestS23(t *testing.T) {
	// 定位任务产生提案，提案生成问题，批准后新卡使用新定位。
	t.Skip("R4-C")
}

func TestS24(t *testing.T) {
	// 执行器写 worktree 以外的地方被沙箱拒绝（只在 macOS 上跑）。
	t.Skip("R2-A")
}
