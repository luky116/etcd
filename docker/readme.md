1、创建etcd数据目录
mkdir -p etcd-node{1,2,3}

2、创建docker网络
docker network create --driver bridge --subnet 172.20.0.0/16 --gateway 172.20.0.1 etcd-cluster


3、本地查询集群节点状态
etcdctl --endpoints=127.0.0.1:12379,127.0.0.1:22379,127.0.0.1:32379 member list -w table

etcdctl --endpoints=127.0.0.1:12379,127.0.0.1:22379,127.0.0.1:32379 endpoint status -w table
