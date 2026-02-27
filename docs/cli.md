# 命令行参数

```
./catpaw [options]
```

| 参数 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `-configs` | string | `conf.d` | 配置目录路径 |
| `-test` | bool | `false` | 测试模式，事件输出到 stdout 而不发送到 FlashDuty |
| `-interval` | int | `0` | 全局采集间隔（秒），覆盖配置文件中的 `global.interval` |
| `-plugins` | string | `""` | 只运行指定插件，多个用 `:` 分隔，如 `disk:procnum` |
| `-url` | string | `""` | FlashDuty 推送地址，覆盖配置文件中的 `flashduty.url` |
| `-loglevel` | string | `""` | 日志级别，覆盖配置文件，可选 `debug` `info` `warn` `error` `fatal` |
| `-version` | bool | `false` | 显示版本号 |

## 常用场景

### 测试模式

不发送到 FlashDuty，直接在终端查看事件输出：

```bash
./catpaw -test
```

### 只运行指定插件

```bash
./catpaw -test -plugins disk:procnum
```

### 指定配置目录

```bash
./catpaw -configs /etc/catpaw/conf.d
```

### Windows 服务管理

```bash
catpaw.exe -win-service-install
catpaw.exe -win-service-start
catpaw.exe -win-service-stop
catpaw.exe -win-service-uninstall
```
