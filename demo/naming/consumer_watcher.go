package main

import (
	"context"
	"go.etcd.io/etcd/client/v3"
	"log"
	"time"
)

const (
	etcdEndpoints2 = "localhost:23790"       // etcd 服务地址
	serviceKey2    = "/services/my-service/" // 服务前缀
)

func main() {
	// 连接到 etcd
	client, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{etcdEndpoints2},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		log.Fatalf("Failed to connect to etcd: %v", err)
	}
	defer client.Close()

	// 启动服务监听协程
	go watchServiceChanges(client)

	// 模拟主程序运行
	select {} // 保持主程序运行
}

// 实时监听服务的上线和下线
func watchServiceChanges(client *clientv3.Client) {
	// 创建 Watcher
	watchChan := client.Watch(context.TODO(), serviceKey2, clientv3.WithPrefix())

	log.Println("Started watching for service changes...")

	// 处理 Watch 事件
	for watchResp := range watchChan {
		for _, ev := range watchResp.Events {
			switch ev.Type {
			case clientv3.EventTypePut:
				log.Printf("Service registered or updated: %s -> %s\n", ev.Kv.Key, ev.Kv.Value)
			case clientv3.EventTypeDelete:
				log.Printf("Service removed: %s\n", ev.Kv.Key)
			}
		}
	}
}
