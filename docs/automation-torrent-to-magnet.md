# 本地种子转磁力节点

`torrent_to_magnet` 在 FilmFusion 内部解析种子，不上传到 115，不访问 Tracker，也不下载媒体文件。运行时只读取配置的种子地址；如果输入已经是磁链，则不发起网络请求。

推荐流程：

```text
RSS → 种子转磁力 → 115 添加链接任务 → 等待 115 下载完成 → 115 API 重命名
```

种子节点输入默认是 `$item.download_url`。115 添加链接任务的 `url` 引用 `$nodes.convert.output.magnet`（`convert` 替换成实际节点 ID）。重命名的 `file_id` 引用等待节点输出；种子中的 Hash 不等于 115 文件 ID。也可以将转换结果交给 qBittorrent。

## 输入与输出

- `url`：HTTP/HTTPS 种子下载地址，或已有的 v1 磁力链接，支持变量。
- `headers`：可选请求头对象，值支持变量，用于 Cookie、Referer 等。不会自动继承 RSS 源的身份信息。
- `include_trackers`：默认 `true`，将种子里的 Tracker 写入磁链；Tracker 可能含站点 passkey，开启后会传给下游下载器。关闭时只从生成的磁链中去掉 Tracker，输出 `trackers` 仍保留原始列表。
- `follow_redirects`：默认 `true`，只跟随同源跳转，防止请求头被带到其他站点。
- `allow_private_network`：默认 `false`；内网 RSS/种子服务可显式开启。沿用 HTTP 节点的 DNS 与地址校验。
- `timeout_seconds`：默认 30 秒。

主要输出：`magnet`、`info_hash`、`torrent_name`、`file_size`、`file_count`、`files`（`index/path/size`）、`private`、`trackers`、`trackers_included`、`torrent_version`、`metadata_available`、`warnings`。

已有磁链直接透传时会提取十六进制或 Base32 BTIH，`info_hash` 统一为 40 位小写十六进制。此时 `metadata_available=false`，不虚构 `private`、文件数量、大小和文件列表。

## 支持范围

支持 v1 种子以及含完整 v1 字段的混合种子，纯 v2 会明确报错。按原始 `info` 字节切片计算 SHA1，不经过反序列化后重新编码；保持种子原本的 Hash，即使其字典键未排序。拒绝重复字典键、截断内容、非规范整数、大小溢出、分片数量不一致，以及超出 8 MiB / 64 层 / 100000 个值 / 10000 个文件的输入。

私有种子会返回 `private=true` 和提示，但不会阻止转换。磁链转换成功不保证 115 或其他下载器能获得元数据或下载成功，也不会绕过私有 Tracker 的访问限制。磁链提交没有官方 BT 接口的 `wanted` 选文件能力。

编辑器预览只展示带 `preview=true` 的模拟数据，不读取远程种子、不请求 115；下载地址的真实 Hash、文件和私有标记在运行时产生。

协议依据：[BEP 3](https://www.bittorrent.org/beps/bep_0003.html)、[BEP 9](https://www.bittorrent.org/beps/bep_0009.html)、[BEP 52](https://www.bittorrent.org/beps/bep_0052.html)。实现验证使用本地合成种子、本机 HTTP 测试服务和内存中的 115 响应，没有使用真实 115 账号或执行云下载。
