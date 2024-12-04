package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"go.etcd.io/etcd/client/v3"
)

const (
	etcdEndpoints = "localhost:23790"       // etcd 服务地址
	serviceKey    = "/services/my-service/" // 服务前缀
)

func main() {
	// 连接到 etcd
	client, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{etcdEndpoints},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		log.Fatalf("Failed to connect to etcd: %v", err)
	}
	defer client.Close()

	// 注册服务
	serviceID := "instance-1"
	address := "127.0.0.1:8080"
	if err := registerService(client, serviceID, address, 5); err != nil {
		log.Fatalf("Failed to register service: %v", err)
	}
	log.Printf("Service %s registered at %s\n", serviceID, address)

	// 等待一段时间以模拟服务运行
	time.Sleep(10000 * time.Second)
}

// 注册服务
func registerService(client *clientv3.Client, serviceID, address string, leaseTTL int64) error {
	// 创建租约
	leaseResp, err := client.Grant(context.TODO(), leaseTTL)
	if err != nil {
		return fmt.Errorf("failed to create lease: %v", err)
	}

	// 设置带租约的服务注册信息
	key := serviceKey + serviceID
	_, err = client.Put(context.TODO(), key, address, clientv3.WithLease(leaseResp.ID))
	if err != nil {
		return fmt.Errorf("failed to register service: %v", err)
	}

	// 启动续租
	ch, err := client.KeepAlive(context.TODO(), leaseResp.ID)
	if err != nil {
		return fmt.Errorf("failed to start keepalive: %v", err)
	}

	// 异步监听续租响应
	go func() {
		for ka := range ch {
			log.Printf("Lease keepalive response received for service %s: TTL=%d\n", serviceID, ka.TTL)
		}
	}()

	return nil
}
