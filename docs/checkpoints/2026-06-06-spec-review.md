# Spec Review Checkpoint — 2026-06-06

6 個專業 agent 對規格(CLAUDE.md / ROADMAP.md / truth-source-schema / connector-interface)做的多維度盤點彙整。
維度:架構、資料模型、安全/隱私、AI 策略/成本、產品/採用、可行性/交付。

> 規則:多個 agent 同時指出 = 最高優先。下面以「收斂程度」排序。

## P0 — 多維度收斂的根本問題(動工前必須處理)

### 1. 「資料留本機」vs「自動外送 Slack/ClickUp/Claude」的根本張力
**誰指出**:安全(高)、產品(高,價值歸屬錯位)。
- 賣點是隱私本地化,核心功能卻是把 commit/decision/research **自動外送**到團隊雲端與 Claude 雲端 → 命題自相矛盾。
- 受益者(PM/團隊)≠ 安裝者(工程師)→ 可能是「向上管理工具」披 engineer-first 皮。
- **動作**:(a) 重新確認定位(見決策清單);(b) 把「外送」定義為需邊界控制的安全操作:預設白名單、外送前 secret/PII redaction、dry-run/預覽、分支白名單、「已 push 才同步」。

### 2. 雙向同步 = 全專案最高風險,且被一行帶過
**誰指出**:架構(高,sync loop/authority)、可行性(高,「v2 願景非 v1」)、產品(中)、資料(中)。
- ClickUp 同時 Source+Sink → echo/迴圈;誰是欄位 authority、衝突解決全未定義。
- **動作**:明確把雙向同步移出 v1,標「探索性」。v1 只做單向 push。但 Phase 2 設計 Sink 時就要預留 `origin / last_synced_hash` 接縫,否則 Phase 4 重寫。

### 3. 同步狀態模型缺席(sync_state + checkpoint 表不在 schema)
**誰指出**:架構(高)、資料(高)。
- Sink 冪等需要持久化 `(sink, entry_id) -> external_ref, last_synced_hash`;Source 增量需要 per-source cursor checkpoint。兩張表都不在 schema、不在 migration。
- `Source.Pull(since time.Time)` 對非時間游標來源(git range / API cursor / slack ts)是錯抽象 → 改不透明 checkpoint token。
- **動作**:把 `sync_state`、`source_checkpoint` 立為核心表,由同步引擎統一管理。

### 4. Phase 1 太大,缺更薄的價值自證切片
**誰指出**:可行性(Slice A/B/C)、產品(先驗證 PMF)。
- Phase 1 把四件難度懸殊的事綁一個 exit;其中**背景服務生命週期**與**關係圖視覺化**是兩隻各吃一週的怪物。
- **建議切片**:
  - **Slice A**:`workingbad sync`(一次性 CLI)→ git Pull → rule 分類 → upsert SQLite →`list`。無背景服務、無 UI、無 Watch。一個下午證明核心。
  - **Slice B**:加唯讀 HTML 列表 + 手動建 entry 表單。仍不做 graph、不做背景服務。
  - **Slice C**:才加 Watch(先輪詢,非 git hook)。
- **graph 視覺化**降級為列表+簡單關聯,延到核心價值驗證後。
- **dogfooding 提前到 Slice A/B**:用 workingbad 管理它自己的 ROADMAP/decision,涵蓋 4/5 種 type、零外部 API,一週內痛點自現。

## P1 — 高風險(單維度但嚴重)

### 5. Secrets 管理規格全空白
**安全(高)**。config.yaml 必然明文存 Slack/ClickUp/GitHub token + Claude key;團隊級 token 單機外洩=團隊外洩。
→ 預設 OS keychain,fallback 0600+加密;最小 scope;不寫進 log。

### 6. AI:Claude「session(免費)vs API(計費)」未區分
**AI(高)**。Phase 4「Claude session source」(復用既有 session,零增量成本)與 `claudeProvider`(自帶 API key 計費)混用同一個「Claude」名詞 → 成本模型天差地別。
→ 拍板預設走 session/MCP(零增量);`claudeProvider` 獨立、標「會計費」、預設關閉。

### 7. 產品 AI 品質無法量(eval-harness 量錯對象)
**AI(高)**。eval-harness 評的是「Claude Code 開發 session」,不是產品的 Classify/Summarize/Relate 輸出。
→ 另立 product-ai-eval:50–100 筆人工標註 ground-truth;Classify 看 accuracy/混淆矩陣、Relate 看 precision@k、Summarize 用 LLM-as-judge。**沒標註集就無法定 threshold**。

