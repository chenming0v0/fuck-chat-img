package cache

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func resetTestStore(maxItems int, ttl time.Duration) *Store {
	s := &Store{
		items:    make(map[string]*Entry),
		maxItems: maxItems,
		ttl:      ttl,
		enabled:  true,
		stopCh:   make(chan struct{}),
	}
	store.Store(s)
	flights = make(map[string]*call)
	return s
}

func TestKeyEndpointDifferentiation(t *testing.T) {
	modelGroup := "gpt-4"
	input := []byte("hello world")

	key1 := Key("chat", modelGroup, input)
	key2 := Key("responses", modelGroup, input)
	key3 := Key("messages", modelGroup, input)

	if key1 == key2 {
		t.Error("chat and responses should produce different keys for same input")
	}
	if key1 == key3 {
		t.Error("chat and messages should produce different keys for same input")
	}
	if key2 == key3 {
		t.Error("responses and messages should produce different keys for same input")
	}

	key1Repeat := Key("chat", modelGroup, input)
	if key1 != key1Repeat {
		t.Error("same endpoint+modelGroup+input should produce same key")
	}
}

func TestGetPutBasic(t *testing.T) {
	resetTestStore(100, time.Hour)

	key := "test-key"
	value := []byte("test-value")

	_, ok := Get(key)
	if ok {
		t.Error("Get on non-existent key should return false")
	}

	PutWithMeta(key, "test-model", value, false, 0, "")

	e, ok := Get(key)
	if !ok {
		t.Fatal("Get on existing key should return true")
	}
	if string(e.Value) != string(value) {
		t.Errorf("expected value %q, got %q", value, e.Value)
	}
	if e.HitCount != 1 {
		t.Errorf("expected HitCount 1, got %d", e.HitCount)
	}

	e2, ok := Get(key)
	if !ok {
		t.Fatal("second Get should return true")
	}
	if e2.HitCount != 2 {
		t.Errorf("expected HitCount 2, got %d", e2.HitCount)
	}
}

func TestLRUEviction(t *testing.T) {
	s := resetTestStore(3, time.Hour)

	PutWithMeta("a", "m", []byte("a"), false, 0, "")
	PutWithMeta("b", "m", []byte("b"), false, 0, "")
	PutWithMeta("c", "m", []byte("c"), false, 0, "")

	if len(s.items) != 3 {
		t.Errorf("expected 3 items, got %d", len(s.items))
	}

	_, ok := Get("a")
	if !ok {
		t.Fatal("a should exist")
	}

	PutWithMeta("d", "m", []byte("d"), false, 0, "")

	if len(s.items) != 3 {
		t.Errorf("expected 3 items after eviction, got %d", len(s.items))
	}

	if _, ok := s.items["b"]; ok {
		t.Error("b should have been evicted (LRU before access to a)")
	}
	if _, ok := s.items["a"]; !ok {
		t.Error("a should still exist (accessed)")
	}
	if _, ok := s.items["c"]; !ok {
		t.Error("c should still exist")
	}
	if _, ok := s.items["d"]; !ok {
		t.Error("d should exist")
	}
}

func TestCacheExpiry(t *testing.T) {
	s := resetTestStore(100, 10*time.Millisecond)

	key := "expiring-key"
	PutWithMeta(key, "m", []byte("value"), false, 0, "")

	_, ok := Get(key)
	if !ok {
		t.Fatal("key should exist immediately after Put")
	}

	time.Sleep(50 * time.Millisecond)

	_, ok = Get(key)
	if ok {
		t.Error("expired key should be removed on Get")
	}

	if _, exists := s.items[key]; exists {
		t.Error("expired key should have been deleted from items")
	}
}

func TestClear(t *testing.T) {
	s := resetTestStore(100, time.Hour)

	PutWithMeta("x", "m", []byte("x"), false, 0, "")
	PutWithMeta("y", "m", []byte("y"), false, 0, "")

	if len(s.items) != 2 {
		t.Errorf("expected 2 items before Clear, got %d", len(s.items))
	}

	n := Clear()
	if n != 2 {
		t.Errorf("Clear should return 2, got %d", n)
	}

	if len(s.items) != 0 {
		t.Errorf("expected 0 items after Clear, got %d", len(s.items))
	}

	_, ok := Get("x")
	if ok {
		t.Error("x should not exist after Clear")
	}

	n = Clear()
	if n != 0 {
		t.Errorf("Clear on empty cache should return 0, got %d", n)
	}
}

func TestSingleflightPanicRecovery(t *testing.T) {
	resetTestStore(100, time.Hour)

	key := "panic-key"
	panicMsg := "intentional panic"

	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		_, err := Do(key, func() (*Entry, error) {
			panic(panicMsg)
		})
		if err == nil {
			t.Error("expected error from panicked fn")
			return
		}
		if !errors.Is(err, errPanic) {
			t.Errorf("expected errPanic wrapped, got %v", err)
		}
	}()

	wg.Wait()

	flightMu.Lock()
	_, leaked := flights[key]
	flightMu.Unlock()
	if leaked {
		t.Error("flights map entry should be cleaned up after panic")
	}

	secondOk := make(chan struct{})
	go func() {
		_, err := Do(key, func() (*Entry, error) {
			return &Entry{Value: []byte("ok")}, nil
		})
		if err != nil {
			t.Errorf("subsequent request after panic failed: %v", err)
		}
		close(secondOk)
	}()

	select {
	case <-secondOk:
	case <-time.After(time.Second):
		t.Fatal("subsequent request after panic blocked forever (flight leaked)")
	}
}

func TestSingleflightDoDedup(t *testing.T) {
	resetTestStore(100, time.Hour)

	key := "dedup-key"
	var callCount int64
	started := make(chan struct{})
	proceed := make(chan struct{})

	fn := func() (*Entry, error) {
		atomic.AddInt64(&callCount, 1)
		close(started)
		<-proceed
		return &Entry{Value: []byte("result")}, nil
	}

	const concurrency = 10
	var wg sync.WaitGroup
	results := make([]*Entry, concurrency)
	errs := make([]error, concurrency)

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx], errs[idx] = Do(key, fn)
		}(i)
	}

	<-started
	time.Sleep(50 * time.Millisecond)

	close(proceed)
	wg.Wait()

	if callCount != 1 {
		t.Errorf("fn should be called exactly once, called %d times", callCount)
	}

	for i := 0; i < concurrency; i++ {
		if errs[i] != nil {
			t.Errorf("goroutine %d got error: %v", i, errs[i])
		}
		if results[i] == nil {
			t.Errorf("goroutine %d got nil result", i)
		}
	}
}
