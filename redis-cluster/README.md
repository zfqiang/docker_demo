# Redis 主从复制与 Sentinel 高可用示例

本目录使用 Docker Compose 启动一个 Redis 主节点、两个副本节点和三个 Sentinel 哨兵。Redis 数据节点通过主从复制同步数据；Sentinel 监控主节点，并在主节点故障时尝试将一个副本提升为新主节点。

> 这是用于学习和测试的示例配置，不等同于生产级高可用部署。默认密码较弱，Redis 和 Sentinel 端口映射到宿主机，部署前请按实际环境修改密码、访问控制和端口。

## 架构与端口

| 服务 | 容器内端口 | 宿主机端口 | 用途 |
| --- | ---: | ---: | --- |
| `redis-master` | 6379 | 6379 | 主节点，默认处理读写 |
| `redis-slave-6378` | 6379 | 6378 | 副本节点 |
| `redis-slave-6380` | 6379 | 6380 | 副本节点 |
| `redis-sentinel-1` | 26379 | 26379 | Sentinel 哨兵 |
| `redis-sentinel-2` | 26379 | 26380 | Sentinel 哨兵 |
| `redis-sentinel-3` | 26379 | 26381 | Sentinel 哨兵 |

所有容器加入 Compose 创建的 `redis-net` bridge 网络。容器之间通过 Compose 服务名通信；客户端可通过宿主机映射端口访问。

## 目录结构

```text
redis-cluster/
├── docker-compose.yml
├── README.md
├── docker_cmd.md
├── master/
│   ├── conf/redis.conf
│   ├── data/
│   └── log/
├── slave6378/
│   ├── conf/redis.conf
│   ├── data/
│   └── log/
├── slave6380/
│   ├── conf/redis.conf
│   ├── data/
│   └── log/
└── sentinel/
    ├── sentinel1/sentinel.conf
    ├── sentinel2/sentinel.conf
    └── sentinel3/sentinel.conf
```

Redis 配置和数据目录分别挂载到容器内。三个 Sentinel 使用各自的配置目录，避免多个 Sentinel 实例共同改写同一个配置文件。

## 启动

需要已安装并启动 Docker Engine 或 Docker Desktop，以及支持 Compose v2 的 Docker Compose。

在本目录运行：

```sh
docker compose config
docker compose up -d
docker compose ps
```

Compose 会先启动 Redis 数据节点；Sentinel 依赖主节点和两个副本节点达到健康状态后再启动。Redis 健康检查通过带密码的 `PING` 检查 Redis 是否可响应。

查看服务日志：

```sh
docker compose logs -f --tail=100
docker compose logs -f redis-master
docker compose logs -f redis-sentinel-1
```

日志使用 Docker `local` 驱动轮转，单个文件上限为 10 MB，最多保留 3 个文件。

## 验证主从复制和 Sentinel

当前示例密码是 `mypass`。可分别检查主节点和副本节点：

```sh
docker exec redis-master-6379 redis-cli -a mypass INFO replication
docker exec redis-slave-6378 redis-cli -a mypass INFO replication
docker exec redis-slave-6380 redis-cli -a mypass INFO replication
```

主节点的结果应显示 `role:master` 和已连接的副本数；副本节点应显示 `role:slave`（部分 Redis 版本可能显示 `role:replica`），并最终达到 `master_link_status:up`。

检查 Sentinel 当前认定的主节点：

```sh
docker exec redis-sentinel-1 redis-cli -p 26379 SENTINEL get-master-addr-by-name mymaster
```

三个 Sentinel 配置了 `quorum 2`，即至少两个 Sentinel 同意主节点不可达，才会判定客观下线并推进故障转移。Sentinel 使用 `redis-master` 服务名作为初始主节点地址，并启用了主机名解析和广播。

## 测试故障转移

在测试环境中停止主节点：

```sh
docker stop redis-master-6379
```

等待 Sentinel 检测和选举完成后，再查询主节点地址：

```sh
docker exec redis-sentinel-1 redis-cli -p 26379 SENTINEL get-master-addr-by-name mymaster
```

恢复原主节点：

```sh
docker start redis-master-6379
```

故障转移期间可能有少量尚未复制的数据丢失；Sentinel 提供自动角色切换，不保证零数据丢失。生产环境应进行专门的故障演练并评估数据持久化、备份和客户端重连策略。

## 停止与数据

```sh
docker compose down
```

该命令停止并移除容器和 Compose 网络，不会删除绑定到宿主机的 `master/data`、`slave6378/data` 和 `slave6380/data` 文件。不要手动清空这些目录，除非明确要重置对应实例的数据。

## 配置说明与注意事项

- Redis 节点使用 `mypass` 进行客户端认证和主从认证；Sentinel 使用相同密码连接 Redis 主节点。
- 将 `mypass` 替换为强密码时，请同步更新主节点、副本节点、三个 Sentinel 配置，以及 Compose 文件中主节点健康检查所使用的密码。
- Redis 配置使用 `daemonize no`，让 Redis 在容器前台运行；异常退出时 Compose 最多自动重试 5 次。
- `min-replicas-to-write 1` 和 `min-replicas-max-lag 10` 用于在健康副本不足时限制主节点写入。
- 健康检查只判断 Redis 进程能否响应 `PING`，不代表副本已经完成同步，也不代表 Sentinel 故障转移已成功。需要结合 `INFO replication` 和 Sentinel 查询结果判断。
- Sentinel 会在运行过程中更新自身配置和发现的节点信息。请确保对应的 `sentinel1`、`sentinel2`、`sentinel3` 配置目录可写，并保留各实例独立的配置文件。
- Compose 映射的端口可能对宿主机网络开放。若只需本机访问，可将端口绑定到 `127.0.0.1`，例如 `127.0.0.1:6379:6379`；不要在不可信网络中直接暴露 Redis 或 Sentinel。
- `redis-cli -a` 会提示命令行密码使用风险。生产环境应使用安全的凭据管理方式，不要提交真实密码到版本库。
