## 简介
这是一个极为轻量的事件监控系统，用于探测一些异常事件并生成告警。通常和 Flashduty 协同使用（当然，你也可以写个接收事件的小服务来做告警分发），catpaw 负责产生事件，Flashduty 负责发送事件。

## 插件简介
catpaw 是插件机制，提供了不同功能的插件用于不同的监控场景。

### exec
自定义脚本执行的插件。脚本用什么语言写都可以，只要按照规定的格式输出即可。

### filechange
监控近期是否有文件发生变化，比如 `/etc/shadow` 等重要文件。

### http
监控 HTTP URL，检查返回的状态码和内容是否符合预期。

### journaltail
使用 journalctl 命令检查日志，如果日志里有关键字就产生事件。

### mtime
递归检查某个目录下的所有文件的 mtime，如果有文件在近期发生变化就产生事件。

### net
通过 tcp、udp 方式探测远端端口是否可用。

### ping
通过 icmp 方式探测远端主机是否可用。

### procnum
检查某个进程的数量，如果数量不够（通常是进程挂了）就产生事件。

### sfilter
执行脚本，检查输出，只要输出中包含关键字就产生事件。

## 使用场景

- 不想引入大型监控系统，不想有太多依赖，就想对一些重要的事情做一些简单的监控。
- 监控系统的自监控。为了避免循环依赖，对监控系统做监控，通常需要另一个系统，catpaw 轻量，合适。
- 对一些日志、字符串、事件文本做监控，直接读取匹配了关键字就告警。

## 安装

从 [github releases](https://github.com/cprobe/catpaw/releases) 页面下载编译好的二进制。

## 使用

首先你需要注册一个 Flashduty 账号。

- [Flashduty产品介绍](https://flashcat.cloud/product/flashduty/)
- [Flashduty免费注册](https://console.flashcat.cloud/)

然后在集成中心创建一个“标准告警事件”的集成，随便起个名字，保存，就可以得到一个 webhook 地址。如果搞不定，Flashduty 页面右上角有较为详细的文档和视频教程。

把 webhook 地址配置到 catpaw 的配置文件中：`conf.d/config.toml`，配置到 flashduty 下面的 url 字段。然后，就可以启动 catpaw 玩耍了。catpaw 有几个命令行参数，通过 `./catpaw --help` 可以看到。

当然了，具体要监控什么，需要去修改各个插件的配置，每个插件的配置文件在 `conf.d` 目录下，比如 `conf.d/p.http` 就是 http 插件的配置文件。里边有详尽的注释。

## 交流

可以加我微信：`picobyte` 进群交流。备注 `catpaw`。

