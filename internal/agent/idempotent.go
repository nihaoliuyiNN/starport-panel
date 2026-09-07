package agent

import (
	"sync"
	"time"

	pb "starport-panel/internal/pb/agentv1"
)

// cachedResult 缓存一次执行的终态，用于 requestId 幂等去重。
type cachedResult struct {
	ok       bool
	exitCode int
	err      *Error
	install  *pb.InstallResult // 结构化结果（装机回传），exec 为空
	at       time.Time
}

// idempotentStore 按 requestId 缓存执行终态：重发同一 id 直接回缓存，不二次执行。
// 装机脚本本身就是「探测-跳过」幂等的，这层额外挡住控制面重试/网络重发导致的并发双跑。
type idempotentStore struct {
	mu   sync.Mutex
	done map[string]cachedResult
	ttl  time.Duration
}

func newIdempotentStore(ttl time.Duration) *idempotentStore {
	return &idempotentStore{done: make(map[string]cachedResult), ttl: ttl}
}

// get 取缓存结果；过期或不存在返回 ok=false。
func (s *idempotentStore) get(id string) (cachedResult, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.done[id]
	if !ok {
		return cachedResult{}, false
	}
	if time.Since(r.at) > s.ttl {
		delete(s.done, id)
		return cachedResult{}, false
	}
	return r, true
}

// put 写入结果并顺手清理过期项。
func (s *idempotentStore) put(id string, r cachedResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r.at = time.Now()
	s.done[id] = r
	for k, v := range s.done {
		if time.Since(v.at) > s.ttl {
			delete(s.done, k)
		}
	}
}
