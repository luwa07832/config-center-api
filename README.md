# config-center-api

把命名空间下的配置项、版本号、灰度标签和回滚版本记录成可查询的服务，支持按命名空间与环境读取生效配置、查看历史版本，并比较两个历史版本的配置项差异。

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

### `POST /namespaces/:namespace/environments/:environment/config-versions`

发布一个新的配置快照。请求体：

```json
{
  "grayTag": "canary",
  "items": {"timeout": "\"30\"", "retries": "3", "feature": "true"}
}
```

- `items` 的每个值都是一段原始 JSON：数字、布尔值、`null` 和字符串按原语义保存，不做类型转换。
- 省略或传空 `grayTag` 表示全量发布，立即成为该命名空间与环境的生效版本；带 `grayTag` 的灰度发布不会改变当前生效版本。
- 版本号在同一命名空间与环境内从 1 开始单调递增。返回 HTTP 201 与新版本元数据。

### `POST /namespaces/:namespace/environments/:environment/config-versions/:version/rollback`

把指定历史版本的快照复制为一个新版本，并在新版本上记录 `rollbackOf` 来源版本号。源版本的灰度标签一并复制。源版本在当前作用域不存在时返回 HTTP 404 `VERSION_NOT_FOUND`。

### `GET /effective-configs?namespace=...&environment=...`

返回当前生效（最新全量发布）的版本号、灰度标签与配置项。尚无全量发布时 `effectiveVersion` 与 `grayTag` 为 `null`、`items` 为空对象。命名空间或环境缺失时返回 HTTP 400 `MISSING_SCOPE`。

### `GET /config-versions?namespace=...&environment=...`

按版本号升序返回该作用域的历史版本列表，每个版本包含版本号、灰度标签、回滚来源 `rollbackOf`、创建时间以及该版本是否为当前生效版本（`effective`）。

### `GET /config-item-histories?namespace=...&environment=...&name=...`

返回单个配置项在各版本间的变化历史，纯只读，不产生任何落盘记录，也不改变版本历史、灰度状态或回滚记录。`name` 按原样匹配，不裁剪空白。HTTP 200 响应：

```json
{
  "namespace": "payments",
  "environment": "prod",
  "name": "timeout",
  "effectiveVersion": 4,
  "effectiveItem": {"present": true, "value": "\"60\""},
  "changes": [
    {"version": {"namespace": "payments", "environment": "prod", "version": 1, "grayTag": null, "rollbackOf": null, "createdAt": "2026-10-01T10:00:00Z", "effective": false}, "changeType": "added", "newValue": "\"30\""},
    {"version": {"namespace": "payments", "environment": "prod", "version": 2, "grayTag": null, "rollbackOf": null, "createdAt": "2026-10-01T10:05:00Z", "effective": false}, "changeType": "modified", "oldValue": "\"30\"", "newValue": "\"60\""}
  ],
  "totalChanges": 2
}
```

历史语义：

- `effectiveVersion` 是最新全量发布版本，尚无全量发布时为 `null`。
- `effectiveItem` 描述该项在当前生效版本中的状态：存在时 `present` 为 `true` 并携带原样 JSON `value`（`null` 值照常返回）；不存在时 `present` 为 `false` 且不返回 `value`。
- `changes` 按版本号升序；每个版本与紧邻前一版本比较该项的状态，灰度与回滚版本同样参与。首次出现为 `added`、删除为 `removed`、再次出现为 `added`、值变化为 `modified`，无变化的版本不列入。
- `added` 只返回 `newValue`，`removed` 只返回 `oldValue`，`modified` 同时返回两者；`totalChanges` 等于 `changes` 数量。
- 值保留原始 JSON 语义（数字、布尔、`null`、字符串不转换），仅做对象键排序和空白规范化，因此 `1` 与 `1.0`、`"1"` 与 `1` 仍视为不同。
- 名称从未出现时 `effectiveItem.present` 为 `false`、`changes` 为空数组、`totalChanges` 为 0，`effectiveVersion` 仍按当前状态给出；名称只在历史版本出现时正常返回其变化。

### `GET /namespaces/:namespace/environments/:environment/config-versions/:version`

读取任意历史版本保存的完整配置快照，纯只读，不新增版本，也不改变生效版本、灰度标签、回滚记录或历史顺序。命中时 HTTP 200：

