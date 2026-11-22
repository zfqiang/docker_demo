# MySQL 一主一从复制示例

使用 Docker Compose 启动两个 MySQL 8.0 实例：一个主库和一个只读从库。Compose 负责启动容器、健康检查和网络连接；**主从复制关系需要按本文步骤手动配置**，容器启动并不代表复制已经开始。

> 此配置用于本地学习和测试，不是生产环境模板。Compose 中的 root 密码是示例明文值，使用前请替换。不要将真实凭据提交到版本库。

## 架构与端口

| Compose 服务 | 角色 | 容器端口 | 宿主机端口 | Docker 网络内地址 |
| --- | --- | ---: | ---: | --- |
| `mysql-master` | 主库 | 3306 | 3307 | `mysql-master:3306` |
| `mysql-slave` | 从库 | 3306 | 3308 | `mysql-slave:3306` |

两个实例位于 `mysql-net` bridge 网络。应用从宿主机连接时使用 `127.0.0.1:3307` 或 `127.0.0.1:3308`；容器间连接则使用服务名和容器端口。

## 目录结构

```text
mysql-replication/
├── docker-compose.yml
├── docker_cmd.md
├── README.md
├── master/
│   ├── conf/my.cnf
│   └── data/                 # 主库持久化数据
└── slave/
    ├── conf/my.cnf
    └── data/                 # 从库持久化数据
```

当前 Compose 将 `master/data` 和 `slave/data` 分别挂载到容器的 `/var/lib/mysql`。配置文件 `master/conf/my.cnf` 和 `slave/conf/my.cnf` **当前没有挂载到容器**；MySQL 参数已写在 Compose 的 `command` 中，以避免 Windows bind mount 的配置文件被 MySQL 判定为全局可写并忽略。

## 启动与状态检查

在本目录运行：

```sh
docker compose config
docker compose up -d
docker compose ps
```

主库通过健康检查后，从库才会启动。健康检查使用 `mysqladmin --no-defaults ping`，每 10 秒执行一次，首次启动宽限期为 10 分钟。首次初始化数据目录时可能需要等待数分钟。

查看日志：

```sh
docker compose logs -f --tail=100
docker compose logs -f mysql-master
docker compose logs -f mysql-slave
```

两个容器均配置 Docker `local` 日志轮转：每个日志文件最多 10 MB，最多保留 3 个文件。

## 当前数据库参数

- 主库：`server-id=101`，启用 `mysql-bin` 二进制日志，字符集为 `utf8mb4`。
- 从库：`server-id=102`，启用 `mysql-relay-bin` 中继日志，并设置为只读。
- 主库首次初始化时创建 `test_db`。从库的 `MYSQL_ROOT_PASSWORD` 只负责首次初始化 root 账号，不会自动创建 `test_db`。
- `MYSQL_ROOT_PASSWORD` 只在数据目录首次初始化时生效。已有数据目录时，修改 Compose 中的密码不会自动修改已有 root 密码。
- Compose 当前没有自动创建复制用户，也没有执行 `CHANGE REPLICATION SOURCE TO` 或 `START REPLICA`。

## 配置主从复制

以下示例假定数据目录是新建的空目录，root 密码使用 Compose 中的示例值 `Root@123456`。生产或已有数据环境请勿直接照搬密码和日志坐标。

### 1. 在主库创建复制账号

进入主库 MySQL：

```sh
docker exec -it mysql-master mysql -uroot -p
```

输入 root 密码后执行：

```sql
CREATE USER 'repl_user'@'%' IDENTIFIED BY 'ReplaceWithStrongPassword';
GRANT REPLICATION SLAVE, REPLICATION CLIENT ON *.* TO 'repl_user'@'%';
SHOW MASTER STATUS;
```

记录 `File` 和 `Position` 的值，下一步会用到。若 `SHOW MASTER STATUS` 没有结果，先确认主库已健康且启用了二进制日志。

### 2. 准备从库数据库并连接主库

登录从库：

```sh
docker exec -it mysql-slave mysql -uroot -p
```

输入 root 密码。由于 `test_db` 是主库初始化时创建的，而复制尚未启动，在从库先创建同名空库：

```sql
CREATE DATABASE IF NOT EXISTS test_db;
```

使用第一步实际查询到的日志文件和位置配置复制（不要照抄示例坐标）：

```sql
CHANGE REPLICATION SOURCE TO
  SOURCE_HOST = 'mysql-master',
  SOURCE_PORT = 3306,
  SOURCE_USER = 'repl_user',
  SOURCE_PASSWORD = 'ReplaceWithStrongPassword',
  SOURCE_LOG_FILE = 'mysql-bin.000001',
  SOURCE_LOG_POS = 123,
  GET_SOURCE_PUBLIC_KEY = 1;

START REPLICA;
SHOW REPLICA STATUS\G
```

将 `SOURCE_LOG_FILE` 和 `SOURCE_LOG_POS` 替换为第一步查到的值，并将密码替换为创建复制账号时使用的值。`GET_SOURCE_PUBLIC_KEY = 1` 用于本示例无 TLS 的复制连接进行认证。

复制正常时，状态中应看到：

```text
Replica_IO_Running: Yes
Replica_SQL_Running: Yes
```

若不是 `Yes`，查看 `Last_IO_Error` 和 `Last_SQL_Error`。较旧的 MySQL 8.0 小版本也可能使用旧命令 `SHOW SLAVE STATUS\G`。

### 3. 验证数据同步

在主库创建表并写入测试记录：

```sql
USE test_db;
CREATE TABLE IF NOT EXISTS users (
  id INT PRIMARY KEY,
  name VARCHAR(50)
);
INSERT INTO users (id, name) VALUES (1, 'Alice');
```

在从库查询：

```sql
USE test_db;
SELECT * FROM users;
```

从库应能查到刚插入的记录。测试完成后，按需删除测试数据。

## 停止与数据持久化

```sh
docker compose down
```

此命令会停止并移除容器和 Compose 网络，但不会删除绑定挂载的 `master/data`、`slave/data`。这些目录中包含真实 MySQL 数据文件，不要手动删除或在数据库运行时直接复制。

## 排查提示

- 检查服务健康状态：`docker compose ps -a`。
- 查看启动错误：`docker compose logs --tail=100 mysql-master` 或 `docker compose logs --tail=100 mysql-slave`。
- 宿主机连接失败时，确认 3307/3308 未被其他程序占用，并连接正确端口。
- 修改初始化环境变量不会重置已有数据目录中的账号或数据库。
- 健康检查只验证 MySQL 是否能响应，不验证复制线程是否运行；复制状态需在从库执行 `SHOW REPLICA STATUS\G` 检查。
- 本例没有设置 TLS、复制监控、自动故障切换或备份策略，不应直接作为生产高可用方案。