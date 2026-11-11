package plugin

import "sync"

// outboxRecord 是队列中的一条待投递事件
type outboxRecord struct {
	EventID      string
	Event        string
	EnqueuedAtMs int64
	Body         string
}

// queue 是单插件的内存投递队列（容量上限，满则丢最旧）。
//
// 与 Avalon outbox 的差异：v1 只有观察档，不需要 WAL 持久与重放，
// 故为纯内存结构；容量与丢最旧语义一致。
type queue struct {
	mu    sync.Mutex
	cap   int
	items []outboxRecord
}

func newQueue(capacity int) *queue {
	if capacity <= 0 {
		capacity = DefaultOutboxCapacity
	}
	return &queue{cap: capacity}
}

// append 入队；容量满时丢弃最旧并返回被丢弃的条数
func (q *queue) append(rec outboxRecord) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	dropped := 0
	if len(q.items) >= q.cap {
		dropped = len(q.items) - q.cap + 1
		q.items = q.items[dropped:]
	}
	q.items = append(q.items, rec)
	return dropped
}

// peek 查看队首（不移除）
func (q *queue) peek() (outboxRecord, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 {
		return outboxRecord{}, false
	}
	return q.items[0], true
}

// pop 移除队首
func (q *queue) pop() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) > 0 {
		q.items = q.items[1:]
	}
}

// size 当前排队条数
func (q *queue) size() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}
