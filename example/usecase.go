package main

import (
	"fmt"
	"github.com/tfbrother/groupcache"
	"log"
	"net/http"
	"os"
)

var (
	peers_addrs = []string{"http://127.0.0.1:8001", "http://127.0.0.1:8002", "http://127.0.0.1:8003"}
)

func main() {
	if len(os.Args) != 2 {
		fmt.Println("\r\n Usage local_addr \t\n local_addr must in(127.0.0.1:8001,localhost:8002,127.0.0.1:8003)\r\n")
		os.Exit(1)
	}
	local_addr := os.Args[1]
	peers := groupcache.NewHTTPPool("http://" + local_addr)
	peers.Set(peers_addrs...)
	var image_cache = groupcache.NewGroup("image", 8<<30, groupcache.GetterFunc(
		func(ctx groupcache.Context, key string, dest groupcache.Sink) error {
			result := key + "123"
			dest.SetBytes([]byte(result))
			return nil
		}))
	http.HandleFunc("/image", func(rw http.ResponseWriter, r *http.Request) {
		var data []byte
		k := r.URL.Query().Get("id")
		fmt.Printf("user get %s from groupcache\n", k)
		image_cache.Get(nil, k, groupcache.AllocatingByteSliceSink(&data))
		rw.Write([]byte(data))
		log.Println("Gets: ", image_cache.Stats.Gets.String())
		log.Println("Load: ", image_cache.Stats.Loads.String())
		log.Println("LocalLoad: ", image_cache.Stats.LocalLoads.String())
		log.Println("PeerError: ", image_cache.Stats.PeerErrors.String())
		log.Println("PeerLoad: ", image_cache.Stats.PeerLoads.String())
	})
	log.Fatal(http.ListenAndServe(local_addr, nil))
}
