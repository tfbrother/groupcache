/*
Copyright 2012 Google Inc.

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

// Package singleflight provides a duplicate(重复) function call suppression(抑制)
// mechanism(机制).
//这个包主要实现了一个可合并操作的接口，singleflight包提供了一个可抑制(合并)重复的函数调用的机制。
package singleflight

import "sync"

// call is an in-flight or completed Do call
// 回调函数接口
type call struct {
	wg  sync.WaitGroup
	val interface{} // 回调函数
	err error
}

// Group represents a class of work and forms a namespace in which
// units of work can be executed with duplicate suppression.
type Group struct {
	mu sync.Mutex       // protects m(保护m的锁)
	m  map[string]*call // lazily initialized
}

// Do executes and returns the results of the given function, making
// sure that only one execution is in-flight for a given key at a
// time. If a duplicate comes in, the duplicate caller waits for the
// original to complete and receives the same results.
// 这个就是这个包的主要接口，用于向其他节点发送查询请求时，合并相同key的请求，减少热点可能带来的麻烦
// 比如说我请求key="123"的数据，在没有返回的时候又有很多相同key的请求，而此时后面的没有必要发，只要
// 等待第一次返回的结果即可
func (g *Group) Do(key string, fn func() (interface{}, error)) (interface{}, error) {
	// 首先获取group锁
	g.mu.Lock()
	// 映射表不存在就创建1个
	if g.m == nil {
		g.m = make(map[string]*call)
	}
	// 判断要查询某个key的请求是否已经在处理了
	if c, ok := g.m[key]; ok {
		// 已经在处理了 就释放group锁 并wait在wg上，我们看到wg加1是在某个时间段内第一次请求时加的
		// 并且在完成fn操作返回后会wg done，那时wait等待就会返回，直接返回第一次请求的结果
		g.mu.Unlock()
		c.wg.Wait()
		return c.val, c.err
	}
	// 创建回调，wg加1，把回调存到m中表示已经有在请求了，释放锁
	c := new(call)
	c.wg.Add(1)
	g.m[key] = c
	g.mu.Unlock()

	// 执行fn，释放wg
	c.val, c.err = fn()
	c.wg.Done()

	// 加锁将请求从m中删除，表示请求已经做好了
	g.mu.Lock()
	delete(g.m, key)
	g.mu.Unlock()

	return c.val, c.err
}
