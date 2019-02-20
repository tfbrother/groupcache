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

/*
不同于memcached，groupcache面向的是静态的缓存系统，比如google家的下载站，用来缓存对应的文件区块。
一经set操作，key对应的value就不再变化。使用的时候，需要自定义缓存缺失时使用的set操作。
当发生miss的时候，首先会从根据key的consist hash值找到对应的peer，去peer里寻找对应的value。
如果找不到，则使用自定义的get函数从慢缓存（比如db，文件读取）获取alue，并填充对应peer。
下一次获取，就直接从cache里取出来，不再访问慢缓存。另外，为避免网络变成瓶颈，本地peer获取cache后，
会存在本地的localCache里，通过LRU算法进行管理。
参考：http://www.cnblogs.com/Lifehacker/p/groupcache_inside.html
*/

// Package groupcache provides a data loading mechanism with caching
// and de-duplication that works across a set of peer processes.
//
// Each data Get first consults its local cache, otherwise delegates
// to the requested key's canonical owner, which then checks its cache
// or finally gets the data.  In the common case, many concurrent
// cache misses across a set of peers for the same key result in just
// one cache fill.
package groupcache

import (
	"errors"
	"math/rand"
	"strconv"
	"sync"
	"sync/atomic"

	pb "github.com/tfbrother/groupcache/groupcachepb"
	"github.com/tfbrother/groupcache/lru"
	"github.com/tfbrother/groupcache/singleflight"
)

// A Getter loads data for a key.
type Getter interface {
	// Get returns the value identified by key, populating dest.
	//
	// The returned data must be unversioned. That is, key must
	// uniquely describe the loaded data, without an implicit
	// current time, and without relying on cache expiration
	// mechanisms.
	Get(ctx Context, key string, dest Sink) error
}

// A GetterFunc implements Getter with a function.
type GetterFunc func(ctx Context, key string, dest Sink) error

func (f GetterFunc) Get(ctx Context, key string, dest Sink) error {
	return f(ctx, key, dest)
}

var (
	mu     sync.RWMutex
	groups = make(map[string]*Group)

	initPeerServerOnce sync.Once
	initPeerServer     func()
)

// GetGroup returns the named group previously created with NewGroup, or
// nil if there's no such group.
// 根据name从全局变量groups（一个string/group的map）查找对于group
func GetGroup(name string) *Group {
	mu.RLock()
	g := groups[name]
	mu.RUnlock()
	return g
}

// NewGroup creates a coordinated group-aware Getter from a Getter.
//
// The returned Getter tries (but does not guarantee) to run only one
// Get call at once for a given key across an entire set of peer
// processes. Concurrent callers both in the local process and in
// other processes receive copies of the answer once the original Get
// completes.
//
// The group name must be unique for each getter.
// 创建一个新的group，如果已经存在该name的group将会抛出一个异常，go里面叫panic
func NewGroup(name string, cacheBytes int64, getter Getter) *Group {
	return newGroup(name, cacheBytes, getter, nil)
}

// If peers is nil, the peerPicker is called via a sync.Once to initialize it.
func newGroup(name string, cacheBytes int64, getter Getter, peers PeerPicker) *Group {
	if getter == nil {
		panic("nil Getter")
	}
	mu.Lock()
	defer mu.Unlock()
	initPeerServerOnce.Do(callInitPeerServer)
	if _, dup := groups[name]; dup {
		panic("duplicate registration of group " + name)
	}
	g := &Group{
		name:       name,
		getter:     getter,
		peers:      peers,
		cacheBytes: cacheBytes,
	}
	if fn := newGroupHook; fn != nil {
		fn(g)
	}
	groups[name] = g
	return g
}

// newGroupHook, if non-nil, is called right after a new group is created.
var newGroupHook func(*Group)

// RegisterNewGroupHook registers a hook that is run each time
// a group is created.
// 注册一个创建新group的钩子，比如将group打印出来等，全局只能注册一次，多次注册会触发panic
func RegisterNewGroupHook(fn func(*Group)) {
	if newGroupHook != nil {
		panic("RegisterNewGroupHook called more than once")
	}
	newGroupHook = fn
}

