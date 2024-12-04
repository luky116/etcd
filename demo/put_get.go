// https://etcd.io/docs/v3.3/rfc/
// https://blog.csdn.net/qq_39557240/article/details/106770513

package main

import (
	"context"
	"fmt"
	"go.etcd.io/etcd/client/v3"
	"time"
)

func main() {
	cli, err := clientv3.New(clientv3.Config{
		//Endpoints:   []string{"127.0.0.1:12379"},
		Endpoints:   []string{"127.0.0.1:23790"},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		fmt.Println("连接到 etcd 失败:", err)
		return
	}
	defer cli.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 设置键值对
	_, err = cli.Put(ctx, "/services/my-service/111", "sample_value111")
	if err != nil {
		fmt.Println("设置键值对失败:", err)
		return
	}

	// 获取键值对
	resp, err := cli.Get(ctx, "sample_key")
	if err != nil {
		fmt.Println("获取键值对失败:", err)
		return
	}
	for _, ev := range resp.Kvs {
		fmt.Printf("%s : %s\n", ev.Key, ev.Value)
	}
}