```json
{
  "namespace": "payments",
  "environment": "prod",
  "version": {"namespace": "payments", "environment": "prod", "version": 2, "grayTag": "canary", "rollbackOf": null, "createdAt": "2026-10-01T10:05:00Z", "effective": false},
  "items": {"retries": "5", "timeout": "\"30\""}
}
```

- `version` 对象沿用历史版本元数据语义，包含版本号、`grayTag`、`rollbackOf`、`createdAt` 和 `effective`。
- `items` 返回该快照当时保存的全部配置项；该版本没有配置项时 `items` 为空对象。
- 值保持服务已保存的原始 JSON 语义：数字、布尔、`null` 和字符串不做类型转换，`1` 与 `1.0`、字符串 `"1"` 不合并。
- 路径中的 `version` 必须是纯十进制正整数：`0`、负数、带符号数或其他非正整数写法返回 HTTP 400 `INVALID_VERSION`。

### `GET /config-version-diffs?namespace=...&environment=...&baseVersion=1&targetVersion=2`

历史版本差异查询，纯只读，不产生任何落盘记录，也不改变版本历史、灰度状态或回滚记录。也支持路径形式：

```text
GET /namespaces/:namespace/environments/:environment/config-version-diffs/:base/:target
```

比较始终按版本号从小到大执行（`baseVersion <= targetVersion`）。HTTP 200 响应：

```json
{
  "namespace": "payments",
  "environment": "prod",
  "baseVersion": {"namespace": "payments", "environment": "prod", "version": 1, "grayTag": null, "rollbackOf": null, "createdAt": "2026-10-01T10:00:00Z", "effective": false},
  "targetVersion": {"namespace": "payments", "environment": "prod", "version": 2, "grayTag": "canary", "rollbackOf": null, "createdAt": "2026-10-01T10:05:00Z", "effective": false},
  "effectiveVersion": 1,
  "changedCount": 2,
  "changes": [
    {"name": "retries", "changeType": "modified", "oldValue": "3", "newValue": "5", "affectsEffectiveConfig": true},
    {"name": "timeout", "changeType": "added", "newValue": "\"30\"", "affectsEffectiveConfig": false}
  ]
}
```

差异语义：

- 按配置项名称归并，输出顺序按名称字典序稳定排列。
- `changeType` 固定为 `added`、`removed`、`modified`：`added` 只返回 `newValue`，`removed` 只返回 `oldValue`，`modified` 同时返回两者。
- 值保留原始 JSON 语义（数字、布尔、`null`、字符串不转换）；仅做对象键排序和空白规范化，因此 `1` 与 `1.0`、`"1"` 与 `1` 仍视为不同。
- `affectsEffectiveConfig` 表示该差异当前是否体现在生效配置上：灰度目标版本与生效版本不一致的差异为 `false`。
- 两个版本相同或差异集合为空时返回 HTTP 200 且 `changedCount` 为 0、`changes` 为空数组。

## 错误约定

所有错误响应都是单个顶层 `error` 对象，包含 `code` 与 `message` 两个字符串字段；`message` 不包含 SQL、堆栈或文件路径。版本差异查询的固定错误结果如下，不会被替换为空差异或静默忽略：

| HTTP | code | 触发条件 |
|---|---|---|
| 400 | `MISSING_SCOPE` | `namespace` 或 `environment` 为空 |
| 400 | `MISSING_ITEM_NAME` | 单项历史查询的 `name` 缺失或为空 |
| 400 | `INVALID_VERSION` | `baseVersion` 或 `targetVersion` 不是正整数 |
| 409 | `VERSION_ORDER_CONFLICT` | `targetVersion` 小于 `baseVersion` |
| 404 | `VERSION_NOT_FOUND` | 任一版本在任何命名空间与环境中都不存在 |
| 409 | `VERSION_SCOPE_MISMATCH` | 版本存在，但属于其他命名空间或环境 |

历史快照读取（`GET .../config-versions/:version`）复用同一套版本查找顺序：版本号在任何命名空间与环境中都不存在时返回 404 `VERSION_NOT_FOUND`；版本号存在但属于其他命名空间或环境时返回 409 `VERSION_SCOPE_MISMATCH`；路径段不是纯十进制正整数时返回 400 `INVALID_VERSION`。
