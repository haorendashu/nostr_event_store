package cache

import "testing"

type testNode struct {
	size uint64
}

func (n *testNode) Size() uint64 {
	return n.size
}

func TestLRUCacheBasic(t *testing.T) {
	c, err := LRUCache(2)
	if err != nil {
		t.Fatalf("LRUCache create error: %v", err)
	}

	c.Set("a", 1)
	c.Set("b", 2)

	v, ok := c.Get("a")
	if !ok || v.(int) != 1 {
		t.Fatalf("expected a=1, got %v ok=%v", v, ok)
	}

	if c.Size() != 2 {
		t.Fatalf("expected size 2, got %d", c.Size())
	}
}

func TestLRUCacheEviction(t *testing.T) {
	c, _ := LRUCache(2)
	c.Set("a", 1)
	c.Set("b", 2)
	c.Set("c", 3)

	if _, ok := c.Get("a"); ok {
		t.Fatalf("expected a evicted")
	}
	if _, ok := c.Get("b"); !ok {
		t.Fatalf("expected b present")
	}
	if _, ok := c.Get("c"); !ok {
		t.Fatalf("expected c present")
	}
}

func TestMemoryCacheEviction(t *testing.T) {
	c, err := MemoryCacheWithLimit(128)
	if err != nil {
		t.Fatalf("MemoryCacheWithLimit error: %v", err)
	}

	c.Set("n1", &testNode{size: 80})
	c.Set("n2", &testNode{size: 80})

	if _, ok := c.Get("n1"); ok {
		t.Fatalf("expected n1 evicted")
	}
	if _, ok := c.Get("n2"); !ok {
		t.Fatalf("expected n2 present")
	}
}

func TestCachePool(t *testing.T) {
	pool := NewCachePool()
	c, err := pool.NewCache("primary", Options{MaxSize: 4})
	if err != nil {
		t.Fatalf("NewCache error: %v", err)
	}

	c.Set("k1", 1)
	if v, ok := c.Get("k1"); !ok || v.(int) != 1 {
		t.Fatalf("expected k1=1, got %v ok=%v", v, ok)
	}

	stats := pool.Stats()
	if _, ok := stats["primary"]; !ok {
		t.Fatalf("expected stats for primary")
	}
}

func TestConcurrentCache(t *testing.T) {
	base, _ := LRUCache(4)
	cc := NewConcurrentCache(base)

	cc.Set("k", 1)
	if v, ok := cc.Get("k"); !ok || v.(int) != 1 {
		t.Fatalf("expected k=1, got %v ok=%v", v, ok)
	}
	cc.Delete("k")
	if _, ok := cc.Get("k"); ok {
		t.Fatalf("expected k deleted")
	}
}
