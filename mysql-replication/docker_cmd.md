🚀 4. 启动与初始化流程
第一步：启动容器
bash
docker-compose up -d

第二步：配置 Master 主库

进入 Master 容器‌：

bash
docker exec -it mysql-master mysql -uroot -pRoot@123456

创建同步用户并授权‌：
MySQL 8.0 默认使用 caching_sha2_password 认证，为了兼容性和稳定性，建议显式指定为 mysql_native_password。

sql
-- 创建用于主从复制的用户
CREATE USER 'repl_user'@'%' IDENTIFIED WITH mysql_native_password BY 'Repl@123456';

-- 授予复制权限
GRANT REPLICATION SLAVE, REPLICATION CLIENT ON _._ TO 'repl_user'@'%';

-- 刷新权限
FLUSH PRIVILEGES;

查看 Master 状态‌：
执行后记录 File 和 Position 的值，后续配置从库时需要用到。

sql
SHOW MASTER STATUS;

示例输出：

表格
File Position Binlog_Do_DB Binlog_Ignore_DB
mysql-bin.000001 156
第三步：配置 Slave 从库

进入 Slave 容器‌：

bash
docker exec -it mysql-slave mysql -uroot -pRoot@123456

配置主从连接信息‌：
注意：MySQL 8.0.23+ 推荐使用 CHANGE REPLICATION SOURCE TO，旧版本使用 CHANGE MASTER TO。这里使用通用性更强的旧语法演示，若报错请替换为新语法。

关键点：MASTER_HOST 填写的是 Docker 网络中的服务名 mysql-master，而不是 localhost 或 127.0.0.1。

sql
CHANGE MASTER TO
MASTER_HOST='mysql-master',
MASTER_USER='repl_user',
MASTER_PASSWORD='Repl@123456',
MASTER_PORT=3306,
MASTER_LOG_FILE='mysql-bin.000001', -- 替换为上面SHOW MASTER STATUS查到的File
MASTER_LOG_POS=156; -- 替换为上面SHOW MASTER STATUS查到的Position

启动复制进程‌：

sql
START SLAVE;

验证同步状态‌：

sql
SHOW SLAVE STATUS\G

成功标志‌：以下两个字段必须同时为 Yes。

Slave_IO_Running: Yes
Slave_SQL_Running: Yes

如果显示 Connecting 或 No，请查看 Last_IO_Error 或 Last_SQL_Error 字段排查错误。

✅ 5. 功能验证

在主库创建测试数据‌：

sql
-- 在 mysql-master 容器中执行
CREATE DATABASE IF NOT EXISTS test_db;
USE test_db;
CREATE TABLE users (id INT PRIMARY KEY, name VARCHAR(50));
INSERT INTO users VALUES (1, 'Alice');

在从库查询数据‌：

sql
-- 在 mysql-slave 容器中执行
USE test_db;
SELECT \* FROM users;

如果能查到 Alice，说明主从同步配置成功。
