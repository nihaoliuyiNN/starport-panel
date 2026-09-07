package installer

import "context"

// Step 一个装机阶段：Name 展示名；Skip 探测是否已完成（幂等，可空）；Run 执行阶段。
type Step struct {
	Name string
	Skip func(ctx context.Context) bool
	Run  func(ctx context.Context, log LogFunc) *Fail
}

// runSteps 顺序执行阶段：已完成的跳过，任一失败即中止并返回该失败。
// 每步前检查 ctx，支持控制面 cancel 中途停机。
func runSteps(ctx context.Context, log LogFunc, steps []Step) *Fail {
	for _, s := range steps {
		if ctx.Err() != nil {
			return &Fail{Code: ErrCancelled, Message: "装机已取消", Retryable: true}
		}
		if s.Skip != nil && s.Skip(ctx) {
			emit(log, "[skip] "+s.Name+"（已就绪）")
			continue
		}
		emit(log, "[step] "+s.Name)
		if f := s.Run(ctx, log); f != nil {
			emit(log, "[fail] "+s.Name+": "+f.Message)
			return f
		}
		emit(log, "[ok] "+s.Name)
	}
	return nil
}
