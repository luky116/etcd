package main

import (
	"context"
	"fmt"
	"go.etcd.io/etcd/client/v3"
	"log"
	"time"
)

const (
	etcdEndpoints1 = "localhost:23790"       // etcd 服务地址
	serviceKey1    = "/services/my-service/" // 服务前缀
)

func main() {
	// 连接到 etcd
	client, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{etcdEndpoints1},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		log.Fatalf("Failed to connect to etcd: %v", err)
	}
	defer client.Close()

	// 发现服务
	services, err := discoverServices(client)
	if err != nil {
		log.Fatalf("Failed to discover services: %v", err)
	}
	log.Printf("Discovered services: %v\n", services)

	// 等待一段时间以模拟服务运行
	time.Sleep(10 * time.Second)
}

// 发现服务
func discoverServices(client *clientv3.Client) ([]string, error) {
	// 获取服务前缀下的所有键
	resp, err := client.Get(context.TODO(), serviceKey1, clientv3.WithPrefix())
	if err != nil {
		return nil, fmt.Errorf("failed to discover services: %v", err)
	}

	services := []string{}
	for _, kv := range resp.Kvs {
		services = append(services, string(kv.Value))
	}

	return services, nil
}
