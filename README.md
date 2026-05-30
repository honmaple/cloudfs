# CloudFS

## Usage
 ```go
 package main

 import (
     "github.com/honmaple/cloudfs/driver/webdav"
     "github.com/honmaple/cloudfs/middleware"
 )

 func main() {
     client, err := webdav.New(&webdav.Option{})
     if err != nil {
         panic(err)
     }

     client, err = middleware.NewFS(
         client,
         middleware.PrefixFS("/aaaaa"),
         middleware.CacheFS(&middleware.CacheOption{}),
     )
     if err != nil {
         panic(err)
     }
     defer client.Close()
 }
 ```

## Drivers

| Driver         | List | Mkdir | Rename | Move | Copy | Remove | Upload | Download |
|----------------|------|-------|--------|------|------|--------|--------|----------|
| Local          | ✅   | ✅    | ✅     | ✅   | ✅   | ✅     | ✅     | ✅       |
| FTP            | ✅   | ✅    | ✅     | ✅   | ✅   | ✅     | ✅     | ✅       |
| SFTP           | ✅   | ✅    | ✅     | ✅   | ✅   | ✅     | ✅     | ✅       |
| S3             | ✅   | ✅    | ✅     | ✅   | ✅   | ✅     | ✅     | ✅       |
| SMB            | ✅   | ✅    | ✅     | ✅   | ✅   | ✅     | ✅     | ✅       |
| Webdav         | ✅   | ✅    | ✅     | ✅   | ✅   | ✅     | ✅     | ✅       |
| Foxel          | ✅   | ✅    | ✅     | ✅   | ✅   | ✅     | ✅     | ✅       |
| Openlist       | ✅   | ✅    | ✅     | ✅   | ✅   | ✅     | ✅     | ✅       |
| Upyun          | ✅   | ✅    | ✅     | ✅   | ✅   | ✅     | ✅     | ✅       |
| 115            | ✅   | ✅    | ✅     | ✅   | ✅   | ✅     | ❌     | ✅       |
| Quark          | ✅   | ✅    | ✅     | ✅   | ❌   | ✅     | ❌     | ✅       |
| Github         | ✅   | ❌    | ❌     | ❌   | ❌   | ❌     | ❌     | ✅       |
| Github Release | ✅   | ❌    | ❌     | ❌   | ❌   | ❌     | ❌     | ✅       |

## Middlewares
- PrefixFS
 ```go
 newFs, err := middleware.NewFS(fs, middleware.PrefixFS("/aaaaa"))
 ```
- CacheFS
 ```go
 newFs, err := middleware.NewFS(fs, middleware.CacheFS(&middleware.CacheOption{
     ExpireTime: 60 * time.Second,
 }))
 ```
- RateLimitFS
 ```go
 newFs, err := middleware.NewFS(fs, middleware.RateLimitFS(&middleware.RateLimitOption{
	Wait: true,
	Burst: 30,
	Limit: time.Second * 5,
 }))
 ```
 - EncryptFS
 ```go
 newFs, err := middleware.NewFS(fs, middleware.EncryptFS(&middleware.EncryptOption{
	Password: "123456",
	DirName: false,
	FileName: true,
 }))
 ```