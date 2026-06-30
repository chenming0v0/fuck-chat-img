package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fuck-chat-img/fci/internal/config"
)

// Entry 缓存条目
type Entry struct {
	Key       string
	Value     []byte // 完整响应体(非流式)或合并后的流式事件序列(用于回放)
	StreamEvents [][]byte // 若为流式响应, 存储事件列表用于回放
	IsStream  bool
	ModelName string
	CreatedAt time.Time
	HitCount  int64
}

// Store 内存 LRU 缓存
type Store struct {
	mu       sync.RWMutex
	items    map[string]*Entry
	order    []string // 简单 LRU 顺序
	maxItems int
	enabled  bool
}

var store *Store

// Init 初始化缓存
func Init() {
	cfg := config.Get()
	store = &Store{
		items:    make(map[string]*Entry),
		maxItems: cfg.CacheMaxItems,
		enabled:  cfg.CacheEnabled,
	}
}

// Enabled 是否启用
func Enabled() bool {
	return store != nil && store.enabled
}

// Key 根据模型组名与规范化后的输入计算稳定缓存键
// 输入应当是规范化后的 canonical JSON 字节
func Key(modelGroup string, canonicalInput []byte) string {
	h := sha256.New()
	h.Write([]byte(modelGroup))
	h.Write([]byte{0})
	h.Write(canonicalInput)
	return hex.EncodeToString(h.Sum(nil))
}

// Get 读取缓存(LRU: 命中时将条目移到最近使用位置)
func Get(key string) (*Entry, bool) {
	if store == nil || !store.enabled {
		return nil, false
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	e, ok := store.items[key]
	if !ok {
		return nil, false
	}
	e.HitCount++
	// LRU: 将命中的 key 移到末尾(最近使用)
	store.touchLocked(key)
	return e, true
}

// touchLocked 将 key 移到 order 末尾(调用方持锁)
func (s *Store) touchLocked(key string) {
	for i, k := range s.order {
		if k == key {
			s.order = append(s.order[:i], s.order[i+1:]...)
			s.order = append(s.order, key)
			return
		}
	}
}

// Put 写入缓存(非流式)
func Put(key, modelName string, value []byte) {
	if store == nil || !store.enabled {
		return
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, exists := store.items[key]; !exists {
		store.order = append(store.order, key)
		store.evictLocked()
	} else {
		// 已存在: 移到末尾(LRU)
		store.touchLocked(key)
	}
	store.items[key] = &Entry{
		Key:       key,
		Value:     value,
		IsStream:  false,
		ModelName: modelName,
		CreatedAt: time.Now(),
	}
}

// PutStream 写入缓存(流式, 存事件列表用于回放)
func PutStream(key, modelName string, events [][]byte) {
	if store == nil || !store.enabled {
		return
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, exists := store.items[key]; !exists {
		store.order = append(store.order, key)
		store.evictLocked()
	} else {
		store.touchLocked(key)
	}
	store.items[key] = &Entry{
		Key:          key,
		StreamEvents: events,
		IsStream:     true,
		ModelName:    modelName,
		CreatedAt:    time.Now(),
	}
}

// evictLocked 淘汰超出上限的条目(FIFO, 调用方持锁)
func (s *Store) evictLocked() {
	for len(s.order) > s.maxItems && len(s.order) > 0 {
		k := s.order[0]
		s.order = s.order[1:]
		delete(s.items, k)
	}
}

// Stats 缓存统计
type Stats struct {
	Enabled  bool  `json:"enabled"`
	Items    int   `json:"items"`
	MaxItems int   `json:"max_items"`
	Hits     int64 `json:"hits"`
	Misses   int64 `json:"misses"`
}

var (
	hits   int64
	misses int64
)

// RecordHit 记录命中(原子操作, 并发安全)
func RecordHit() { atomic.AddInt64(&hits, 1) }

// RecordMiss 记录未命中(原子操作, 并发安全)
func RecordMiss() { atomic.AddInt64(&misses, 1) }

// Stats 返回统计
func GetStats() Stats {
	if store == nil {
		return Stats{}
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	return Stats{
		Enabled:  store.enabled,
		Items:    len(store.items),
		MaxItems: store.maxItems,
		Hits:     atomic.LoadInt64(&hits),
		Misses:   atomic.LoadInt64(&misses),
	}
}

// Clear 清空缓存
func Clear() int {
	if store == nil {
		return 0
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	n := len(store.items)
	store.items = make(map[string]*Entry)
	store.order = nil
	return n
}
