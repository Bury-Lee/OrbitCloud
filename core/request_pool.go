// request_pool.go —— API 请求准入池:限并发 + 可选排队 + block/reject 模式,提供显式背压。
package core

import (
	"context"
	"errors"
)

// ErrTooManyRequests 请求被准入池拒绝(队列满 + reject 模式)。
var ErrTooManyRequests = errors.New("too many requests")

// AdmissionMode 准入池满时行为。
type AdmissionMode int8

const (
	AdmissionModeBlock  AdmissionMode = iota // 阻塞等待(背压到 HTTP Server)
	AdmissionModeReject                      // 快速拒绝(返回 ErrTooManyRequests → 503)
)

// AdmissionPool 请求准入池。
//   sem    — 同时在途令牌(MaxConcurrent 把)
//   queue  — 等待队列(QueueSize 深; cap=0 时无队列)
//   mode   — block / reject
type AdmissionPool struct {
	sem   chan struct{}
	queue chan struct{}
	mode  AdmissionMode
}

// NewAdmissionPool 构造准入池。
//   maxConc    – 同时在途上限(必须 > 0)
//   queueDepth – 排队深度(0 = 不排队)
//   mode       – 池满行为
func NewAdmissionPool(maxConc, queueDepth int, mode AdmissionMode) *AdmissionPool {
	return &AdmissionPool{
		sem:   make(chan struct{}, maxConc),
		queue: make(chan struct{}, queueDepth),
		mode:  mode,
	}
}

// Acquire 获取准入令牌。ctx 取消时返回 ctx.Err()。
// 行为取决于 mode:
//   block  — 并发满时阻塞(队列满也在 sem 上阻塞),背压自然传导;
//   reject — 并发 & 队列全满时返回 ErrTooManyRequests。
func (p *AdmissionPool) Acquire(ctx context.Context) error {
	// Fast path: 直接获取令牌
	select {
	case p.sem <- struct{}{}:
		return nil
	default:
	}

	// 无队列 → 按 mode 处理
	if cap(p.queue) == 0 {
		if p.mode == AdmissionModeReject {
			return ErrTooManyRequests
		}
		select {
		case p.sem <- struct{}{}:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	// 有队列 → 尝试入队
	select {
	case p.queue <- struct{}{}:
		// 入队成功,等令牌
		select {
		case p.sem <- struct{}{}:
			<-p.queue
			return nil
		case <-ctx.Done():
			<-p.queue
			return ctx.Err()
		}
	case <-ctx.Done():
		return ctx.Err()
	default:
		// 队列也满 → 按 mode 处理
		if p.mode == AdmissionModeReject {
			return ErrTooManyRequests
		}
		// block: 阻塞入队
		select {
		case p.queue <- struct{}{}:
			select {
			case p.sem <- struct{}{}:
				<-p.queue
				return nil
			case <-ctx.Done():
				<-p.queue
				return ctx.Err()
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Release 释放令牌,让下一个等待请求进入。
func (p *AdmissionPool) Release() {
	<-p.sem
}