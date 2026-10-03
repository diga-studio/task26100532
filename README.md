# task26100532

# voxvault

VoxVault 音频入口的增量 Ogg Opus 拆包与采样时间轴库，供本地转写组件读取。
只处理封装、包结构和时间轴，不解码压缩音频。

## 用法

```go
d := voxvault.New(func(p voxvault.Packet) {
    // p.Data     原始音频包字节（逐字节保真）
    // p.Samples  解码样本数（48 kHz）
    // p.HeadTrim 头部应丢弃样本数（pre-skip）
    // p.TailTrim 尾部应丢弃样本数（末页 granule）
    // p.Start/p.End 保留部分在输出时间轴上的左闭右开区间
})
for chunk := range chunks {
    if err := d.Feed(chunk); err != nil { /* 坏页等 */ }
}
if err := d.Finish(); err != nil { /* 截断或缺失末页 */ }
```

- 输入为单个 Ogg 逻辑流（OpusHead 版本 1、声道 1/2、映射族 0）。
- `Feed` 可在任意字节处切分，也可一次包含多页；切分方式不影响输出。
- 页通过 CRC 与 granule 验证后才发布其完成的包；错误不回滚已发布的合法输出。
- 内部缓冲有界（一页 + 一个跨页包，远低于 2 MiB），可处理任意长度的流。

## 测试

```sh
go test ./...
```
