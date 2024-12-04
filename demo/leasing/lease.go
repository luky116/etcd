package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"go.etcd.io/etcd/client/v3"
)

func main() {
	// 连接到 ETCD 集群
	client, err := clientv3.New(clientv3.Config{
		Endpoints: []string{"localhost:2379"},
	})
	if err != nil {
		log.Fatalf("Failed to connect to ETCD: %v", err)
	}
	defer client.Close()

	// 创建租约，租约有效期为 10 秒
	leaseResp, err := client.Grant(context.Background(), 10)
	if err != nil {
		log.Fatalf("Failed to create lease: %v", err)
	}

	// 使用租约设置一个键值对
	_, err = client.Put(context.Background(), "service/instance1", "192.168.1.100", clientv3.WithLease(leaseResp.ID))
	if err != nil {
		log.Fatalf("Failed to put key with lease: %v", err)
	}
	fmt.Println("Key 'service/instance1' set with lease ID", leaseResp.ID)

	// 查询并获取租约信息
	leaseInfo, err := client.TimeToLive(context.Background(), leaseResp.ID)
	if err != nil {
		log.Fatalf("Failed to get lease info: %v", err)
	}
	fmt.Printf("Lease ID %d has %d seconds remaining\n", leaseResp.ID, leaseInfo.TTL)

	// 等待租约过期
	fmt.Println("Waiting for the lease to expire...")
	time.Sleep(12 * time.Second) // 等待 12 秒，租约将到期

	// 查看键是否已被删除
	resp, err := client.Get(context.Background(), "service/instance1")
	if err != nil {
		log.Fatalf("Failed to get key: %v", err)
	}
	if len(resp.Kvs) == 0 {
		fmt.Println("Key 'service/instance1' has been deleted after lease expiration")
	} else {
		fmt.Printf("Key 'service/instance1' still exists with value: %s\n", resp.Kvs[0].Value)
	}
}
