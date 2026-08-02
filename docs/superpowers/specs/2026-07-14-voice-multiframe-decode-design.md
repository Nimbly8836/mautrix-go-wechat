# Complete WeChat SILK Internal-Frame Decode

> **Partial supersession / 部分内容已被替代（2026-08-02）：** Statements about `/Tools/DownloadVoice` offset chunking, Base64 fragment assembly, and chunk-specific tests are superseded by the linked [2026-08-02 actual API design](2026-08-02-voice-download-actual-api-design.md) and [implementation plan](../plans/2026-08-02-voice-download-actual-api.md), because the old `/Tools/DownloadVoice` offset chunking contract does not match either this repository's `swagger.json` or the user's actual TypeScript client. The SILK packet parsing and multi-internal-frame decoder design remains authoritative. / 本文中关于 `/Tools/DownloadVoice` 偏移分片、Base64 分片拼接和分片专用测试的内容已被链接的 2026-08-02 实际接口设计和实施计划替代，因为旧的 `/Tools/DownloadVoice` 偏移分片契约既不符合本仓库的 `swagger.json`，也不符合用户实际使用的 TypeScript 客户端；SILK 数据包解析与多内部帧解码器设计仍然有效并保持权威。

## 中文审查摘要

**目标：** 修复微信入站语音解码后时长恰好减半。

**证据：** 上游下载 `declared=3200, actual=3200`，仅裁掉 7 字节；微信原始约 3.4 秒，Matrix WAV 为 82,604 字节，即 1.72 秒。1 秒/40ms SILK 测试也稳定复现旧 decoder 只输出 500ms。

**方案：** 保留现有下载、分片、拼接、结构检查和截断恢复；只替换 SILK PCM 解码调用，按官方参考在同一个外层 packet 上循环 `moreInternalDecoderFrames`，取完全部 20ms 内部帧。

**影响范围：** `internal/wechat/voice.go`、新增 decoder adapter/test、`go.mod/go.sum`。不改 FFmpeg、出站、Docker、Matrix 或 DownloadVoice。

**风险：** 新增固定版本的纯 Go SILK SDK/modernc 依赖，会增加编译量；功能边界由 1 秒 fixture 和现有恢复测试约束。

## Problem Statement

`github.com/youthlin/silk v0.0.4` calls `SKP_Silk_SDK_Decode` once for each outer SILK packet and ignores `moreInternalDecoderFrames`. WeChat packets may contain two sequential 20ms internal frames. The dependency emits only the first frame, causing a 40ms packet to become 20ms PCM and total duration to become exactly half.

The upstream module has no newer release and its master retains the same behavior.

## Goals

1. Decode all internal frames from every validated outer packet.
2. Preserve the existing DownloadVoice, Base64 assembly, structure validation, retry, and truncation recovery.
3. Keep 20ms packet streams unchanged.
4. Produce non-empty 24kHz mono PCM/WAV with the correct duration.
5. Add deterministic RED/GREEN duration coverage.

## Non-Goals

- Changing voice acquisition parameters or chunk assembly.
- Adding FFmpeg or outbound audio support.
- Stretching, duplicating, or synthesizing missing audio.
- Changing Matrix duration metadata in this change.

## Selected Design

Use the exported SDK from pinned `github.com/wdvxdr1123/go-silk` with `modernc.org/libc`. Refactor the existing SILK scanner into a shared packet parser so validation, recovery, and decoder use identical packet boundaries.

For each positive validated packet payload:

1. Maintain one decoder state for the full stream.
2. Reset `nSamplesOut` to 480 samples before each call.
3. Call `SDK_Decode` with the same packet payload.
4. Append returned little-endian PCM samples.
5. Repeat while `FmoreInternalDecoderFrames != 0`.
6. Reject SDK errors, sample counts outside `1..480`, or a status loop exceeding five calls.

Resource rules:

- create and close `libc.TLS`;
- validate decoder size and allocation;
- free decoder state before closing TLS;
- keep payload/output/control objects alive across unsafe calls.

`silkToWAV` continues to validate structure first, calls the new adapter, rejects empty PCM, then wraps PCM using the existing WAV writer.

## Testing Strategy

1. Encode one second of 24kHz PCM using `PacketSizeMs(40)`.
2. Remove only the encoder's zero-length buffering records in test code.
3. Assert the fixture contains 25 positive outer packets and passes structure validation.
4. RED: current decoder returns approximately 500ms.
5. GREEN: adapter returns `980..1020ms`.
6. Run all existing 20ms voice, invalid-structure, retry, and truncated-recovery tests.
7. Run focused race tests with the SDK package checkptr exception required by the translated uintptr ABI, plus full non-race tests and vet.

## Acceptance Criteria

- Production-like 40ms packets decode to full duration.
- No DownloadVoice, FFmpeg, outbound, Docker, or Matrix files change.
- Existing acquisition and truncation tests remain green.
- Full tests and vet pass.

## Technical Vocabulary / 技术词汇

- **internal frame** — 内部帧
- **outer packet** — 外层包
- **decoder state** — 解码器状态
- **sample count** — 样本数
- **unsafe lifetime** — unsafe 生命周期
- **translated SDK** — 转译 SDK

## 中文审查清单

- [ ] 是否确认数据获取已完整，剩余问题是内部帧遗漏？
- [ ] 是否只替换解码调用？
- [ ] 是否解码真实第二帧，而非拉伸音频？
- [ ] 是否保留现有截断恢复？
- [ ] 是否不包含 FFmpeg、出站和 Docker？
