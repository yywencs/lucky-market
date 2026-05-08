# MySQL DBStats Prometheus 采样约束

## 背景

当前项目在 `internal/data/data.go` 的 `NewDB()` 中已经可以获取到底层 `*sql.DB`，因此可以基于 `sql.DB.Stats()` 做 MySQL 连接池运行态指标采样，并暴露给 Prometheus。

本次工作目标仅限于为 MySQL 连接池增加定时采样能力，不扩展到 SQL 语句级 tracing、慢查询分析、业务埋点重构或配置体系改造。

## 目标

每 10 秒采样一次 `sql.DB.Stats()`，并更新以下指标：

- `open_connections`
- `in_use`
- `idle`
- `wait_count`
- `wait_duration_seconds`
- `max_idle_closed_total`
- `max_lifetime_closed_total`

说明：

- 采样方式为后台 `goroutine + ticker`
- 采样结果更新到 Prometheus `Gauge`
- 采样对象至少覆盖 `NewDB()` 创建出的主库连接
- 如果后续要覆盖 `DBRouter` 的分库实例，必须显式说明标签设计和采样去重策略

## 允许修改

本次实现，优先只允许修改以下位置：

- `internal/metrics`
  - 新增 MySQL 连接池指标定义
  - 新增 MySQL 指标更新辅助函数
- `internal/data/data.go`
  - 在 `NewDB()` 获取到底层 `*sql.DB` 后启动采样协程
  - 如有必要，新增极小范围的私有辅助函数，例如：
    - `startMySQLStatsCollector(...)`
    - `collectMySQLStats(...)`
- 如确有必要，可新增一个仅服务于 `internal/data` 的私有文件，例如：
  - `internal/data/mysql_metrics.go`
  - 但职责必须只围绕 `DBStats()` 采样

## 禁止修改

以下内容本次绝对不要改：

- 不修改 `DBRouter` 的分库分表路由算法
- 不修改 `NewDBRouter()` 的建库初始化流程
- 不修改 DSN 拼接逻辑
- 不修改 MySQL 连接池配置值本身
  - 包括 `SetMaxIdleConns`
  - `SetMaxOpenConns`
  - `SetConnMaxLifetime`
  - `SetConnMaxIdleTime`
- 不修改 `ProviderSet` 的现有依赖装配关系，除非新增依赖是实现采样所必需且不会影响现有构造函数签名
- 不改 Redis、RabbitMQ、Asynq 相关任何逻辑
- 不改业务仓储实现
  - `activity_quota.go`
  - `activity_partake.go`
  - `award.go`
  - `strategy.go`
  - `task.go`
  - `rebate.go`
- 不引入新的配置项
  - 本次采样周期固定为 `10s`
- 不引入新的第三方依赖
- 不把采样逻辑做成阻塞启动流程
- 不因为指标采样失败而 `panic`
- 不在采样协程里打印高频日志
- 不把 `user_id`、`order_id`、SQL 文本、DSN、密码等敏感或高基数信息作为标签

## 指标设计约束

### 指标类型

本次统一使用 `Gauge`，因为这里采样的是连接池当前状态值。

### 标签约束

标签必须保持低基数，推荐仅允许以下维度：

- `db_name`
- `role`

如果当前阶段拿不到稳定且低基数的库标识，可以先不加标签，或者只加一个固定标签。

绝对不要使用以下内容做标签：

- 完整 DSN
- host:port
- 用户名
- SQL 语句
- 错误消息全文
- 动态库下标之外的任意高基数字段

### 命名约束

建议挂在 `bigmarket_mysql_*` 前缀下，例如：

- `bigmarket_mysql_open_connections`
- `bigmarket_mysql_in_use`
- `bigmarket_mysql_idle`
- `bigmarket_mysql_wait_count`
- `bigmarket_mysql_wait_duration_seconds`
- `bigmarket_mysql_max_idle_closed_total`
- `bigmarket_mysql_max_lifetime_closed_total`

注意：

- 即使名称里有 `_total`，本次仍按 `Gauge` 更新当前 `DBStats()` 值，不要额外做 `Counter` 差值累计
- `wait_duration` 必须转换成秒后再写入指标

## 实现约束

### 采样方式

采样协程要求：

- 使用 `time.NewTicker(10 * time.Second)`
- 启动后可以先立即采样一次，再进入定时循环
- 每次循环调用 `sqlDB.Stats()`
- 将统计值写入 Prometheus 指标

### 生命周期

可以接受当前版本使用“随进程生命周期存在的后台协程”方案，但必须满足：

- 协程实现简单直接
- 不阻塞 `NewDB()` 返回
- 不依赖外部 `context.Context`
- 不额外引入复杂 shutdown 编排

### 容错要求

因为 `DBStats()` 本身是内存态读取，正常情况下不会报错，因此：

- 不需要为采样逻辑设计复杂重试
- 不需要引入熔断、降级或恢复机制
- 如果出现极端异常，最多静默跳过本轮，不影响主流程

## 代码风格约束

- 只写必要注释，重点解释“为什么这么做”
- 新增函数尽量保持私有
- 函数职责单一，不把 metrics 定义、采样调度、数据库初始化揉在一个超长函数里
- 不吞掉真正会影响初始化的错误
- 不为了采样而改变现有 `panic` 初始化策略

## 验收标准

实现完成后，至少要满足以下条件：

- `go test ./...` 通过
- 新增代码通过 `gofmt`
- `NewDB()` 返回逻辑与当前行为保持一致
- 启动后 Prometheus 能看到 MySQL 连接池指标
- 指标值会随连接池状态变化而更新
- 没有引入新的高基数标签
- 没有改动业务语义和分库分表行为

## 本次不做

以下内容明确不在本次范围内：

- SQL 执行耗时埋点
- GORM callback 指标
- 慢查询日志治理
- 连接池参数自适应调整
- 按 SQL/表名维度统计
- 数据源健康探测重构
- 多数据源统一观测框架抽象

## 只能修改原则

如果后续继续实现本 spec，必须遵守：

- 优先做最小改动
- 优先复用现有 `internal/metrics` 包
- 优先把新增逻辑限制在 `internal/data` 和 `internal/metrics`
- 如果发现需要改动业务链路文件，先停下来确认，而不是直接扩散修改
- 如果发现需要改配置、改 Wire 签名、改启动流程，先停下来确认，而不是默认继续