// RegisterServerStart registers a hook that is run when the first
// group is created.
// 注册一个服务，该服务在NewGroup开始时被调用，并且只被调用一次
func RegisterServerStart(fn func()) {
	if initPeerServer != nil {
		panic("RegisterServerStart called more than once")
	}
	initPeerServer = fn
}

func callInitPeerServer() {
	if initPeerServer != nil {
		initPeerServer()
	}
}

// A Group is a cache namespace and associated data loaded spread over
// a group of 1 or more machines.
//该类是GroupCache的核心类，所有的其他包都是为该类服务的。groupcache支持namespace概念，不同的namespace有自己的配额以及cache，
//不同group之间cache是独立的也就是不能存在某个group的行为影响到另外一个namespace的情况 groupcache的每个节点的cache分为2层，
//由本节点直接访问后端的存在maincache，其他存在在hotcache
type Group struct {
	name       string     // group名，可以理解为namespace的名字，group其实就是一个namespace
	getter     Getter     // 由调用方传入的回调，用于groupcache访问后端数据
	peersOnce  sync.Once  //sync.Once is an object that will perform exactly one action.通过该对象可以达到只调用一次
	peers      PeerPicker // 用于访问groupcache中其他节点的接口，比如上面的HTTPPool实现了该接口
	cacheBytes int64      // limit for sum of mainCache and hotCache size.cache的总大小

	// mainCache is a cache of the keys for which this process
	// (amongst its peers) is authorative. That is, this cache
	// contains keys which consistent hash on to this process's
	// peer number.
	// 从本节点指向向后端获取到的数据存在在cache中
	//默认就是cache所有字段的零值
	mainCache cache

	// hotCache contains keys/values for which this peer is not
	// authorative (otherwise they would be in mainCache), but
	// are popular enough to warrant mirroring in this process to
	// avoid going over the network to fetch from a peer.  Having
	// a hotCache avoids network hotspotting, where a peer's
	// network card could become the bottleneck on a popular key.
	// This cache is used sparingly to maximize the total number
	// of key/value pairs that can be stored globally.
	// 从groupcache中其他节点上获取到的数据存在在该cache中，因为是用其他节点获取到的，也就是说这个数据存在多份，也就是所谓的hot
	hotCache cache

	// loadGroup ensures that each key is only fetched once
	// (either locally or remotely), regardless of the number of
	// concurrent callers.
	// 用于向groupcache中其他节点访问时合并请求
	loadGroup singleflight.Group

	// Stats are statistics on the group.
	// 统计信息
	Stats Stats
}

// Stats are per-group statistics.
type Stats struct {
	Gets           AtomicInt // any Get request, including from peers
	CacheHits      AtomicInt // either cache was good
	PeerLoads      AtomicInt // either remote load or remote cache hit (not an error)
	PeerErrors     AtomicInt
	Loads          AtomicInt // (gets - cacheHits)
	LoadsDeduped   AtomicInt // after singleflight
	LocalLoads     AtomicInt // total good local loads
	LocalLoadErrs  AtomicInt // total bad local loads
	ServerRequests AtomicInt // gets that came over the network from peers
}

// Name returns the name of the group.
func (g *Group) Name() string {
	return g.name
}

// TODO(tfbrother) 此处将HTTPPool和Group关联起来了
func (g *Group) initPeers() {
	if g.peers == nil {
		g.peers = getPeers()
	}
}

//整个groupcache核心方法就这么一个
//这里需要说明的是A向groupcache中其他节点B发送请求，此时B是调用Get方法，然后如果本地不存在则也会走load，但是不同的是
//PickPeer会发现是本身节点（HTTPPOOL的实现），然后就会走getLocally，会将数据在B的maincache中填充一份，也就是说如果
//是向groupcache中其他节点发请求的，会一下子在groupcache集群内存2分数据，一份在B的maincache里，一份在A的hotcache中，
//这也就达到了自动复制，越是热点的数据越是在集群内份数多，也就达到了解决热点数据的问题
func (g *Group) Get(ctx Context, key string, dest Sink) error {
	// 初始化peers，全局初始化一次
	g.peersOnce.Do(g.initPeers)
	// 统计信息gets增1
	g.Stats.Gets.Add(1)
	if dest == nil {
		return errors.New("groupcache: nil dest Sink")
	}
	// 查找本地是否存在该key，先查maincache，再查hotcache
	value, cacheHit := g.lookupCache(key)

	// 如果查到就hit增1，并返回
	if cacheHit {
		g.Stats.CacheHits.Add(1)
		return setSinkView(dest, value)
	}

	// Optimization to avoid double unmarshalling or copying: keep
	// track of whether the dest was already populated. One caller
	// (if local) will set this; the losers will not. The common
	// case will likely be one caller.
	// 加载该key到本地cache中，其中如果是本地直接请求后端得到的数据，并且是同一个时间段里第一个，
	// 就不需要重新setSinkView了，在load中已经设置过了，destPopulated这个参数以来底的实现
	destPopulated := false
	value, destPopulated, err := g.load(ctx, key, dest)
	if err != nil {
		return err
	}
	if destPopulated {
		return nil
	}
	return setSinkView(dest, value)
}

