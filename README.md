# config-center-api

把命名空间下的配置项、版本号、灰度标签和回滚版本记录成可查询的服务，支持按命名空间与环境读取生效配置、查看历史版本、比较两个历史版本的配置项差异，对比同一命名空间下两个环境的当前生效配置，并比较同一命名空间下两个环境各自历史版本的差异。

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

### `POST /namespaces/:namespace/environments/:environment/config-versions/:version/promote`

把一个灰度版本转为正式生效版本，无需业务请求体（发送 `{}` 或空请求体都可以）。服务读取路径版本的完整快照，在同一命名空间与环境生成下一个版本：新版本复制全部配置项与原样 JSON 值，不带 `grayTag`，立即成为生效版本，并用 `promotionOf` 记录源版本号。源版本及其余历史保持不变；普通发布、回滚以及旧版本的 `promotionOf` 为 `null`，回滚行为不变。成功返回 HTTP 201：

```json
{
  "namespace": "payments",
  "environment": "prod",
  "version": {"namespace": "payments", "environment": "prod", "version": 3, "grayTag": null, "rollbackOf": null, "promotionOf": 2, "createdAt": "2026-10-01T10:10:00Z", "effective": true},
  "items": {"retries": "5", "timeout": ""30""}
}
```

- `version` 是当前作用域最大版本号加一：`grayTag` 与 `rollbackOf` 为 `null`，`promotionOf` 为源版本号，`effective` 为 `true`。
- 每次晋升只创建一个版本；与发布、回滚并发写入时也不会产生重复或跳号。失败时不留下半成品版本，也不改变历史、灰度、回滚或生效状态。
- 所有版本相关读取结果（历史列表、历史快照、版本差异、单项历史中的 `version` 对象）都展示 `promotionOf`；旧 SQLite 数据升级后可直接读取，历史版本的 `promotionOf` 为 `null`。

| HTTP | code | 触发条件 |
|---|---|---|
| 400 | `INVALID_VERSION` | 路径 `version` 不是纯十进制正整数 |
| 404 | `VERSION_NOT_FOUND` | 该版本号在任何命名空间与环境中都不存在 |
| 409 | `VERSION_SCOPE_MISMATCH` | 版本存在，但属于其他命名空间或环境 |
| 409 | `NOT_GRAY_VERSION` | 源版本存在但没有 `grayTag`（非灰度版本） |

### `GET /effective-configs?namespace=...&environment=...`

返回当前生效（最新全量发布）的版本号、灰度标签与配置项。尚无全量发布时 `effectiveVersion` 与 `grayTag` 为 `null`、`items` 为空对象。命名空间或环境缺失时返回 HTTP 400 `MISSING_SCOPE`。

### `GET /historical-effective-configs?namespace=...&environment=...&asOf=...`

按时间点读取历史生效配置，纯只读：不创建版本，也不改变生效版本、灰度标签、回滚、晋升与历史顺序；现有生效配置、版本列表、版本快照与版本差异入口行为不变。`namespace` 与 `environment` 沿用现有作用域语义；`asOf` 接受 RFC3339 时间（含小数秒与时区偏移），服务统一换算成 UTC 并在响应中回传归一化后的值。

- 查询只考虑该作用域 `createdAt` 小于或等于 `asOf` 的版本，并返回其中最新的全量发布快照；全量发布是不带 `grayTag` 的版本，灰度版本在任何时间点都不能成为历史生效配置。
- 同一秒存在多个候选全量发布时取版本号最大者。
- 命中时 HTTP 200 响应包含 `namespace`、`environment`、归一化的 `asOf`、`effectiveVersion`、`version` 与 `items`：`effectiveVersion` 为该时间点的全量发布版本号；`version` 沿用现有版本元数据（版本号、`grayTag`、`rollbackOf`、`promotionOf`、`createdAt` 与相对当前的 `effective`），普通发布、回滚和晋升产生的全量版本都可被选中；`items` 返回完整配置项，值保持原始 JSON 语义，不转换数字、布尔、`null` 和字符串。
- `asOf` 早于该作用域第一个全量发布时仍返回 HTTP 200，`effectiveVersion` 与 `version` 为 `null`、`items` 为 `{}`；只有灰度历史而从未全量发布、或作用域本身没有版本时同样返回空结果。
- `asOf` 晚于或等于最新创建时间时返回最新全量发布。

