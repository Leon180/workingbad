# Project Instructions (working name: workingbad)

> 全域 coding style / security / token-efficiency 已由 `~/.claude/CLAUDE.md` 載入，這裡只放本專案專屬內容。

## 產品定位
**本地 + LLM 的語意 truth source**：truth source 是**產品本體**（工程師的本地工作記憶），同步是 killer feature。
- **產品本體**：truth source 維護每個 `goal` 的一致狀態；各連接產品只 fetch/push 自己關心的切片（hub-and-spoke，非 N×N）。
- **受益者 = 工程師本人**：免除「不斷手動更新各種 ticket 狀態讓 PM/隊友了解進度」的 toil。團隊的 visibility 是副產品 → **裝的人就是受益者**，避開「向上管理工具」陷阱。
- **護城河 = 三者交集**：LLM 跨 domain 互譯（git → 協作者讀得懂的語言，非搬欄位）＋ 本地（資料主權）＋ 語意圖。缺一即淪為本地版 Zapier。
- **設計鐵律**：純本地、未接任何同步時，就必須對工程師有價值。Phase 1 純本地 mock 即須讓人想用（dogfooding = 價值驗證）。
- 反面參照：Unito/Zapier/n8n（dumb 同步、非本地非語意）、Unblocked（cloud/閉源）、Gitmore（管理層導向）。

## 技術約束（不可妥協）
- **單一 Go binary**，輕量、可跨平台編譯。榜樣：Gitea。
- 儲存：`modernc.org/sqlite`(純 Go、免 cgo,FTS5 內建);migration 走 `goose v3 + embed.FS`、forward-only、startup 自動、**`schema-frozen` marker tag 後純 additive**(marker 前——含 Slice D node 手術——可改既有 migration);版號由 release-please 依 conventional commit 自動 bump,與 schema-freeze 脫鉤。
- Web UI：`net/http` + `embed.FS` 打包進 binary，localhost；**不引重前端框架**。
- 設定：單一 `config.yaml`，「下載 → 設好 config → 直接跑」。
- AI 為**必要能力**，setup 擇一 local(Ollama,隱私)/api(Claude,送雲)。**型別由 LLM 依內容判斷**、activity 合成需模型(無 fallback，建議 lazy)；branch 僅分組(免費)。

## 架構
```
Sources → TruthSource(core + SQLite) → Sinks
              └─ AIProvider (Classify/Summarize/Relate)  ◀ local(Ollama) | api(Claude)
         背景服務 + localhost 輕量 Web UI / CLI
```
三個關節 interface：
- `Source`  — `Pull(checkpoint)/Watch()`，外部事件正規化成 Entry（git=fetch-only）
- `Sink`    — `Sync(entries)` + direction policy（fetch∩push=∅，消滅迴圈）
- `AIProvider` — `Classify()`(LLM 判型)/`Summarize()`(activity 核心)/`Relate()`(embedding)；branch 僅分組
- **`RepositoryService` = 唯一寫入閘門**(架構不變量);CLI / HTTP / sync 皆 thin adapter,不准繞過。

## Truth Source 資料模型（一等公民）
- `Entry.type`：**activity | research | discuss | decision | goal**（activity≠git commit：是模型合成的工作階段人類語言記錄，1:N git commit，依 branch 分組）
- `Edge`：relates_to | derived_from | blocks | part_of | iteration_of —— `goal` 可遞迴聚合域(iteration_of 疊迭代史)
- `origin`：fetched(唯讀) | pushed | local。隔離鍵 `repo_id` + 跨編輯穩定身分 `logical_id`(uuid v7);全系統 uuid v7 PK(raw 例外)。詳細契約見 skill `truth-source-schema`、`connector-interface`。

## MVP 範圍（Phase 1：interface + mock + 完整測試，不串外部 API）
1. 定好 Source/Sink/AIProvider interface（含 checkpoint cursor、origin/direction、Summarize 為核心）
2. in-memory mock Source/Sink/AIProvider，mock git 紀錄跑完整 pipeline
3. SQLite repo（entries / edges / sync_state / source_checkpoint）+ 完整測試
4. 最簡 Web UI：唯讀列表 + 手動建 research/decision/goal（graph 降級、延後）
5. 延後：真實 connector(Slack/ClickUp/GitHub)、背景服務、雙向同步（用 disjoint sets 解，非 CRDT）

> 已知要串接的外部來源/目標：git、GitHub、Slack、**ClickUp**、Claude。ClickUp 同時可當
> Source（拉任務狀態）與 Sink（推 goal/進度）；其 task 天然對應 `goal` 型 Entry。

## 開發紀律
- **寫/改 code 時守 `karpathy-guidelines`**（全域 skill）：最小手術式修改、不過度設計、把假設講出來、先定義可驗證的成功標準。
- 設計/Phase 邊界、或卡在決策時用 `grill-me`(互動);多面向且要留持久記錄與來源時改用 **`/grill-design`** workflow(多 agent 自動化,產 `docs/grill/` 記錄)。
- TDD：skill `tdd-workflow` + `golang-testing`，新功能 80%+ 覆蓋。
- 寫 code 前先 `search-first`（找現成 lib / pattern，別重造輪子）。
- AI 相關設計查 `cost-aware-llm-pipeline`、`regex-vs-llm-structured-text`、`agentic-engineering`、`eval-harness`。
- 完成宣稱前跑 `verification-loop`。
- 迭代機制見 [docs/ROADMAP.md](../docs/ROADMAP.md)：階段邊界用 `project-review-checkpoint` 審查。
- 開發流程 / harness（自動 PR loop、CI gate、big-version full review + streamline）見 [docs/DEVELOPMENT.md](../docs/DEVELOPMENT.md)。
- **Context 紀律：任何超過 5 行的細節 → 寫進 skill，不寫進本檔。** CLAUDE.md 只當索引，
  目標 < 100 行（有 PostToolUse hook 守門）。維護用 `skill-stocktake` / `strategic-compact` / `/harness-audit`。

## Agents（按需 spawn，不佔常駐 context）
- `architect` — 系統設計 / 架構決策（PROACTIVE，現在設計期常用）
- `go-reviewer` — Go code review（所有 Go 變更必用）
- `go-build-resolver` — build / vet / lint 錯誤修復
- `silent-failure-hunter` — 揪吞錯、靜默失敗、壞 fallback
- `harness-optimizer` — 調 harness 的可靠度 / 成本 / 吞吐
