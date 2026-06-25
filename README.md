# cc-session-reader (offline fork)

> Fork of [Mapleeeeeeeeeee/cc-session-reader](https://github.com/Mapleeeeeeeeeee/cc-session-reader)，
> 移除 `stats` / `benchmark` 子命令與 `internal/tokens` 套件，變成完全離線、零外連的 CLI。
> 原版所有 session 讀取／注入／審視功能保留不變。

讀取 Claude Code session 記錄，產出精簡摘要的 CLI 工具。
每個 tool call 壓成一行摘要（tool name + 關鍵參數 + result 狀態），對話文字完整保留。
純靜態提取，不使用 LLM、不打任何 HTTP API。

token reduction 視 session 組成而定：tool I/O 為主的 session 典型可達 **80–88%**；
以大型 plan 文件或純對話為主的 session 較低（實測約 40–65%），因為使用者／assistant 文字會完整保留、不壓縮。

## 安裝

### 從 source build（需要 Go 1.22+）

```bash
go install github.com/Mapleeeeeeeeeee/cc-session-reader/cmd/cc-session@latest
```

binary 會放在 `$GOPATH/bin`（Windows 預設是 `%USERPROFILE%\go\bin`），把該目錄加進 PATH 即可使用。

> 若你想跑此 fork 而非原作：clone 此 repo 後在根目錄跑 `go install ./cmd/cc-session`。

### 安裝 Skill（讓 Claude Code 自動知道怎麼用）

```bash
mkdir -p ~/.claude/skills/cc-session
curl -o ~/.claude/skills/cc-session/SKILL.md \
  https://raw.githubusercontent.com/wuguofish/cc-session-reader/main/SKILL.md
```

Windows PowerShell：

```powershell
New-Item -ItemType Directory -Force "$HOME\.claude\skills\cc-session" | Out-Null
Invoke-WebRequest -Uri "https://raw.githubusercontent.com/wuguofish/cc-session-reader/main/SKILL.md" `
  -OutFile "$HOME\.claude\skills\cc-session\SKILL.md"
```

### 關閉 CLI usage log（可選）

CLI 會把自己的使用紀錄（command / target / cwd / caller session）寫到 `~/.claude/skills/cc-session/usage.jsonl`，純本機、不外送。要完全無痕：

```bash
export CC_SESSION_NO_USAGE=1
```

Windows：

```powershell
[Environment]::SetEnvironmentVariable('CC_SESSION_NO_USAGE', '1', 'User')
```

## 子命令

| 命令 | 說明 | 範例 |
|------|------|------|
| `list` | 瀏覽最近的 session | `cc-session list -n 10 -p myproject` |
| `read` | 完整對話 + inline tool 摘要 | `cc-session read <id> -max-lines 200` |
| `context` | 精簡注入格式，含 session metadata header | `cc-session context <id>` |
| `inject` | 分頁 context 注入（每頁 ≤20K chars，自動追蹤進度，`-reset` 重來） | `cc-session inject <id>` |
| `audit` | 取樣被過濾的內容，確認沒漏掉重要資訊 | `cc-session audit <id> -n 10` |
| `expand` | 展開特定 tool call 的完整 input/result | `cc-session expand <id> uCVa` |
| `usage` | 查看此 CLI 的使用紀錄 | `cc-session usage -cmd read` |

Session ID 支援 prefix match，通常前 8 碼就夠。`read` 和 `context` 預設截斷 200 行（`-max-lines` 可調），截斷時印出總行數和建議的下一段 offset。

`list` 來源是 session metadata（`~/.claude/usage-data/session-meta/`），數量少於磁碟全部 transcript；`read`／`context` 可直接存取任何 transcript，不限於 `list` 列出的。

### Verbose flags（適用 read / context）

| Flag | 效果 |
|------|------|
| `-verbose-agents` | 完整保留 Agent subagent 回傳結果（預設只留一行摘要） |
| `-verbose-bash` | 完整顯示 Bash 工具的 stdout/stderr（預設摘要） |
| `-verbose-thinking` | 顯示 assistant 的 thinking 區塊（預設隱藏） |
| `-verbose-commands` | 展開 slash／bash 指令完整輸出（預設只留 marker） |

## 壓縮邏輯

工具呼叫、Bash 輸出、Agent 結果、thinking 都預設壓成摘要或一行 marker；User／Assistant 對話文字完整保留。Skill injection、teammate warning、command injection、Context Usage 區塊、system-reminder 等 injection 類型會額外壓縮或整段移除，減少 context 噪音。詳細過濾規則見 [SKILL.md](SKILL.md)。

## 架構

```
cmd/cc-session/       CLI 入口，子命令路由
internal/
  claudecodec/        JSONL 讀取、noise 過濾、raw→event 解析（TranscriptReader / HeaderScanner 介面）
  session/            Domain model（Event, ToolUse, ToolResult, ToolInput）
  parser/             Session 搜尋（找 transcript、解析 ID、metadata）
  summarizer/         Tool call → 一行摘要
  formatter/          輸出格式化（read mode、context mode）
  analyzer/           Audit 取樣
  inject/             分頁注入狀態管理
  tracker/            CLI usage 追蹤（本機 JSONL）
  config/             config.json 與 env var 讀取
  jsonutil/           JSON map 工具函數
  skillpath/          Skill 目錄路徑解析
```

`claudecodec` 是唯一與 JSONL 格式耦合的套件；其餘套件透過 `TranscriptReader` 和 `HeaderScanner` 介面存取 session 資料。

## 移除

```bash
rm $(go env GOPATH)/bin/cc-session
rm -rf ~/.claude/skills/cc-session
```

## Fork 與原作的差異

| 項目 | 原作 | 本 Fork |
|------|------|---------|
| `stats` 子命令 | ✅ 含 Anthropic count_tokens 整合 | ❌ 移除 |
| `benchmark` 子命令 | ✅ 含 cost 計算 | ❌ 移除 |
| `internal/tokens` | Anthropic API 呼叫 | ❌ 移除 |
| `install.sh` / `install.ps1` | prebuilt binary 安裝腳本 | ❌ 移除（僅支援 `go install`） |
| 外部 HTTP 呼叫 | count_tokens API | 零外連 |
| 其他功能（list / read / context / inject / audit / expand / usage） | ✅ | ✅ 保留 |

合併原版未來更新時，需注意上述檔案不要還原。