// load loads key either by invoking the getter locally or by sending it to another machine.
// 调用本地的回调函数getter获取key的值或者发送到其它服务器获取key值。
func (g *Group) load(ctx Context, key string, dest Sink) (value ByteView, destPopulated bool, err error) {
	// 统计loads增1
	g.Stats.Loads.Add(1)
	// 使用singleflight来达到合并请求
	viewi, err := g.loadGroup.Do(key, func() (interface{}, error) {
		// 统计信息真正发送请求的次数增1
		g.Stats.LoadsDeduped.Add(1)
		var value ByteView
		var err error

		// 选取向哪个节点发送请求，比如HTTPPool中的PickPeer实现
		if peer, ok := g.peers.PickPeer(key); ok {
			// 从groupcache中其他节点获取数据，并将数据存入hotcache
			value, err = g.getFromPeer(ctx, peer, key)
			if err == nil {
				g.Stats.PeerLoads.Add(1)
				return value, nil
			}
			g.Stats.PeerErrors.Add(1)
			// TODO(bradfitz): log the peer's error? keep
			// log of the past few for /groupcachez?  It's
			// probably boring (normal task movement), so not
			// worth logging I imagine.
		}

		// 如果选取的节点就是本节点或从其他节点获取失败，则由本节点去获取数据，也就是调用getter接口的Get方法
		// 另外我们看到这里dest已经被赋值了，所以有destPopulated来表示已经赋值过不需要再赋值
		value, err = g.getLocally(ctx, key, dest)
		if err != nil {
			g.Stats.LocalLoadErrs.Add(1)
			return nil, err
		}
		g.Stats.LocalLoads.Add(1)
		destPopulated = true // only one caller of load gets this return value
		// 将获取的数据放入本地缓存maincache
		g.populateCache(key, value, &g.mainCache)
		return value, nil
	})
	if err == nil {
		value = viewi.(ByteView)
	}
	return
}

// 从回调函数获取key对应的val。
func (g *Group) getLocally(ctx Context, key string, dest Sink) (ByteView, error) {
	err := g.getter.Get(ctx, key, dest)
	if err != nil {
		return ByteView{}, err
	}
	return dest.view()
}

//从远方服务器节点peer中获取缓存，并按照一定的概率写入本地的hotCache中。
func (g *Group) getFromPeer(ctx Context, peer ProtoGetter, key string) (ByteView, error) {
	req := &pb.GetRequest{
		Group: &g.name,
		Key:   &key,
	}
	res := &pb.GetResponse{}
	err := peer.Get(ctx, req, res)
	if err != nil {
		return ByteView{}, err
	}
	value := ByteView{b: res.Value}
	// TODO(bradfitz): use res.MinuteQps or something smart to
	// conditionally populate hotCache.  For now just do it some
	// percentage of the time.
	//把从其他服务器上获取的缓存随机概率10%写入本地缓存
	if rand.Intn(10) == 0 {
		g.populateCache(key, value, &g.hotCache)
	}
	//为了测试，改为每次都写入本地缓存
	//g.populateCache(key, value, &g.hotCache)
	return value, nil
}

// 查找本地是否存在该key，先查maincache，再查hotcache
func (g *Group) lookupCache(key string) (value ByteView, ok bool) {
	if g.cacheBytes <= 0 {
		return
	}
	value, ok = g.mainCache.get(key)
	if ok {
		return
	}
	value, ok = g.hotCache.get(key)
	return
}

