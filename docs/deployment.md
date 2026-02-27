# 部署指南

## 二进制部署

### 1. 下载

从 [GitHub Releases](https://github.com/cprobe/catpaw/releases) 下载对应平台的压缩包并解压。

### 2. 配置

编辑 `conf.d/config.toml`，填入 FlashDuty 的 integration_key：

```toml
[flashduty]
url = "https://api.flashcat.cloud/event/push/alert/standard?integration_key=YOUR_KEY"
```

按需启用或调整 `conf.d/p.*` 下的插件配置。

### 3. 测试运行

```bash
./catpaw -test
```

确认输出无误后，正式启动。

### 4. systemd 服务（推荐）

创建 `/etc/systemd/system/catpaw.service`：

```ini
[Unit]
Description=catpaw event monitor
After=network.target

[Service]
Type=simple
ExecStart=/opt/catpaw/catpaw -configs /opt/catpaw/conf.d
WorkingDirectory=/opt/catpaw
Restart=always
RestartSec=5
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
```

启用并启动：

```bash
sudo systemctl daemon-reload
sudo systemctl enable catpaw
sudo systemctl start catpaw
```

查看日志：

```bash
sudo journalctl -u catpaw -f
```

### 5. 热加载

catpaw 支持通过 `SIGHUP` 信号热加载插件配置（新增/修改/删除插件目录），无需重启：

```bash
kill -HUP $(pidof catpaw)
```

## Docker 部署

```bash
docker run -d \
  --name catpaw \
  -v /path/to/your/conf.d:/app/conf.d \
  flashcatcloud/catpaw:latest
```

镜像内置了默认 `conf.d`，挂载自定义配置目录即可覆盖。

## 目录结构

```
/opt/catpaw/
├── catpaw                  # 二进制文件
└── conf.d/
    ├── config.toml         # 全局配置
    ├── p.disk/
    │   └── disk.toml       # 磁盘监控配置
    ├── p.procnum/
    │   └── procnum.toml    # 进程数监控配置
    ├── p.http/
    │   └── http.toml       # HTTP 监控配置
    └── ...
```

每个 `p.<plugin>/` 目录下可放多个 `.toml` 文件，内容会被合并加载。