| HTTP | code | 触发条件 |
|---|---|---|
| 400 | `MISSING_SCOPE` | `namespace` 或 `environment` 缺失或为空 |
| 400 | `MISSING_AS_OF` | 缺少 `asOf` 查询参数 |
| 400 | `INVALID_TIMESTAMP` | `asOf` 存在但不是合法 RFC3339 时间（含空字符串） |
| 503 | `storage_unavailable` | 存储不可用 |

### `GET /config-versions?namespace=...&environment=...`

按版本号升序返回该作用域的历史版本列表，每个版本包含版本号、灰度标签、回滚来源 `rollbackOf`、晋升来源 `promotionOf`、创建时间以及该版本是否为当前生效版本（`effective`）。

可选查询参数 `limit` 与 `afterVersion` 用于分页读取；两个参数都不传时保持上述完整列表响应，任一传入即启用分页。分页读取是纯只读操作：不创建版本，也不改变任何已存记录。

- `limit` 是 1 到 100 的十进制正整数，缺省为 100。
- `afterVersion` 是纯十进制非负整数，表示只读取该版本号之后的版本；第一页不传，后续页传上一页响应中的 `nextAfterVersion`。
- 顺序始终按版本号升序，`versions` 只包含连续一页；相同参数重复读取得到相同页面，发布新版本不影响已定位的旧页面。

分页响应示例：

```json
{
  "namespace": "payments",
  "environment": "prod",
  "versions": [
    {"namespace": "payments", "environment": "prod", "version": 3, "grayTag": null, "rollbackOf": null, "promotionOf": 2, "createdAt": "2026-10-01T10:10:00Z", "effective": true}
  ],
  "totalVersions": 5,
  "nextAfterVersion": 3,
  "hasMore": true
}
```

- `totalVersions` 是该作用域的全部版本数。
- `nextAfterVersion` 是本页最后一条的版本号；本页为空时等于请求中的 `afterVersion`；作用域完全无版本时为 0。
- `hasMore` 表示本页之后是否还有版本。
- `afterVersion` 大于已有最大版本号时返回 HTTP 200，`versions` 为空数组。

分页读取的固定错误结果：

| HTTP | code | 触发条件 |
|---|---|---|
| 400 | `MISSING_SCOPE` | `namespace` 或 `environment` 缺失或为空 |
| 400 | `INVALID_PAGE_SIZE` | `limit` 存在但不是 1 到 100 的十进制正整数 |
| 400 | `INVALID_CURSOR_VERSION` | `afterVersion` 存在但不是纯十进制非负整数 |
| 503 | `storage_unavailable` | 存储不可用 |

### `GET /namespaces/:namespace/environments/:environment/config-versions/:version`

读取任意历史版本保存的完整配置快照，纯只读，不新增版本，也不改变生效版本、灰度标签、回滚记录或历史顺序。命中时 HTTP 200：

```json
{
  "namespace": "payments",
  "environment": "prod",
  "version": {"namespace": "payments", "environment": "prod", "version": 2, "grayTag": "canary", "rollbackOf": null, "promotionOf": null, "createdAt": "2026-10-01T10:05:00Z", "effective": false},
  "items": {"retries": "5", "timeout": "\"30\""}
}
```

- `version` 对象沿用历史版本元数据语义，包含版本号、`grayTag`、`rollbackOf`、晋升来源 `promotionOf`、`createdAt` 和 `effective`。
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
  "baseVersion": {"namespace": "payments", "environment": "prod", "version": 1, "grayTag": null, "rollbackOf": null, "promotionOf": null, "createdAt": "2026-10-01T10:00:00Z", "effective": false},
  "targetVersion": {"namespace": "payments", "environment": "prod", "version": 2, "grayTag": "canary", "rollbackOf": null, "promotionOf": null, "createdAt": "2026-10-01T10:05:00Z", "effective": false},
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

### `GET /effective-config-diffs?namespace=...&baseEnvironment=...&targetEnvironment=...`

跨环境生效配置对比，查询同一命名空间下两个环境当前生效配置的差异。纯只读，不创建版本或审计记录，也不改变发布、灰度、晋升、回滚状态与历史查询结果。也支持等价的路径形式：

