package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"go.etcd.io/etcd/client/v3"
)

type ServiceConfig struct {
	Name     string            `json:"name"`
	Port     int               `json:"port"`
	Features []string          `json:"features"`
	Settings map[string]string `json:"settings"`
}

func main() {
	// 连接到 etcd
	client, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{"localhost:12379"},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		log.Fatalf("Error connecting to etcd: %v", err)
	}
	defer client.Close()

	// 定义 JSON 数据
	config := ServiceConfig{
		Name:     "service1",
		Port:     8080,
		Features: []string{"featureA", "featureB"},
		Settings: map[string]string{"retry": "3", "timeout": "30s"},
	}

	// 将数据编码为 JSON
	jsonData, err := json.Marshal(config)
	if err != nil {
		log.Fatalf("Error marshaling JSON: %v", err)
	}

	// 存储到 etcd
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = client.Put(ctx, "/config/service1", string(jsonData))
	if err != nil {
		log.Fatalf("Error putting value in etcd: %v", err)
	}

	// 从 etcd 读取数据
	resp, err := client.Get(ctx, "/config/service1")
	if err != nil {
		log.Fatalf("Error getting value from etcd: %v", err)
	}

	// 解码 JSON 数据
	for _, kv := range resp.Kvs {
		var retrievedConfig ServiceConfig
		err = json.Unmarshal(kv.Value, &retrievedConfig)
		if err != nil {
			log.Fatalf("Error unmarshaling JSON: %v", err)
		}
		fmt.Printf("Retrieved Config: %+v\n", retrievedConfig)
	}
}
