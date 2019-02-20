/*
Copyright 2013 Google Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

     http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package lru implements an LRU cache.
/*
LRU 最近最少使用算法，LRU算法主要用于缓存淘汰。
原理：

添加元素时，放到链表头
缓存命中，将元素移动到链表头
缓存满了之后，将链表尾的元素删除

LRU实现：

可以用一个双向链表保存数据
使用hash实现O(1)的访问
*/
package lru

import "container/list"

// Cache is an LRU cache. It is not safe for concurrent access.
//Cache 结构体，定义lru cache 不是线程安全的
type Cache struct {
	// MaxEntries is the maximum number of cache entries before
	// an item is evicted. Zero means no limit.
	// 数目限制，0是无限制
	MaxEntries int

	// OnEvicted optionally specificies a callback function to be
	// executed when an entry is purged from the cache.
	// 删除时, 可以添加可选的回调函数
	OnEvicted func(key Key, value interface{})

	// 使用链表保存数据
	ll    *list.List
	cache map[interface{}]*list.Element
}

// A Key may be any value that is comparable. See http://golang.org/ref/spec#Comparison_operators
// Key 是任何可以比较的值
type Key interface{}

type entry struct {
	key   Key
	value interface{}
}

// New creates a new Cache.
// If maxEntries is zero, the cache has no limit and it's assumed
// that eviction is done by the caller.
func New(maxEntries int) *Cache {
	return &Cache{
		MaxEntries: maxEntries,
		ll:         list.New(),
		cache:      make(map[interface{}]*list.Element),
	}
}

// Add adds a value to the cache.
//添加一个元素到缓存中
func (c *Cache) Add(key Key, value interface{}) {
	if c.cache == nil {
		c.cache = make(map[interface{}]*list.Element)
		c.ll = list.New()
	}
	//如果该key已经存在，则将该key移到链表的首部，并赋予新值。
	if ee, ok := c.cache[key]; ok {
		c.ll.MoveToFront(ee)
		// TODO(tfbrother)类型断言
		ee.Value.(*entry).value = value
		return
	}
	//如果该key不存在，则将该key放到连表的首部。
	ele := c.ll.PushFront(&entry{key, value})
	c.cache[key] = ele
	//如果设置了最大缓存数，并且链表长度已经超过了最大缓存数，则把最老的元素移除。
	if c.MaxEntries != 0 && c.ll.Len() > c.MaxEntries {
		c.RemoveOldest()
	}
}

// Get looks up a key's value from the cache.
//在缓存中查找key
func (c *Cache) Get(key Key) (value interface{}, ok bool) {
	if c.cache == nil {
		return
	}
	//查找到则返回，并且把改元素移动到链表的首部。
	if ele, hit := c.cache[key]; hit {
		c.ll.MoveToFront(ele)
		return ele.Value.(*entry).value, true
	}
	return
}

// Remove removes the provided key from the cache.
//在缓存中移除key
func (c *Cache) Remove(key Key) {
	if c.cache == nil {
		return
	}
	if ele, hit := c.cache[key]; hit {
		c.removeElement(ele)
	}
}

// RemoveOldest removes the oldest item from the cache.
//移除缓存中最老的一个元素
func (c *Cache) RemoveOldest() {
	if c.cache == nil {
		return
	}
	ele := c.ll.Back()
	if ele != nil {
		c.removeElement(ele)
	}
}

//删除缓存中的某个元素（私有方法）
func (c *Cache) removeElement(e *list.Element) {
	c.ll.Remove(e)
	kv := e.Value.(*entry)
	delete(c.cache, kv.key)
	if c.OnEvicted != nil {
		c.OnEvicted(kv.key, kv.value)
	}
}

// Len returns the number of items in the cache.
//返回缓存中的元素个数
func (c *Cache) Len() int {
	if c.cache == nil {
		return 0
	}
	return c.ll.Len()
}