### 8. 背景服務生命週期 / Watch 失敗語意未定義
**架構(高)**。Watch panic/斷線誰重啟、多 Source supervise、graceful shutdown、channel 背壓全缺 → goroutine 靜默死亡溫床。
→ 定義 Supervisor/Runner + Scheduler 抽象;v1 可先接受「前景跑、使用者自己背景化」,避開跨平台 daemon 坑。

### 9. PMF 假設未驗證
**產品(高)**。「工程師會為自動回報自架服務+管 5 個 token」是高風險假設。
→ 動工前 5–10 人用戶訪談 + 紙板原型;dogfooding 當最早訊號。

## P2 — 中風險

- **本地 Web UI 安全**(安全 中-高):預設綁 127.0.0.1、CSRF token、Origin/Host 驗證防 DNS rebinding、評估本地 token auth。會外送/改資料的 endpoint 視為 state-changing。
- **Ollama 與「輕量」矛盾**(AI 中):7B 需 5–6GB RAM + 獨立 daemon,違背 Gitea 式單 binary。→ Ollama 降為 opt-in;中段 routing 改用 **embedding(純 Go/小 ONNX,數百 MB)**。
- **Relate 用 LLM 投報率最差**(AI 中):O(N) 呼叫 + 幻覺污染關係圖。→ 預設用 embedding 餘弦相似度做 candidate + relates_to;強關係且使用者要求才升 LLM,設 precision 門檻。
- **rule classifier「信心分數」未定義**(AI 高/中):conventional-commit 是二元 match,無連續信心 → threshold 形同開關。→ 用「match 種類數/欄位完整度」產生 0–1 分;記錄每次 routing 決策供調校。
- **manual entry 冪等鍵**(資料 高):`(manual, NULL)` 在 SQLite NULL≠NULL,UI 重複建會產生幽靈 entry。→ 對 manual 用內容 hash 生 source_ref,或 UI 樂觀鎖。
- **單表異質資料無 per-type 必填契約**(資料 高):靠 connector 自律 → 加 per-type field contract 表 + repository 入庫驗證。
- **FTS5 同步策略未選**(資料/架構 中):FTS5 不能直接 trigger → 選 content table 模式(`content="entries"`)在第一個 FTS migration 前定案。
- **status 對非 goal 型語意雜訊**(資料 中):限定只對 goal 有效,或補齊 per-type 狀態機。
- **migration 機制懸「golang-migrate / 自建」**(資料/可行性):Phase 0 內定案,啟動時 forward-only 自動 migrate,SQL embed 進 binary,失敗 abort。
- **Sink 看不看得到 Edge**(架構 中):關係圖是賣點但 Sink 簽名只有 `[]Entry`。→ 明定要不要同步 Edge / ClickUp subtask 映射。
- **商業模式懸空**(產品 高/中):本地+開源無營收路徑。→ 坦白定位:portfolio/OSS、企業內部部署、或隱私敏感小眾。

## 必做 Spike(按「會否推翻架構」排序)
1. **modernc.org/sqlite 是否真帶 FTS5 且跨平台正確** — 整個搜尋的地基,半天驗證。
2. **背景服務跨平台生命週期** — 決定要不要碰 launchd/systemd/service;建議 v1 前景跑。
3. **git Watch:輪詢 vs git hook** — spike 輪詢 `git log --since` 效能/正確性。
4. **embed.FS + net/http 手刻關係圖可行性** — 結果直接決定 graph 砍不砍。
5. **(延後)各家 API auth + rate limit** — Phase 2 前各半天讀文件。
6. **(Phase 2 前 blocking)modernc 向量支援** — Phase 3 embeddings 可能須換 cgo driver,影響跨平台編譯。

## 決策清單(blocking,需使用者拍板)
1. **定位**:engineer-first 本地任務圖(回報是副作用)/ 小團隊內部工具 / 隱私敏感小眾 — 三選一。
2. **這是產品還是先給自己用的工具?**(決定 graph/跨平台/第二 sink 是否大幅延後)
3. **雙向同步**:v1 承諾 or v2 願景?(強烈建議 v2)
4. **Claude**:預設復用既有 session(免費)還是自帶 API key(計費)?
5. **AI 預設**:純 rule-based 出貨,Ollama 不綁預設?(可延到 Phase 3,不 block 現在)
6. **背景服務 v1**:接受前景跑 / 使用者自己背景化?
7. **產品正式名稱**(codename: workingbad)。
8. **時間預算**(沒有時間框就無法定該砍什麼)。

## 一句話總結
設計品質與工程直覺成熟(解耦、冪等、零成本預設都對),但這是**願景藍圖不是交付計畫**:最大風險不是技術而是 (a) 本地化與自動外送的定位/隱私張力、(b) 雙向同步被低估、(c) Phase 1 太胖。建議大幅瘦身成 Slice A→B→C、雙向同步與多數 connector 移到 v2、用 dogfooding 最早驗證核心價值。
