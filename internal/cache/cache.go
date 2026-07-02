package cache

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fuck-chat-img/fci/internal/config"
)

type call struct {
	wg  sync.WaitGroup
	val *Entry
	err error
}

var (
	flightMu sync.Mutex
	flights  = make(map[string]*call)
)

var errPanic = errors.New("singleflight: fn panic")

func Do(key string, fn func() (*Entry, error)) (val *Entry, err error) {
	flightMu.Lock()
	if c, ok := flights[key]; ok {
		flightMu.Unlock()
		c.wg.Wait()
		return c.val, c.err
	}
	c := &call{}
	c.wg.Add(1)
	flights[key] = c
	flightMu.Unlock()

	defer func() {
		if r := recover(); r != nil {
			c.err = fmt.Errorf("%w: %v", errPanic, r)
		}
		c.wg.Done()
		flightMu.Lock()
		delete(flights, key)
		flightMu.Unlock()
		val = c.val
		err = c.err
	}()

	c.val, c.err = fn()

	return c.val, c.err
}

type Entry struct {
	Key            string
	Value          []byte
	StreamEvents   [][]byte
	IsStream       bool
	ModelName      string
	CreatedAt      time.Time
	ExpiresAt      time.Time
	HitCount       int64
	HasImage       bool
	ImageCount     int
	ImageModelUsed string

	element *list.Element
}

type Store struct {
	mu       sync.RWMutex
	items    map[string]*Entry
	order    *list.List
	maxItems int
	ttl      time.Duration
	enabled  bool
	stopCh   chan struct{}
	closed   int32
}

var (
	store    atomic.Value
	initOnce sync.Once
)

const defaultCacheTTL = 24 * time.Hour

func Init() {
	initOnce.Do(func() {
		cfg := config.Get()
		s := &Store{
			items:    make(map[string]*Entry),
			order:    list.New(),
			maxItems: cfg.CacheMaxItems,
			ttl:      defaultCacheTTL,
			enabled:  cfg.CacheEnabled,
			stopCh:   make(chan struct{}),
		}
		store.Store(s)
		if s.enabled {
			go s.cleanupLoop()
		}
	})
}

func (s *Store) cleanupLoop() {
	defer func() {
		if r := recover(); r != nil {
			if atomic.LoadInt32(&s.closed) == 0 {
				go s.cleanupLoop()
			}
		}
	}()
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.cleanupExpired()
		case <-s.stopCh:
			return
		}
	}
}

func (s *Store) Close() {
	if atomic.CompareAndSwapInt32(&s.closed, 0, 1) {
		close(s.stopCh)
	}
}

func (s *Store) cleanupExpired() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	var next *list.Element
	for e := s.order.Front(); e != nil; e = next {
		next = e.Next()
		entry := e.Value.(*Entry)
		if !entry.ExpiresAt.IsZero() && now.After(entry.ExpiresAt) {
			s.removeElementLocked(e)
		}
	}
}

func Enabled() bool {
	s := loadStore()
	return s != nil && s.enabled
}

func loadStore() *Store {
	v := store.Load()
	if v == nil {
		return nil
	}
	return v.(*Store)
}

func Key(endpoint, modelGroup string, canonicalInput []byte) string {
	h := sha256.New()
	h.Write([]byte(endpoint))
	h.Write([]byte{0})
	h.Write([]byte(modelGroup))
	h.Write([]byte{0})
	h.Write(canonicalInput)
	return hex.EncodeToString(h.Sum(nil))
}

func Get(key string) (*Entry, bool) {
	s := loadStore()
	if s == nil || !s.enabled {
		atomic.AddInt64(&misses, 1)
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.items[key]
	if !ok {
		atomic.AddInt64(&misses, 1)
		return nil, false
	}
	if !e.ExpiresAt.IsZero() && time.Now().After(e.ExpiresAt) {
		s.removeElementLocked(e.element)
		atomic.AddInt64(&misses, 1)
		return nil, false
	}
	e.HitCount++
	s.moveToBackLocked(e)
	atomic.AddInt64(&hits, 1)
	return e, true
}

func (s *Store) moveToBackLocked(e *Entry) {
	if e.element != nil {
		s.order.MoveToBack(e.element)
	}
}

func (s *Store) removeElementLocked(el *list.Element) {
	entry := el.Value.(*Entry)
	s.order.Remove(el)
	delete(s.items, entry.Key)
	entry.element = nil
}

func jitterExpiry(ttl time.Duration) time.Time {
	if ttl <= 0 {
		return time.Time{}
	}
	jitter := time.Duration(float64(ttl) * 0.1 * (rand.Float64()*2 - 1))
	return time.Now().Add(ttl + jitter)
}

func PutWithMeta(key, modelName string, value []byte, hasImage bool, imgCount int, imgModelUsed string) {
	putEntry(key, &Entry{
		Key:            key,
		Value:          value,
		IsStream:       false,
		ModelName:      modelName,
		CreatedAt:      time.Now(),
		HasImage:       hasImage,
		ImageCount:     imgCount,
		ImageModelUsed: imgModelUsed,
	})
}

func PutStreamWithMeta(key, modelName string, events [][]byte, hasImage bool, imgCount int, imgModelUsed string) {
	putEntry(key, &Entry{
		Key:            key,
		StreamEvents:   events,
		IsStream:       true,
		ModelName:      modelName,
		CreatedAt:      time.Now(),
		HasImage:       hasImage,
		ImageCount:     imgCount,
		ImageModelUsed: imgModelUsed,
	})
}

func putEntry(key string, e *Entry) {
	s := loadStore()
	if s == nil || !s.enabled {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e.Key = key
	e.ExpiresAt = jitterExpiry(s.ttl)
	if existing, ok := s.items[key]; ok {
		if existing.element != nil {
			s.removeElementLocked(existing.element)
		}
	}
	e.element = s.order.PushBack(e)
	s.items[key] = e
	s.evictLocked()
}

func (s *Store) evictLocked() {
	for s.order.Len() > s.maxItems && s.order.Len() > 0 {
		front := s.order.Front()
		if front != nil {
			s.removeElementLocked(front)
		}
	}
}

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

func RecordHit() { atomic.AddInt64(&hits, 1) }

func RecordMiss() { atomic.AddInt64(&misses, 1) }

func GetStats() Stats {
	s := loadStore()
	if s == nil {
		return Stats{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Stats{
		Enabled:  s.enabled,
		Items:    s.order.Len(),
		MaxItems: s.maxItems,
		Hits:     atomic.LoadInt64(&hits),
		Misses:   atomic.LoadInt64(&misses),
	}
}

func Clear() int {
	s := loadStore()
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.items)
	s.items = make(map[string]*Entry)
	s.order.Init()
	return n
}
