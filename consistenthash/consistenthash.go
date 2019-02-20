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

// Package consistenthash provides an implementation of a ring hash.
/*一致性哈希算法,参考分析：
http://blog.segmentfault.com/lds/1190000000414004，
http://blog.codinglabs.org/articles/consistent-hashing.html
一致性哈希算法主要使用在分布式数据存储系统中，按照一定的策略将数据尽可能均匀分布到
所有的存储节点上去，使得系统具有良好的负载均衡性能和扩展性。
*/
package consistenthash

import (
	"hash/crc32"
	"sort"
	"strconv"
)

type Hash func(data []byte) uint32

//Map 结构，定义核心数据结构,其中hash是哈希函数，用于对key进行hash，keys字段保存所有的节点（包括虚拟节点）是可排序的，
//hashmap 则是虚拟节点到真实节点的映射。一致性哈希算法在服务节点太少时，容易因为节点分部不均匀而造成数据倾斜问题。
//一致性哈希算法引入了虚拟节点机制，即对每一个服务节点计算多个哈希，每个计算结果位置都放置一个此服务节点，称为虚拟节点。
type Map struct {
	hash     Hash  // hash 函数
	replicas int   // replicas是指的是每个节点和虚拟节点的个数。
	keys     []int // Sorted
	hashMap  map[int]string
}

func New(replicas int, fn Hash) *Map {
	m := &Map{
		replicas: replicas,
		hash:     fn,
		hashMap:  make(map[int]string),
	}
	if m.hash == nil {
		m.hash = crc32.ChecksumIEEE
	}
	return m
}

// Returns true if there are no items available.
func (m *Map) IsEmpty() bool {
	return len(m.keys) == 0
}

// Adds some keys to the hash.
/*Map的Add方法，添加节点到圆环，参数是一个或者多个string，对每一个key关键字进行哈希，
这样每台机器就能确定其在哈希环上的位置，在添加每个关键字的时候，并添加对应的虚拟节点，
每个真实节点和虚拟节点个数由replicas字段指定，保存虚拟节点到真实节点的对应关系到hashmap字段。
比如在测试用例中，hash.Add("6", "4", "2")，则所有的节点是 2, 4, 6, 12, 14, 16, 22, 24, 26,
当has.Get('11') 时，对应的节点是12，而12对应的真实节点是2

hash.Add("6", "4", "2")是数据值：

2014/02/20 15:45:16 replicas: 3
2014/02/20 15:45:16 keys: [2 4 6 12 14 16 22 24 26]
2014/02/20 15:45:16 hashmap map[16:6 26:6 4:4 24:4 2:2 6:6 14:4 12:2 22:2]

说明: 此处添加虚拟节点的算法是有问题的，比如replicas = 3，传入的key为2,12,22，则就会出现某个节点的虚拟实际上就是
另外的真实节点。不过在groupcache中的应用不存在这个情况，因为keys都是字符串，而且是host:port形式的字符串，所以
就不会出现上面的情况。
在php-memcache中也实现了一致性hash算法的，在计算虚拟节点的时候，采用的是类似的算法。相关源码是：
memcache_consistent_hash.c mmc_consistent_add_server函数内部：
for (i=0; i<points; i++) {
	key_len = sprintf(key, "%s:%d-%d", mmc->host, mmc->port, i);
	state->points[state->num_points + i].server = mmc;
	state->points[state->num_points + i].point = state->hash(key, key_len);
	MMC_DEBUG(("mmc_consistent_add_server: key %s, point %lu", key, state->points[state->num_points + i].point));
}
*/
func (m *Map) Add(keys ...string) {
	for _, key := range keys {
		for i := 0; i < m.replicas; i++ {
			hash := int(m.hash([]byte(strconv.Itoa(i) + key)))
			m.keys = append(m.keys, hash)
			m.hashMap[hash] = key
		}
	}
	sort.Ints(m.keys)
}

// Gets the closest item in the hash to the provided key.
//Get方法根据提供的key定位数据访问到相应服务器节点，算法是：将数据key使用相同的哈希函数H计算出哈希值h，
//通根据h确定此数据在环上的位置，从此位置沿环顺时针“行走”，第一台遇到的服务器就是其应该定位到的服务器。
func (m *Map) Get(key string) string {
	if m.IsEmpty() {
		return ""
	}

	hash := int(m.hash([]byte(key)))

	// Linear search for appropriate replica.
	/*
		顺时针“行走”，找到第一个大于哈希值的节点
		此处其实可以进行优化，m.keys是有序的，此处是采用的顺序查找算法，当keys较多的时候还是会影响
		性能，所以可以采用其它效率更高的查找算法。
	*/
	for _, v := range m.keys {
		if v >= hash {
			return m.hashMap[v] // 返回真实节点
		}
	}

	// Means we have cycled back to the first replica.
	// hash值大于最大节点哈希值的情况
	return m.hashMap[m.keys[0]]
}