//往缓存填充数据
func (g *Group) populateCache(key string, value ByteView, cache *cache) {
	if g.cacheBytes <= 0 {
		return
	}
	cache.add(key, value)

	// Evict items from cache(s) if necessary.
	// 死循环。会一直执行，直到缓存容量没有超载的情况下，才会停止循环。
	for {
		mainBytes := g.mainCache.bytes()
		hotBytes := g.hotCache.bytes()
		if mainBytes+hotBytes <= g.cacheBytes {
			return
		}

		// TODO(bradfitz): this is good-enough-for-now logic.
		// It should be something based on measurements and/or
		// respecting the costs of different resources.
		victim := &g.mainCache
		if hotBytes > mainBytes/8 {
			victim = &g.hotCache
		}
		victim.removeOldest()
	}
}

// CacheType represents a type of cache.
type CacheType int

const (
	// The MainCache is the cache for items that this peer is the
	// owner for.
	MainCache CacheType = iota + 1

	// The HotCache is the cache for items that seem popular
	// enough to replicate to this node, even though it's not the
	// owner.
	HotCache
)

// CacheStats returns stats about the provided cache within the group.
func (g *Group) CacheStats(which CacheType) CacheStats {
	switch which {
	case MainCache:
		return g.mainCache.stats()
	case HotCache:
		return g.hotCache.stats()
	default:
		return CacheStats{}
	}
}

// cache is a wrapper around an *lru.Cache that adds synchronization,
// makes values always be ByteView, and counts the size of all keys and
// values.
//Cache的实现很简单基本可以认为就是直接在LRU上面包了一层，加上了统计信息，锁，以及大小限制
type cache struct {
	mu         sync.RWMutex
	nbytes     int64 // of all keys and values
	lru        *lru.Cache
	nhit, nget int64
	nevict     int64 // number of evictions
}

//返回统计信息，加读锁
func (c *cache) stats() CacheStats {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return CacheStats{
		Bytes:     c.nbytes,
		Items:     c.itemsLocked(),
		Gets:      c.nget,
		Hits:      c.nhit,
		Evictions: c.nevict,
	}
}

// 加入一个kv，如果cache的大小超过nbytes了，就淘汰
func (c *cache) add(key string, value ByteView) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lru == nil {
		c.lru = &lru.Cache{
			OnEvicted: func(key lru.Key, value interface{}) {
				val := value.(ByteView)
				c.nbytes -= int64(len(key.(string))) + int64(val.Len())
				c.nevict++
			},
		}
	}
	c.lru.Add(key, value)
	c.nbytes += int64(len(key)) + int64(value.Len())
}

// 根据key返回value
func (c *cache) get(key string) (value ByteView, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nget++
	if c.lru == nil {
		return
	}
	vi, ok := c.lru.Get(key)
	if !ok {
		return
	}
	c.nhit++
	return vi.(ByteView), true
}

// 删除最久没被访问的数据
func (c *cache) removeOldest() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lru != nil {
		c.lru.RemoveOldest()
	}
}

// 获取当前cache的大小
func (c *cache) bytes() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.nbytes
}

// 获取当前cache中kv的个数，内部会加读锁
func (c *cache) items() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.itemsLocked()
}

// 获取当前cache中kv的个数，函数名中的Locked的意思是调用方已经加锁，比如上面的stats方法
func (c *cache) itemsLocked() int64 {
	if c.lru == nil {
		return 0
	}
	return int64(c.lru.Len())
}

// An AtomicInt is an int64 to be accessed atomically.
type AtomicInt int64

// Add atomically adds n to i.
func (i *AtomicInt) Add(n int64) {
	atomic.AddInt64((*int64)(i), n)
}

// Get atomically gets the value of i.
func (i *AtomicInt) Get() int64 {
	return atomic.LoadInt64((*int64)(i))
}

func (i *AtomicInt) String() string {
	return strconv.FormatInt(i.Get(), 10)
}

// CacheStats are returned by stats accessors on Group.
// 缓存统计信息
type CacheStats struct {
	Bytes     int64 //大小
	Items     int64 //缓存项数量
	Gets      int64 //请求次数
	Hits      int64 //命中次数
	Evictions int64
}