```text
GET /namespaces/:namespace/effective-config-diffs/:baseEnvironment/:targetEnvironment
```

HTTP 200 响应：

```json
{
  "namespace": "payments",
  "baseEnvironment": "staging",
  "targetEnvironment": "prod",
  "baseVersion": {"namespace": "payments", "environment": "staging", "version": 2, "grayTag": null, "rollbackOf": null, "promotionOf": null, "createdAt": "2026-10-01T10:05:00Z", "effective": true},
  "targetVersion": {"namespace": "payments", "environment": "prod", "version": 3, "grayTag": null, "rollbackOf": null, "promotionOf": 2, "createdAt": "2026-10-01T10:10:00Z", "effective": true},
  "changedCount": 2,
  "changes": [
    {"name": "retries", "changeType": "modified", "oldValue": "3", "newValue": "5"},
    {"name": "timeout", "changeType": "added", "newValue": "\"30\""}
  ]
}
```

差异语义：

- `baseVersion` 与 `targetVersion` 沿用历史版本元数据语义（版本号、`grayTag`、`rollbackOf`、`promotionOf`、`createdAt`、`effective`），对应环境没有全量发布时为 `null`。
- `changes` 按配置项名称字典序排列，`changeType` 固定为 `added`、`removed`、`modified`：`added` 只返回 `newValue`，`removed` 只返回 `oldValue`，`modified` 同时返回两者；`changedCount` 等于 `changes` 条数。
- 比较两边生效快照保存的原始 JSON：数字、布尔、`null`、字符串不转换，仅忽略空白与对象键顺序的表示差异，因此 `1` 与 `1.0`、`1` 与 `"1"` 仍视为不同。
- 两边都没有生效版本时返回 HTTP 200，两个版本字段均为 `null`，`changedCount` 为 0，`changes` 为空数组；仅一边没有生效版本时按空配置计算，另一边的全部配置项记为 `added` 或 `removed`。
- 该入口固定错误结果：

| HTTP | code | 触发条件 |
|---|---|---|
| 400 | `MISSING_SCOPE` | `namespace`、`baseEnvironment` 或 `targetEnvironment` 为空 |
| 400 | `SAME_ENVIRONMENT` | `baseEnvironment` 与 `targetEnvironment` 相同 |

### `GET /cross-environment-config-version-diffs?namespace=...&baseEnvironment=...&baseVersion=...&targetEnvironment=...&targetVersion=...`

跨环境历史版本对比，查询同一命名空间下两个不同环境各自的一个历史版本之间的配置项差异。版本号按环境独立递增，`baseVersion` 大于、等于或小于 `targetVersion` 都可以比较。纯只读，不创建版本、审计或差异记录，也不改变发布、灰度、晋升、回滚、历史读取与现有差异查询结果。结果表示从 base 侧到 target 侧的变化。HTTP 200 响应：

```json
{
  "namespace": "payments",
  "baseEnvironment": "staging",
  "targetEnvironment": "prod",
  "baseVersion": {"namespace": "payments", "environment": "staging", "version": 1, "grayTag": null, "rollbackOf": null, "promotionOf": null, "createdAt": "2026-10-01T10:00:00Z", "effective": true},
  "targetVersion": {"namespace": "payments", "environment": "prod", "version": 3, "grayTag": null, "rollbackOf": null, "promotionOf": 2, "createdAt": "2026-10-01T10:10:00Z", "effective": true},
  "changedCount": 2,
  "changes": [
    {"name": "retries", "changeType": "modified", "oldValue": "3", "newValue": "5"},
    {"name": "timeout", "changeType": "added", "newValue": "\"30\""}
  ]
}
```

差异语义：

