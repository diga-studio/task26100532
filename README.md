# task26100532

# voxvault

VoxVault 音频入口的增量 Ogg Opus 拆包与采样时间轴库，供本地转写组件读取。
只处理封装、包结构和时间轴，不解码压缩音频。

## 用法

```go
d := voxvault.New(func(p voxvault.Packet) {
    // p.Data      原始音频包字节（逐字节保真）
    // p.Samples   解码样本数（48 kHz）
    // p.TrimStart 头部应丢弃的样本数（pre-skip）
    // p.TrimEnd   尾部应丢弃的样本数（末页 granule）
    // p.Start/End 保留部分在零起点输出时间轴上的左闭右开区间
})
for chunk := range chunks { // 任意字节切分
    if err := d.Feed(chunk); err != nil { /* 流损坏，停止 */ }
}
if err := d.Finish(); err != nil { /* 截断或缺失末页 */ }
```

## 行为要点

- 单个 Ogg 逻辑流；OpusHead 版本 1、声道 1/2、映射族 0；OpusHead 与
  OpusTags 各独占一页。
- 校验页 CRC、序号连续性（32 位回绕）、流标识与包延续标志；坏页直接报错，
  不做 "OggS" 重同步。
- 音频包可跨页、最大 128 KiB；覆盖 Opus 四种帧计数编码，校验帧长结构与
  最多 120 ms 包时长；所有时长以 48 kHz 整数样本计算。
- 零起点时间轴：先按 pre-skip 裁头，再按末页 granule 裁尾；完全被裁掉的
  包不输出；保留区间无重叠、无缺口地覆盖 `[0, 末页granule - preSkip)`。
- 页通过 CRC 与 granule 验证后才发布该页完成的包，且在当次 `Feed` 返回前
  发布；后续错误不回滚已发布的合法输出。
- 内部只保留当前页（≤ 65307 字节）与当前未完成的包（≤ 128 KiB），
  处理任意大小的流都不需要更多内存。

## 测试

```sh
go test ./...
```
