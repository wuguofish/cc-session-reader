---
name: cc-session
description: |
  用 cc-session CLI 讀取過去的 Claude Code session，取代直接讀 JSONL。
  CLI 在 context 外完成過濾，原始 300K 壓到 30-50K，只保留對話和 tool call 一行摘要。
  使用者想回顧、引用、分析過去的對話時使用。
allowed-tools:
  - Bash
  - Read
---

## 子命令

| 意圖 | 命令 |
|------|------|
| 找目標 session | `cc-session list` — 列出最近 session，`-p` 過濾專案 |
| 回顧特定段落 | `cc-session read <id>` — 對話全文 + tool call 一行摘要，`-offset` 跳讀 |
| 完整載入為 context | `cc-session inject <id>` — 每頁 ≤20K chars，自動分頁，重複呼叫推進下一頁 |
| 注入為 context（單次） | `cc-session context <id>` — 同 read 但更緊湊，帶 metadata header |
| 展開單一 tool call | `cc-session expand <id> <tool-id>` — tool-id 取自 read 輸出的 `[Tool#xxxx]` |
| 展開同類所有 tool call | `cc-session read <id> -verbose-bash` — 也有 `-verbose-agents` / `-verbose-thinking` |
| 檢查過濾遺漏 | `cc-session audit <id>` |
| 查看 CLI 使用紀錄 | `cc-session usage` |

Session ID 支援 prefix match，前 8 碼通常就夠。各子命令的 flags 用 `-h` 查看。

## read vs inject

使用者要查特定資訊（「上次那個 bug 怎麼修的」、「看一下那段討論」）→ `read`，用 `-offset` 跳到相關段落。
使用者要全局分析（「這個 session 有哪些流程可以改善」、「幫我回顧上次的開發過程」）→ `inject`，完整載入後分析。

## 分頁

大 session 的輸出可達數千行，一次全倒會超出 Bash stdout buffer 被寫入檔案，等同回到讀原始 JSONL。

**read**：預設 200 行截斷。用 `-offset 180 -max-lines 200` 接續（overlap 20 行銜接上下文）。
**inject**：自動追蹤進度，重複呼叫即翻頁，顯示 `[inject complete]` 後用 `-reset` 從頭開始。

## 本 Fork 的差異

此 fork 移除了原版的 `stats` / `benchmark` 子命令以及 `internal/tokens`（Anthropic count_tokens API 呼叫），
變成完全離線、零外連的 CLI。原版有的所有讀取／注入／審視功能保留不變。
