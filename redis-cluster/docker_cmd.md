# 准备目录和配置文件

mkdir -p /data/redis/conf
mkdir -p /data/redis/data

# 创建容器

docker run -d --name redis8 -p 6379:6379 -v ./redis/conf/redis.conf:/etc/redis/redis.conf -v ./redis/data:/data redis:8.0 redis-server /etc/redis/redis.conf

# redis.conf 配置密码和aof

bind 0.0.0.0

# 当 protected-mode no 时‌：Redis ‌允许远程连接

protected-mode no  
port 6379

# 设置密码，主从通信需要

requirepass your_password

# 开启AOF持久化

appendonly yes

// 进入容器
docker exec -it redis8-6378 /bin/bash

docker run -d --name redis8-6378 -p 6378:6379 -v ./redis6378/conf/redis.conf:/etc/redis/redis.conf -v ./redis6378/data:/data redis:8.0 redis-server /etc/redis/redis.conf

执行以下命令查看 redis-master 的 IP 地址：

docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' redis8

172.17.0.2

配置从节点

# 主节点的密码，用于认证

masterauth "mypass2026"

# 【关键】指向主节点的内网IP和端口

replicaof 172.17.0.2 6379

# 确保从节点只读

replica-read-only yes

// 进入容器
docker exec -it redis8-6378 /bin/bash

redis-cli -> auth password -> info replication

出现以下信息，说明主从配置成功

# Replication

role:master  
connected_slaves:1

确认 Redis 容器的运行用户 UID‌：
docker exec -it redis8 id redis

# 输出示例：uid=999(redis) gid=999(redis)

# 注意：不同版本的 Redis 镜像 UID 可能是 999 或 1001，请以实际输出为准

docker exec -it redis8-6378 id redis
