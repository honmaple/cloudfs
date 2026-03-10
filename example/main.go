package main

import (
	// "github.com/honmaple/cloudfs"
	"github.com/honmaple/cloudfs/driver/webdav"
	"github.com/honmaple/cloudfs/middleware"
)

func main() {
	// fs, err := cloudfs.New("webdav", map[string]any{
	//	"aa": "vvv",
	// })

	fs, err := webdav.New(&webdav.Option{})
	if err != nil {
		panic(err)
	}

	fs, err = middleware.NewFS(
		fs,
		middleware.PrefixFS("/aaaaa"),
		middleware.OptionFS(&middleware.CacheOption{}),
		middleware.CacheFS(&middleware.CacheOption{}),
	)
	if err != nil {
		panic(err)
	}
	defer fs.Close()
}
