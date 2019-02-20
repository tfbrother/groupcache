package main

import (
	"fmt"
	"github.com/tfbrother/groupcache"
)

var thumbNails = groupcache.NewGroup("thunbnail", 512<<20, groupcache.GetterFunc(
	func(ctx groupcache.Context, key string, dest groupcache.Sink) error {
		fileName := key
		//设置回调函数，当缓存中没有查到值时，需要提供回调函数获取值，然后再把值写入缓存中，下次就能直接从缓存中读取了。
		bytes, err := generateThumbnail1(fileName)
		if err != nil {
			return err
		}
		dest.SetBytes(bytes)
		return nil
	}))

//func generateThumbnail(filename string) ([]byte, error) {
//	resp, err := http.Get("http://10.246.13.180:5000" + filename)
//	if err != nil {
//		return nil, err
//	}
//	println("RRR")
//	defer resp.Body.Close()
//	return ioutil.ReadAll(resp.Body)
//}

func generateThumbnail1(filename string) ([]byte, error) {
	fmt.Println(filename, "在缓存里面没有找到，所以从generateThumbnail1获取")
	return []byte(filename + "123"), nil
}

//func FileHandler(w http.ResponseWriter, r *http.Request) {
//	var ctx groupcache.Context
//	key := r.URL.Path
//	var data []byte
//	//fmt.Println("KEY:", key)
//	err := thumbNails.Get(ctx, key, groupcache.AllocatingByteSliceSink(&data))
//	if err != nil {
//		http.Error(w, err.Error(), 500)
//		return
//	}
//	var modTime time.Time = time.Now()
//
//	rd := bytes.NewReader(data)
//	http.ServeContent(w, r, filepath.Base(key), modTime, rd)
//}

func main() {
	key := "tfbrother"
	var ctx groupcache.Context
	var data []byte
	err := thumbNails.Get(ctx, key, groupcache.AllocatingByteSliceSink(&data))
	if err != nil {
		fmt.Println(err.Error())
		return
	}
	fmt.Println(string(data))

	err = thumbNails.Get(ctx, key, groupcache.AllocatingByteSliceSink(&data))
	if err != nil {
		fmt.Println(err.Error())
		return
	}
	fmt.Println(string(data))
}
