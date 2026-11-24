# 无持久盘部署：R2 / B2 状态快照同步

Orkestr 免费层的容器文件系统是临时的，重启即丢。本目录用一个入口脚本把 `/app`
（`config.json` + `auths/` + `data/`）整体打包成**单个** `state.tar.gz` 存到对象存储：

- **启动前**：拉取快照并解包，失败则拒绝启动（防止空目录覆盖线上备份）；
- **运行期**：每 `STATE_SYNC_INTERVAL` 秒（默认 60）推送一次，另写一份 `daily/YYYY-MM-DD.tar.gz`；
- **退出前**：收到 SIGTERM 后先停应用，再推最后一次。

不改一行 Go 代码，上游仓库同步时只需保持 `ops/` 与 `Dockerfile` 这两处。

```
s3/r2 桶
└── autoclaw2api/
    ├── state.tar.gz            ← 唯一真相，每次覆盖
    └── daily/2026-11-25.tar.gz ← 按天归档，建议生命周期保留 14~30 天
```

## 1. 对象存储准备

建一个**私有** bucket（例如 `autoclaw2api`），不要开公共访问。快照里有 accessToken、
面板密码哈希和 API Key，等同于全套账号凭证。

## 2. 环境变量（Orkestr → 项目 → 环境变量）

### Cloudflare R2（推荐）

免费额度：10 GB 存储、100 万次 Class A/月、1000 万次 Class B/月、出站流量免费。
60 秒间隔约 8.6 万次写入/月，远低于额度。

| 变量 | 值 |
| --- | --- |
| `STATE_REMOTE` | `r2:autoclaw2api/autoclaw2api`（`r2:桶名/前缀`，**不要**带结尾斜杠） |
| `STATE_SYNC_INTERVAL` | `60` |
| `RCLONE_CONFIG_R2_TYPE` | `s3` |
| `RCLONE_CONFIG_R2_PROVIDER` | `Cloudflare` |
| `RCLONE_CONFIG_R2_ACCESS_KEY_ID` | R2 API 令牌生成的 Access Key ID |
| `RCLONE_CONFIG_R2_SECRET_ACCESS_KEY` | 对应 Secret |
| `RCLONE_CONFIG_R2_ENDPOINT` | `https://<ACCOUNT_ID>.r2.cloudflarestorage.com` |
| `RCLONE_CONFIG_R2_REGION` | `auto` |
| `RCLONE_CONFIG_R2_ACL` | `private` |

### Backblaze B2（原生 API）

| 变量 | 值 |
| --- | --- |
| `STATE_REMOTE` | `b2:autoclaw2api/autoclaw2api` |
| `STATE_SYNC_INTERVAL` | `300` |
| `RCLONE_CONFIG_B2_TYPE` | `b2` |
| `RCLONE_CONFIG_B2_ACCOUNT` | Application Key 的 keyID |
| `RCLONE_CONFIG_B2_KEY` | Application Key |

> B2 的 Class A 事务按 $4/百万 计费，60 秒间隔约 8.6 万次/月（≈ $0.35）；用 300 秒可降到
> 约 8,600 次。R2 免费额度更宽裕，**建议主用 R2**，需要异地容灾再把 `daily/` 同步到 B2。

## 3. 首次部署（关键：只做一次）

1. 先设 `ALLOW_EMPTY=1`，其余变量照上表填好，部署。
2. 打开面板完成初始化、添加账号 / API Key / 代理，等至少一个推送周期（60 秒）或看日志出现
   `已推送快照`。
3. **删掉 `ALLOW_EMPTY`**，重新部署一次。之后所有重启都走「先拉取、拉不到就不启动」。

> 千万别把 `ALLOW_EMPTY=1` 留在生产环境：拉取一旦失败，应用会以空状态启动，60 秒后把空
> 快照推上去，线上备份就被覆盖了。

## 4. 排障与回滚

```bash
# 看快照内容
rclone cat r2:autoclaw2api/autoclaw2api/state.tar.gz | tar -tzf -

# 恢复到某一天（停容器后，在本地或临时容器里执行）
rclone copyto r2:autoclaw2api/autoclaw2api/daily/2026-11-25.tar.gz ./state.tar.gz
tar -C /app -xzf ./state.tar.gz
```

容器日志里 `[state-sync]` 前缀的行会显示拉取 / 推送结果。

## 5. Orkestr 免费层的几个硬约束

- **空闲会休眠**。容器睡着时，08:30 自动签到、30 分钟模型同步、15 分钟 token 续期都不会跑；
  醒来时会先拉快照再启动，冷启动多几秒。建议用免费的 1 个 Cron 每天定时请求
  `https://<你的域名>/health`（该接口无需鉴权），既保活也避免 token 长时间不续期。
- **端口必须跟平台走**。入口脚本检测到 `PORT` 会自动追加 `-listen 0.0.0.0:$PORT`，不用改配置。
- **单实例**。快照是「整目录覆盖」，没有分布式锁，不能多副本同时跑。
- **流量**。每次推送约几十 KB，60 秒间隔一个月约 4 GB（300 秒则约 1 GB），远低于 100 GB/月。
- **构建**。`apk add rclone` 让镜像大约多 20 MB，构建时间只多几秒（免费层 300 分钟/月）。
