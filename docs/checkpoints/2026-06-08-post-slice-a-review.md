# Post-Slice-A Multi-Agent Review — 2026-06-08

> 7 PR(#1–#7)合進 main、Phase 1 Slice A dogfooding exit 達成後,三個專業 agent 平行做的程式碼審查彙整。
> 焦點:**效能 / 資料最終一致性 / 架構**。

## 整體判斷

Phase 1 Slice A 的 schema/契約/測試覆蓋扎實,但**有一處三方都聚焦的中樞**:`materializeOne` 的 supersede 鏈。
- 效能:同 tx 內呼叫 Summarize → Phase 3 真 provider 上線即凍住寫入。
- 一致性:supersede activity 後**沒 re-point outgoing edges** → 已 attach goal 的 activity re-summarize 後從 goal 視圖消失。**這是 Phase 1 dogfooding 真實會踩的 bug,不等 Phase 2。**
- 架構:`BatchMaterialize` 對 `ports/ai` 的硬依賴 → 反向 dep。

## P0 — 立即修(Phase 1 已破契約)

### #1 `materializeOne` supersede 沒 re-point outgoing edges
**檔/行**:[internal/repository/pipeline.go:332-335](../../internal/repository/pipeline.go) + [supersedeEntryInTx](../../internal/repository/pipeline.go) 缺對稱版。
**契約**:grill round 6 拍板「supersede 自動 re-point(同 tx,Summarizer 負責)」。`SetGoalStatus` 用 `rePointIncomingEdges` 處理 `to_id`;`materializeOne` 的 activity supersede 對 `from_id` 完全沒處理。
**現象**:
1. CLI:`attach A1 G1` → edge `from=A1, to=G1, part_of, is_current=1`
2. CLI:`summarize`(觸發 re-Summarize 同 segment)→ `A1 is_current=0`,新 `A2` 同 LogicalID
3. `GetGoalActivities(G1)` 用 `JOIN entries e ON edges.from_id = e.id WHERE e.is_current=1` 找不到 A2(edge 指向 A1 而 A1 not current),A1 自己也被 `is_current=1` 過濾掉。**結果:goal 視圖 attached activity 消失。**
**修法**:
- 新增 `rePointOutgoingEdges(ctx, qtx, oldID, newID)` 對稱版(遍歷 `from_id = ? AND is_current = 1`,supersede 舊 + insert 新 `from_id=newID`)。
- 在 `supersedeEntryInTx` 末段呼叫(讓 `service.Supersede` / `materializeOne` 兩條路徑共享)。
- 加測試 `TestBatchMaterialize_ReMaterialize_PreservesAttachedGoal` 鎖死。

## P1 — Phase 2 / Phase 3 上線前必補

### #2 `materializeOne` 把真實 Summarize 放在 sqlite write tx 裡
**檔**:[pipeline.go:280-345](../../internal/repository/pipeline.go)
Phase 1 mock 是純函式看不到;Phase 3 接 Ollama/Claude 後一次 5–60s 跨網路呼叫期間 hold write lock,pool=1 下所有其他寫入超過 `busy_timeout=5000ms` 直接拋 SQLITE_BUSY。grill round 8 明文「真 API 邊界 Phase 1 就對」,目前不對。
**修法**:三段拆解 — RO tx 讀 changes → 即 commit → tx 外 Summarize → 短 RW tx 寫 entry + 重查 live(防 race)+ 樂觀鎖 segment.updated_at。

### #3 `repository.BatchMaterialize` 對 `ports/ai` 反向依賴
**檔**:[pipeline.go:14](../../internal/repository/pipeline.go)
repository 不該認得 port。Phase 3 換真 provider 後所有 caller(CLI/HTTP/scheduler)都會被迫 import `ports/ai`。
**修法**:在 `internal/repository` 定義窄介面 `Summarizer interface { Summarize(ctx, []RawChange) (string, string, error) }`,`BatchMaterialize` 收這個,`ports/ai.AIProvider` 自然 satisfy。依賴倒置成本接近零。

### #4 mock AIProvider 會被 link 進正式 binary
**檔**:[adapters/ai/mock/mock.go](../../internal/adapters/ai/mock/mock.go) + [commands.go:313](../../cmd/workingbad/commands.go) `actionSummarize` 直接 import
**風險**:Phase 3 上線時 mock 若還在 binary 內,變成「未設好 provider 時的隱性 fallback」,直接違反 CLAUDE.md「AI 必要能力,無 fallback」鐵律。
**修法**:現在就用 `//go:build phase1mock` build tag 框住,Phase 3 PR 拆掉並要求 config 顯式 provider。

### #5 沒 file-lock 防雙 instance 開同一個 DB
**檔**:[db.go:29-31](../../internal/repository/db.go)
`SetMaxOpenConns(1)` 註解承諾是「架構不變量」,但兩個 workingbad process 開同一 DB 就破。
**修法**:startup `syscall.Flock` on db file 或 sentinel,第二個 fail-fast。

### #6 `InsertRawCommit` / `InsertRawChange` 用裸 INSERT
**檔**:[queries/raw.sql:13](../../internal/repository/queries/raw.sql)
Grill round 12 明文「顯式 ON CONFLICT DO NOTHING」,目前是 Go 層先 SELECT 後 INSERT;pool=1 OK,Phase 2 多 instance 會撞 PK rollback。
**修法**:`INSERT ... ON CONFLICT(sha) DO NOTHING`(raw_commits)、`ON CONFLICT(change_id) DO NOTHING`(raw_changes)。便宜。

### #7 invariant 9 沒 DB-level 強制
**檔**:[migrations/0001_entries.sql](../../internal/migrations/0001_entries.sql)
「per (segment_id, type) ≤1 live」只靠 Go 層 `GetLiveActivityForSegment` 偵測。pool=1 安全,Phase 2 多 instance 並發 materialize 會雙活。
**修法**:加 partial unique idx `ON entries(source, source_ref, type) WHERE is_current=1 AND source='git'`(pre-v0.1.0,可改)。Phase 4 多型時 generalize 是 additive。

### #8 silent corruption — `parseRFC` / `formatRFC` 吞錯/隱式替換
**檔**:[conv.go:107-117](../../internal/repository/conv.go) + [queries.go:66](../../internal/repository/queries.go) 同 pattern
`time.Parse` 錯誤吞 → row 時間欄變 epoch,排序錯亂;`formatRFC` 對 zero time 偷偷 substitute now() → caller 忘賦值 bug 被掩蓋。違反全域 coding-style「NEVER silently suppress errors」。
**修法**:`parseRFC` 改回傳 error,call site 必須處理或 must 包裝;`formatRFC` 對 zero time panic 或 error。

## P2 — Slice B 之前該補(架構接縫)

### #9 `repository.Service` 沒抽 interface,decorator 接縫缺
**檔**:[cmd/workingbad/commands.go:86-97](../../cmd/workingbad/commands.go) `withService(fn func(*repository.Service) error)`
Slice B HTTP 要套 CSRF/mutationGuard chain seam(ROADMAP 承諾的 additive)、Phase 3 要套 budget/caching wrapper、Phase 2 要套 sync barrier instrumentation — 全部需要在 service 邊界塞 decorator,目前沒切口。
**修法**:`type RepositoryService interface { ... }` 列出公開方法,`*Service` 自然滿足;CLI/HTTP/sync 都收 interface。Additive,零 caller 改動。

### #10 `EditEntry`(COALESCE_WINDOW)未實作
**檔**:[service.go](../../internal/repository/service.go)
connector-interface skill 第 122 行列出 `EditEntry(ctx, id, patch)`;目前只有 `Supersede`,CLI 也沒 edit 子指令。Slice B Web 表單只能 supersede,違反「manual 未引用/未 push + 5min 窗內原地 UPDATE」例外規定。
**修法**:補 `EditEntry` 含 COALESCE_WINDOW 判斷;CLI 加 `edit <id> <new-title>` 子指令。

### #11 `iteration_of` 寫得進讀不到
**檔**:[queries.go:89-105](../../internal/repository/queries.go) `GetGoalActivities` 只走 logical_id 不走 iteration_of;但 `Edge.Relation` enum 含 iteration_of,`AttachToGoal` 沒擋。
**現象**:有人手動建 iteration_of edge,讀路徑靜默漏資料。
**修法**:二擇一 — (a) `AttachToGoal` 加 wantRelation 參數,Phase 1 寫死 part_of;或 (b) 在 query 加 recursive CTE 沿 iteration_of 遞迴(grill 拍板的做法,但 Phase 1 可延)。

### #12 動態 SQL 戳穿 sqlc 真理來源承諾
**檔**:[queries.go](../../internal/repository/queries.go) ListEntries / CountPendingSegments + [pipeline.go](../../internal/repository/pipeline.go) listSegmentsNeedingMaterialize
三條手寫 SQL 對 schema 漂移無防護。Slice B Web 一加 optional filter 就會更多。
**修法**:用 sqlc 的 NULL filter 寫法(`WHERE (?1 = '' OR type = ?1)`)收回真理來源。

### #13 缺 FK
**檔**:[migrations/0002_edges.sql](../../internal/migrations/0002_edges.sql) + 0003
`edges.from_id/to_id`(指 entries.id)、`entries.superseded_by`(指 entries.id)、`segment_raw.change_id`(指 raw_changes)都**沒設 FK**。`PRAGMA foreign_keys=ON` 已開但無 FK 約束無效。
**修法**:pre-v0.1.0 可改,直接加上 FK。

### #14 `raw_commits.sha` 全表 PK,沒帶 repo_id
**檔**:[migrations/0004_raw.sql](../../internal/migrations/0004_raw.sql) + [queries/raw.sql](../../internal/repository/queries/raw.sql) `GetChangeIDBySHA`
兩個 fork repo 同 commit sha 會被視為同一個,第二個 repo 永遠拿不到自己的 commit。極罕見但可能。
**修法**:`PRIMARY KEY (sha, repo_id)` 或 `GetChangeIDBySHA` 帶 repo_id 雙鍵。

## P3 — Polish / 小幅優化

| # | 項目 | 檔 |
|---|---|---|
| 15 | `patchid.go` 4MB 上限靜默失敗,大 diff 失去 rewrite 連結 | adapters/git/patchid.go:48 |
| 16 | `UpsertRaw` idempotent early-exit 不需開 write tx | pipeline.go:46 |
| 17 | `DetachFromGoal` 對 already-detached 回錯,HTTP 標準上應 idempotent | edges.go:97 |
| 18 | DB CHECK `from_id != to_id` 防自迴圈邊 | migrations/0002_edges.sql |
| 19 | CLI emoji `✓` 在 Windows cmd 壞 | commands.go:多處 |
| 20 | `titleAndBody` 改 `strings.Join` 一行更乾淨 | commands.go:222 |
| 21 | `Supersede` 不檢查 type/source 跟 old 一致(允許 type 切換但語意錯) | service.go |
| 22 | `Edge.IsCurrent` 在 `AttachToGoal` 回傳 hardcoded true,沒回讀 | edges.go:82 |

## Phase 2 多 instance 升級前必補清單

按優先序整理(把 P0–P3 排平):

1. **P0** `rePointOutgoingEdges` + 同 tx 掛 `supersedeEntryInTx` + 回歸測試 → 修 dogfooding bug
2. **P1** `materializeOne` 三段拆 → 修 Phase 3 凍寫鎖
3. **P1** Service Summarizer interface 倒置 → 修架構反向 dep
4. **P1** mock build tag + 拒絕未設 provider 啟動 → 守 AI 必要能力鐵律
5. **P1** db file lock → 防雙 instance
6. **P1** InsertRaw ON CONFLICT → 對齊 grill 契約
7. **P1** invariant 9 partial unique idx → 鎖死「per (segment,type)≤1 live」
8. **P1** parseRFC/formatRFC 不吞錯 → 守 silent-failure-hunter 鐵律
9. **P2** `repository.Service` 抽 interface → Slice B / Phase 2 / Phase 3 都吃 decorator
10. **P2** `EditEntry` + COALESCE_WINDOW → Slice B Web 表單前
11. **P2** iteration_of 寫入閘擋掉 OR 補遞迴 CTE → 修「寫得進讀不到」
12. **P2** 動態查詢 sqlc 化 → 收回真理來源
13. **P2** 缺漏的 FK 補齊 → 防 dangling reference
14. **P2** `raw_commits` PK 改 `(sha, repo_id)` → 修跨 repo 撞 sha
15. **P3** patchid 大 diff warning + buffer 上限提到 64MB
16. **P3** Phase 2 評估 `synchronous=FULL` 或文檔化 NORMAL 契約
17. **P3** 其餘 polish(emoji、Join、自迴圈 CHECK …)

## 不該動的地方(防 over-optimize)

效能與架構審查共同確認:
1. **`SetMaxOpenConns(1)` 單寫者模型** — 對的設計,WAL 讓讀仍並發,不要拆讀寫池(profile 沒指證前)。
2. **per-segment 獨立 tx** — `BatchMaterialize` 一段失敗不擋下一段的 isolation 結構是對的。修 P0/P1 時要保留這條。
3. **三條 partial unique idx**(`idx_edges_live_triple` / `idx_segments_needs_summary` / `idx_raw_changes_repo_patch`)— 設計都對且有效,**不要動**。
4. **patchid 用 SHA-1 + 不追求 git bit-identical** — 設計上明確只要 internal consistency。改 SHA-256 / BLAKE3 沒收益且破壞既存資料。
5. **append-only + supersede 由 schema + Go + same-tx 連動三層兜起來** — 核心對。

## 結論

**核心健康,但有 1 個 P0 bug 不修就會在 dogfooding 真實踩到**(re-point outgoing edges)。其餘 P1 都是 Phase 2/3 上線前必補,**沒有架構層需要 unwind 的問題**——所有 finding 都是 additive 修法(加 method / 加 index / 加 build tag / 加 interface),不破壞既有設計。

下一步建議:**先用一個 PR 修 P0(+ 相關回歸測試),再決定 Slice B 還是先掃 P1**。