- `baseVersion` 与 `targetVersion` 是对应侧历史版本的元数据对象（版本号、`grayTag`、`rollbackOf`、`promotionOf`、`createdAt`、`effective`），`effective` 表示该版本是否为其所在环境的当前生效版本；旧数据中 `promotionOf` 缺省仍为 `null`。
- `changes` 按配置项名称字典序排列，`changeType` 固定为 `added`、`removed`、`modified`：`added` 只返回 `newValue`，`removed` 只返回 `oldValue`，`modified` 同时返回两者；变化不携带 `affectsEffectiveConfig` 字段，`changedCount` 等于 `changes` 条数。
- 值保留已保存的原始 JSON：数字、布尔、`null`、字符串不转换，仅忽略空白与对象键顺序的表示差异，因此 `1` 与 `1.0`、`1` 与 `"1"` 仍视为不同。
- 两侧版本快照完全相同时返回 HTTP 200 且 `changedCount` 为 0、`changes` 为空数组。
- 参数按 `namespace`/环境、`baseVersion`、`targetVersion`、再到 base 侧版本、target 侧版本的固定顺序校验，固定错误结果：

| HTTP | code | 触发条件 |
|---|---|---|
| 400 | `MISSING_SCOPE` | `namespace`、`baseEnvironment` 或 `targetEnvironment` 为空 |
| 400 | `SAME_ENVIRONMENT` | `baseEnvironment` 与 `targetEnvironment` 相同 |
| 400 | `INVALID_VERSION` | `baseVersion` 或 `targetVersion` 不是纯十进制正整数（先校验 base 再校验 target） |
| 404 | `VERSION_NOT_FOUND` | 版本号在任何命名空间与环境中都不存在（先查 base 侧再查 target 侧） |
| 409 | `VERSION_SCOPE_MISMATCH` | 版本存在，但不属于该侧的命名空间与环境 |

### `GET /config-item-histories?namespace=...&environment=...&name=...`

单项配置历史查询，纯只读，不新增版本，也不改变生效版本、灰度标签、回滚记录或历史顺序。`name` 原样使用，不裁剪首尾空白。HTTP 200 响应：

```json
{
  "namespace": "payments",
  "environment": "prod",
  "name": "timeout",
  "effectiveVersion": 3,
  "effectiveItem": {"present": true, "value": "\"30\""},
  "changes": [
    {"version": {"namespace": "payments", "environment": "prod", "version": 1, "grayTag": null, "rollbackOf": null, "promotionOf": null, "createdAt": "2026-10-01T10:00:00Z", "effective": false}, "changeType": "added", "newValue": "\"30\""},
    {"version": {"namespace": "payments", "environment": "prod", "version": 2, "grayTag": "canary", "rollbackOf": null, "promotionOf": null, "createdAt": "2026-10-01T10:05:00Z", "effective": false}, "changeType": "removed", "oldValue": "\"30\""},
    {"version": {"namespace": "payments", "environment": "prod", "version": 3, "grayTag": null, "rollbackOf": 1, "promotionOf": null, "createdAt": "2026-10-01T10:10:00Z", "effective": true}, "changeType": "added", "newValue": "\"30\""}
  ],
  "totalChanges": 3
}
```

历史语义：

- 每个版本都与同作用域内紧邻的前一个已存版本比较该配置项，`changes` 按版本号升序；灰度发布与回滚版本同样参与比较。
- 首次出现记 `added`，随后消失记 `removed`，再次出现仍记 `added`，值变化记 `modified`，无变化的版本不列入。
- `added` 只返回 `newValue`，`removed` 只返回 `oldValue`，`modified` 同时返回两者；值保持已保存的原始 JSON，比较沿用原样 JSON 语义（数字、布尔、`null`、字符串不转换），仅做对象键排序和空白规范化。
- 每个 `version` 对象沿用历史版本元数据语义，包含版本号、`grayTag`、`rollbackOf`、晋升来源 `promotionOf`、`createdAt` 和 `effective`。
- `effectiveVersion` 是最新全量发布版本号，没有全量发布时为 `null`。
- `effectiveItem` 描述该配置项在当前生效快照中的状态：存在时为 `{"present": true, "value": <原样 JSON 值>}`，值为 JSON `null` 时 `value` 仍是 `null`；不存在时为 `{"present": false}` 且不返回 `value`。
- 名称在该作用域从未出现、或只在历史版本中出现但当前不在生效快照中，都返回 HTTP 200：前者 `changes` 为空数组、`totalChanges` 为 0、`effectiveItem.present` 为 `false`，后者正常列出变化。
- 该入口固定错误结果：

