# config-center-api

把命名空间下的配置项、版本号、灰度标签和回滚版本记录成可查询的服务，支持按命名空间与环境读取生效配置并查看历史版本。

## 运行要求

- Go 1.26 或以上
- SQLite（本服务自带存储，不需要外部数据库）

## 构建、测试与启动

```bash
go build ./...
go test ./...
go run .
```

服务默认监听 `127.0.0.1:8080`。可用环境变量覆盖：

| 变量 | 默认值 | 用途 |
|---|---|---|
| `ADDR` | `127.0.0.1:8080` | HTTP 监听地址 |
| `DB_PATH` | `config-center-api.db` | SQLite 数据库文件路径 |

## 已公开的入口

### `GET /healthz`

返回服务与存储状态。正常时 HTTP 200：

```json
{"status":"ok","database":"ok"}
```

存储不可用时 HTTP 503：

```json
{"error":{"code":"storage_unavailable","message":"database is not available"}}
```

### `GET /api/v1/config-versions/diff`

比较同一命名空间与环境下的两个配置版本。查询参数：

| 参数 | 必填 | 说明 |
| --- | --- | --- |
| `namespace` | 是 | 配置命名空间 |
| `environment` | 是 | 环境，例如 `prod` |
| `baseVersion` | 是 | 基准版本号，必须为正整数 |
| `targetVersion` | 是 | 目标版本号，必须为正整数 |

系统按版本号从小到大解释差异：目标版本新增、删除或修改了哪些配置项。配置值使用原始 JSON 类型输出，不转换数字、布尔值、空值或字符串；差异项按配置项名称稳定排序。

成功返回 HTTP 200：

```json
{
  "namespace": "payments",
  "environment": "prod",
  "baseVersion": {"namespace":"payments","environment":"prod","version":3,"grayLabel":null,"rollbackSourceVersion":null,"createdAt":"..."},
  "targetVersion": {"namespace":"payments","environment":"prod","version":5,"grayLabel":"gray-a","rollbackSourceVersion":3,"createdAt":"..."},
  "currentVersion": 5,
  "changedCount": 1,
  "affectsCurrent": true,
  "diffs": [
    {"name":"timeout","type":"modified","oldValue":1000,"newValue":2000,"affectsCurrent":true}
  ]
}
```

差异类型固定为 `added`、`removed`、`modified`：`added` 返回 `newValue`，`removed` 返回 `oldValue`，`modified` 同时返回两者。两个版本相同或差异集合为空时返回 HTTP 200、`changedCount: 0` 与空 `diffs` 数组。`affectsCurrent` 表示该差异是否已经体现在当前生效配置中；查询本身不写入版本历史、灰度状态或回滚记录。

兼容路径也支持 `/api/v1/namespaces/{namespace}/environments/{environment}/versions/diff?baseVersion=...&targetVersion=...`。

固定错误：

| HTTP | code | 触发条件 |
| --- | --- | --- |
| 400 | `MISSING_SCOPE` | `namespace` 或 `environment` 为空 |
| 400 | `INVALID_VERSION` | 任一版本号不是正整数 |
| 409 | `VERSION_ORDER_CONFLICT` | 目标版本号小于基准版本号 |
| 404 | `VERSION_NOT_FOUND` | 任一版本在当前命名空间和环境中不存在 |
| 409 | `VERSION_SCOPE_MISMATCH` | 版本存在，但属于其他命名空间或环境 |

## 错误约定

所有错误响应都是单个顶层 `error` 对象，包含 `code` 与 `message` 两个字符串字段；`message` 不包含 SQL、堆栈或文件路径。
