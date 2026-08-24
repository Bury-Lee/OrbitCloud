// execute.go — 泛型 Execute[T] 辅助:提交任务到 agilePool 并等待结果。
// 仅作为基础设施提供,由 api 层按需调用,不侵入 server 层业务逻辑。
package core

import (
	"context"

	agilepool "github.com/Yiming1997/agilePool/v2"
)

// Execute 将 fn 提交到 pool 并阻塞等待结果。
//   - pool 队列满时 SubmitCtx 阻塞 → 背压自然传导到 HTTP handler → 客户端。
//   - ctx 取消时返回 ctx.Err()。
//   - fn 返回的 error 保持原样传递(调用方可根据哨兵错误做 HTTP 映射)。
//
// 泛型参数 T 为返回值类型,避免调用方手动管理 channel。
func Execute[T any](ctx context.Context, pool *agilepool.Pool, fn func() (T, error)) (T, error) {
	type result struct {
		val T
		err error
	}
	ch := make(chan result, 1)

	pool.SubmitCtx(ctx, agilepool.TaskFunc(func() error {
		v, e := fn()
		ch <- result{v, e}
		return nil
	}))

	select {
	case r := <-ch:
		return r.val, r.err
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	}
}