| HTTP | code | 触发条件 |
|---|---|---|
| 400 | `MISSING_SCOPE` | `namespace` 或 `environment` 为空 |
| 400 | `MISSING_ITEM_NAME` | `name` 缺失或为空字符串（纯空白名称视为非空） |

### `GET /effective-config-item-search?name=...&namespace=...&environment=...&present=...&value=...`

全局生效配置项检索：给定一个配置项名称，返回所有已有版本的命名空间与环境组合（包括只有灰度历史、从未全量发布的作用域）下该名称在当前生效快照中的状态。纯只读，不新增任何记录，也不改变发布、灰度、晋升、回滚及其他查询结果。

- `name` 必填，原样按完整名称精确匹配，不裁剪首尾空白，也不做子串匹配；纯空白名称是有效名称。
- `namespace`、`environment` 可选，提供非空值时精确筛选；缺省或空值不限制。
- `present` 可选，只接受字面量 `true` 或 `false`：`true` 仅返回生效快照中存在该名称的作用域，`false` 返回不存在该名称的作用域（含只有灰度历史者）；缺省时不按存在性筛选，名称从未出现的作用域仍返回 `present false`。
- `value` 可选，值本身是一段原始 JSON，按保存值语义精确匹配，仅忽略空白与对象键顺序：`1` 与 `1.0`、`1` 与 `"1"` 均视为不同；JSON `null` 只匹配保存值为 `null` 的生效项，不匹配不存在的作用域。`value` 匹配隐含要求生效项存在且值相同；`value` 与 `present=false` 不能同时出现。

结果按 `namespace`、`environment` 的 Unicode 码点升序排列。HTTP 200 响应：

```json
{
  "name": "timeout",
  "matchedCount": 2,
  "results": [
    {"namespace": "payments", "environment": "prod", "effectiveVersion": 3, "effectiveItem": {"present": true, "value": "\"30\""}},
    {"namespace": "payments", "environment": "staging", "effectiveVersion": null, "effectiveItem": {"present": false}}
  ]
}
```

- `matchedCount` 为过滤后的 `results` 条数；没有任何匹配时为 0、`results` 为空数组。
- 每项的 `effectiveVersion` 是该作用域最新全量发布版本号，没有全量发布时为 `null`。
- `effectiveItem` 语义同单项历史：存在时为 `{"present": true, "value": <原样 JSON 值>}`，值为 JSON `null` 时仍带 `"value": null`；不存在时为 `{"present": false}` 且不返回 `value`。
- 该入口固定错误结果（按 `name`、`present`、`value` 的顺序校验）：

| HTTP | code | 触发条件 |
|---|---|---|
| 400 | `MISSING_ITEM_NAME` | `name` 缺失或为空字符串（纯空白名称视为非空） |
| 400 | `INVALID_PRESENCE_FILTER` | `present` 存在且值不是 `true` 或 `false` |
| 400 | `INVALID_VALUE_FILTER` | `value` 不是有效 JSON，或与 `present=false` 同时出现 |
| 503 | `storage_unavailable` | 存储不可用 |

## 错误约定

所有错误响应都是单个顶层 `error` 对象，包含 `code` 与 `message` 两个字符串字段；`message` 不包含 SQL、堆栈或文件路径。版本差异查询的固定错误结果如下，不会被替换为空差异或静默忽略：

| HTTP | code | 触发条件 |
|---|---|---|
| 400 | `MISSING_SCOPE` | `namespace` 或 `environment` 为空 |
| 400 | `INVALID_VERSION` | `baseVersion` 或 `targetVersion` 不是正整数 |
| 409 | `VERSION_ORDER_CONFLICT` | `targetVersion` 小于 `baseVersion` |
| 404 | `VERSION_NOT_FOUND` | 任一版本在任何命名空间与环境中都不存在 |
| 409 | `VERSION_SCOPE_MISMATCH` | 版本存在，但属于其他命名空间或环境 |

历史快照读取（`GET .../config-versions/:version`）复用同一套版本查找顺序：版本号在任何命名空间与环境中都不存在时返回 404 `VERSION_NOT_FOUND`；版本号存在但属于其他命名空间或环境时返回 409 `VERSION_SCOPE_MISMATCH`；路径段不是纯十进制正整数时返回 400 `INVALID_VERSION`。
