# v1.0.0 Refactor Plan — 對抗式驗證裁決

**整體裁決：GO-WITH-ADJUSTMENTS**

五位獨立驗證者全數回傳 `needs-adjustment`（無人 NO-GO）。計畫的**核心方向、Gap1/Gap2 技術選型、issue triage 大方向皆成立並通過對抗檢查**——這是一份方向正確、可執行的計畫。但有兩類系統性破洞必須在動 Slice D 之前修正：(1) **Slice D 嚴重低估**——它不是「加一層 node」的 additive 工作，而是要把 supersede / version / FTS5 / bitemporal / edges re-key 整套不變量從 entries 搬到 nodes 的核心重寫，且與 Slice L 的 layout 重建重複計算；(2) **多個 slice 把「net-new 子系統」誤標為「evolve」**——connector runtime（checkpoint / sync_state / registry / scheduler 全部零實作）、AIProvider 的 Split/aggregate method 不存在、clustering 演算法是未解 spike。修正後誠實時程落在 **16–22 週**，計畫的 12 週樂觀地板不成立。所有底層證據經本人 grep 複核屬實。

---

## 計畫破洞

### 1. [HIGH] Slice D「edges become node-edges」不是 additive — 它改寫 append-only chain 的既有欄位語意
- **證據**：`edges.from_id/to_id` 是指向 `entries.id`（per-version id，非 logical_id）的 raw TEXT FK（[0002_edges.sql](internal/migrations/0002_edges.sql)）。整套 supersede re-point 機制（`rePointAllLiveEdges` / `rePointIncoming` / `rePointOutgoing`，[edges.go](internal/repository/edges.go)）在**每次 entry supersede 時都會跑**（pipeline.go:564）。`idx_edges_live_triple UNIQUE(from_id,to_id,relation) WHERE is_current=1` 與 bitemporal `EdgesAt` 的 occurred_at 繼承契約全都建立在 entry-id edges 上。本人複核：**81 處** `from_id/to_id/FromID/ToID` 非測試引用。
- **修正**：把「edges 重新 key 到 node-id」當成**需要 migration 策略決策的破壞性變更**，不是 L-effort。明確二選一：(a) edges.from_id/to_id 改指 node-id（連動 unique-triple index + re-point 不變量整套改寫），或 (b) 新增平行 `node_edges` 表、deprecate 舊 edges。在 D 開工前寫進 decisions 文件。

### 2. [HIGH] 模型反轉未被 slice scope 捕捉 — supersede/FTS/bitemporal 今天全在 entries 上
- **證據**：pipeline-decisions.md:21-23 定義 Entry 為 source-immutable、Node 為 mutable 圖單元，**反轉**今天的模型——今天 supersede + version + FTS5 + bitemporal occurred_at/ingested_at **全部活在 entries**（service.go:494 `FlipEntrySuperseded`、pipeline.go:564、0007_entries_fts.sql）。`materializeOne`（pipeline.go:341）目前用 `Summarize` 合成 activity **entries** 並依 source_ref supersede。
- **修正**：Slice D 必須**先**把 supersede/version/FTS5/bitemporal time-travel 與 segment→activity 寫入路徑從 entries 搬到 nodes，G/H/I 才能寫 node mapping。這是 prerequisite，不是「加一層」。建議拆 **D1**（additive node 表 + backfill + sqlc，~1wk）與 **D2**（搬遷 supersede/edges/FTS/bitemporal + 測試重寫，~3-5wk），隔離最危險的不變量工作。

### 3. [HIGH] AIProvider interface 不表達 3-step pipeline — F 是 interface 重設計，非 mock→real swap
- **證據**：[provider.go](internal/ports/ai/provider.go) 只有 `Classify/Summarize/Relate`。**沒有 Split method**（`Summarize` 是其逆運算 N→1）；**沒有 aggregate method**；`Relate` 是 entry-keyed 且回傳的 `domain.Edge` **沒有 Confidence 欄位**。本人複核：`Relate` 與 `Classify` **零 production caller**（只有 `Summarize` 接在 pipeline.go:385）——它們是 dead interface slot。
- **修正**：F 重新 scope 為「設計新 Split method + 把 Relate 改成 node-keyed 且回傳 confidence + 首次 wiring Classify/Relate + 搭 3-stage orchestrator 骨架」。在 F 設計期就**鎖定新 method 簽章**，別把 Split 設計外溢到 G。F 從 L (~2wk) 提到 **~2.5-3wk**。

### 4. [HIGH] 沒有 orchestrator seam — 唯一驅動是單步 BatchMaterialize 迴圈
- **證據**：repo 內唯一 orchestration 是 `Service.BatchMaterialize`（pipeline.go:321-339），單 pass 呼叫 `materializeOne → Summarize`。沒有 split/aggregate/relate driver、沒有 per-step state machine、沒有 halt-queue、沒有 cluster step。
- **修正**：3-stage orchestrator（Split → vector-cluster+LLM-validate → Layer A/B/C relate）是**淨新代碼**，不是 BatchMaterialize 的演進。歸到 F scope。

### 5. [HIGH] Connector runtime 完全不存在 — J/K 從空 interface 起步
- **證據**：本人複核 `source_checkpoint`(0006) 與 `sync_state`(0005) 表**零 sqlc query、零 Service method**（非測試 grep 回傳空）。`Source`/`Sink` interface 零 implementer、零 caller。`OriginPushed` 除了 validator switch（service.go:147）外**從未被寫入**——local→pushed 轉換路徑不存在。無 background scheduler（grep ticker/scheduler 為空）。
- **修正**：J/K 共享一個**未估算的前置子系統**：connector runtime（registry disjoint-set wiring + checkpoint/sync_state Go 持久化 + Watch→Pull degrade scheduler）。把這 ~1wk plumbing 指派給 J（或 pre-J slice），否則 K 被 block。J: ~1wk→**~1.5wk**；K: ~1wk→**~1.5-2wk**。

### 6. [HIGH] Pure-Go HDBSCAN 不存在堪用版本 — Slice H clustering 是未解 spike
- **證據**：Belval/hdbscan 自述「Project is not in working condition」（3 stars，未維護）；humilityai/hdbscan 倉庫 404 不可存取；urnetwork/cluster 是其衍生品同樣未維護。`sqlite-vec` 只給 KNN index，**不給 clustering 演算法**。
- **修正**：H 在估到 'L' 之前**必須先選定演算法**。建議 Phase 1 **pre-commit k-means 或 epsilon-ball**（簡單可行），把 HDBSCAN 推 Phase 2。並解決 scope 矛盾：decisions 文件說 Phase 1 含 clustering theta=0.80，refactor 文件卻 defer 到 Phase 2——**二者必須對齊**，否則 Step 2 aggregate 在 Phase 1 跑不起來。

### 7. [MED] seed-github「evolve/M」高估可重用性 — Pull/checkpoint/raw-write 是淨新
- **證據**：[seed_github.go](cmd/workingbad/seed_github.go) 自述「not the real GitHub Source connector」。`fetchAllIssues`（:228）**每次都從 page=1 全量重抓**（無 since/checkpoint），與 incremental 相反。直接呼叫 `svc.InsertEntry`，繞過 Source interface。`closesRefRegexp`（:159）是 GitHub-PR-body 專用、硬綁 `part_of`+`goal`。
- **修正**：只有 HTTP/regex/auth helper 可重用；`Source.Pull` + Checkpoint cursor + raw layer 是淨新。Layer-A 泛化（git trailers / Slack refs / ClickUp links → 任意 typed edge）是**新抽象**，把 per-source impl 分攤到 J/K，別全壓在 I。

### 8. [MED] GitHub raw layer 形狀不對 — 0004 是 git-specific，無法重用
- **證據**：[0004_raw.sql](internal/migrations/0004_raw.sql) 是 `sha/patch_id/parent_shas/diff/branch_hint` git 形狀。GitHub issue/PR raw（issue number/state/labels/**updated_at incremental cursor**）不存在。
- **修正**：J 必須新增 GitHub raw 表（含 updated-since cursor 欄位才能 incremental Pull），不能套用 git raw schema。

### 9. [MED] edge.confidence 所有權在 D 與 I 之間曖昧
- **證據**：本人複核 `domain.Edge`（types.go:134）**無 Confidence 欄位**；`InsertEdge` 與 0002 migration 無 confidence 欄。新增牽動 Edge struct + 新 migration + sqlc regen + 每個 edge writer——與 Slice D 的「confidence REAL col」重疊。
- **修正**：在 **D 明確決定是否出 confidence column**。若 D 不出，I 會默默繼承一個沒編列的 schema migration + sqlc regen。

### 10. [MED] 本地 checkout 過期 — triage 在過期樹上執行會誤判
- **證據**：本人複核兩個本地樹皆**缺 PR #62**：workingbad-perf-12 在 `retro/actions`(6e95238)、workingbad main 在 e0e5e93(#46)。`git merge-base --is-ancestor dab1b03 HEAD` 在兩樹皆 **NO**。server.go 仍是 `err == http.ErrServerClosed`、無 `Server.Close`、無 `path.Base`。
- **修正**：任何 triage / Slice L 工作前**先 sync 到 origin/main**（a600faa / PR #65）。在過期樹執行 #20/#21「verify→close」會看到未修代碼而誤判重開。

### 11. [MED] 「confidence slider 需要 deferred JS layer」與代碼相反（方向性反向破洞）
- **證據**：本人複核互動式 JS 層**已 ship**：[graph.js](internal/web/static/graph.js) 338 行（drag-pan / zoom / tooltip / side panel / search-fade / `applyEdgeFilter` edge-toggle），[app.css](internal/web/static/app.css) 已有 `stroke-dasharray`。MEMORY 標為「deferred」的 redesign **已透過 PR #56 landed**。
- **修正**：confidence slider 是既有 `.edge-toggle` 的近複製——這個子任務**比計畫便宜**。但反向地，Slice L 的 mutation-action 風險被低估（見下）。

### 12. [MED] Slice L 的 merge/split/link 被 backend block
- **證據**：本人複核 `MergeEntries` / `SplitEntry` / 泛型 `Unlink`/`LinkEdge` **全不存在**，只有 part_of 專用的 `AttachToGoal`/`DetachFromGoal`（edges.go:19,100）。graph.js 點擊只開唯讀 panel，無 mutation UI。
- **修正**：#49/#50/#52 是淨新 RepositoryService method。L 的 ~3wk **只在這些 method 於 owning slice (G/H/I) 先交付時成立**；否則 L 吸收它們、漲到 ~4-4.5wk。把此依賴在 DAG 上明寫。

---

## 修正後時程

計畫原估 **12-16wk**。各 slice effort_correction 後（單人開發、守 TDD 80% 覆蓋）：

| Slice | 計畫估 | 修正估 | 主因 |
|---|---|---|---|
| D | XL ~2-3wk | **~4-6wk**（拆 D1 ~1wk / D2 ~3-5wk） | supersede/FTS/bitemporal 搬遷 + edges re-key + ~4,388 行測試重寫 |
| E | L-XL ~1.5-2wk | ~1.5-2wk（+1wk 若 HDBSCAN spike 失敗） | clustering 需 pre-commit k-means/epsilon-ball |
| F | L ~2wk | **~2.5-3wk** | interface 重設計 + Split + 首次 wiring + orchestrator 骨架 |
| G | L ~1wk | ~1-1.5wk | split-method 設計外溢自 F |
| H | L ~1wk | ~1.5-2wk | clustering 演算法未解 spike |
| I | XL ~2-3wk | ~2-3wk（吸收 Layer-A 泛化 + 若 D 不出 confidence 則 +migration） | — |
| J | L ~1wk | **~1.5wk** | + 共享 connector runtime plumbing ~1wk |
| K | L ~1wk | **~1.5-2wk** | sync_state + origin local→pushed + registry（零實作） |
| L | XL ~3wk | **~3-4.5wk** | merge/split/link backend-blocked；confidence slider 反而更便宜 |

**誠實總時程：~16-22wk**（含拆出的 connector-runtime plumbing ~1wk）。計畫的 **12wk 平行樂觀地板不成立**；即使理想平行化也應抓 16wk 上界起跳。關鍵警示：**de-double-count D 與 L 的 layout 重建**（layout.go 404 + timeline.go 234 LOC 的 node-render 重寫在兩個 slice 都被算了一次），以及 **J↔D 依賴為真**（Pull 必須寫 nodes 非 entries，J 無法如 DAG 暗示般與 D 平行）。

---

## Issue Triage 核對結果

| Issue | 計畫 disposition | 核對結果 |
|---|---|---|
| **#20 Server.Close** | PR #62 已實作→close | **成立**。本人複核 `dab1b03` 在 origin/main，`server.go` 含 `Server.Close` 與 graceful `Shutdown`。**可 close**。 |
| **#21 errors.Is/path.Base** | PR #62 已實作→close | **成立**。同 PR #62（body「Closes #19, #20, #21」，state MERGED，base main，mergedAt 2026-06-12）。**可 close**。 |
| **#49 merge / #50 split / #52 link-unlink** | backlog→promote 為 CORE | **成立且為真實 gap**。本人複核三者皆無實作，只有 part_of 專用 AttachToGoal/DetachFromGoal。GitHub 上仍 OPEN，promote 正確。 |
| **#14-18** | fold 進 Slice L | 一致，無異議。 |
| **#48 collaborators** | 折進 L | 成立但需 node-level actor model，加重 L。 |
| **#25/#32/#41** | defer post-v1 | 一致，無異議。 |

**Triage 唯一警示（非 disposition 錯，是執行環境錯）**：#20/#21「verify→close」**在 origin/main 上正確，但在兩個本地過期 checkout 上會看到未修代碼**。執行 triage 前必須 sync 到 origin/main（a600faa），否則風險誤判重開。GitHub 上 #20/#21 截至檢查時仍 OPEN——**verify→close 動作尚未實際執行**，需在同步後的樹上做。

---

## 確認可行的部分（通過對抗檢查、計畫的穩固核心）

1. **Gap1（Embedder 解耦）正確**：本人複核 AIProvider **無 Embed method**，把 Embedder 設為獨立可插拔 interface 與現況一致、無需 unwind。nomic-embed-text 為真實 Ollama 模型（768-dim，~1.5GB，laptop-CPU 可行）。
2. **Gap2（viant/sqlite-vec pure-Go）技術選型正確**：純 Go、無 CGO、與 `modernc.org/sqlite v1.52.0` 內建 vec 相容。WASM fallback（ncruces/go-sqlite3，678 stars，活躍）為真實退路。**單一 Go binary 鐵律存活**。
3. **「entries ARE nodes、無 node 層」前提屬實**：本人複核 migrations 無 nodes 表，bifurcation 是真議題——計畫沒誤判現況，只是低估工作量。
4. **v0.1.0 未凍結 → additive DDL 機制可行**：`git tag -l` 為空，pre-tag 可編輯，0012 已示範 additive 新表 + INSERT…SELECT backfill。**DDL 是 easy part，計畫對這點判斷正確**。
5. **raw layer (0004) 已實作且測試覆蓋**（git-specific），`UpsertRaw` 運作中——J 的 git raw 基礎為真。
6. **Ollama structured JSON 輸出可用**：支援 json_schema 約束解碼，緩解 G/F 的 JSON-repair 風險（但語意驗證 loop 仍需保留）。
7. **互動式 graph JS 層已 ship（PR #56）**：confidence/search/filter 等 UI 風險被計畫**高估**——這些是既有 pattern 的近複製。

---

## 建議修正動作（動 Slice D 之前的具體文件編輯）

對 [2026-06-13-v1-refactor-plan.md](docs/grill/2026-06-13-v1-refactor-plan.md) 與 [2026-06-13-v1-pipeline-decisions.md](docs/grill/2026-06-13-v1-pipeline-decisions.md)：

1. **拆 Slice D 為 D1 + D2**：D1 = additive node 表 + entry_node_map + backfill + sqlc（~1wk）；D2 = 搬遷 supersede/version/FTS5/bitemporal + edges re-key + 測試重寫（~3-5wk）。把 D2 標為 ★最高風險、需獨立 green。
2. **在 decisions 文件加一條 edges re-key 決策**：明寫二選一——(a) edges.from_id/to_id 改 node-id（連動 UNIQUE live-triple index + rePointAllLiveEdges 重寫），或 (b) 新增 node_edges 表 deprecate 舊 edges。**不可留「edges become node-edges」這種掩蓋破壞性的措辭**。
3. **在 D scope 明確指派 edge.confidence column 的所有權**（建議 D 出），消除 D/I 曖昧；否則在 I 編列一筆 schema migration + sqlc regen。
4. **重寫 Slice F scope**：從「mock→real swap」改為「AIProvider interface 重設計（新增 Split、Relate 改 node-keyed+confidence、首次 wiring Classify/Relate）+ 3-stage orchestrator 骨架」。F 升 ~2.5-3wk。在 F 鎖簽章，禁止外溢到 G。
5. **解決 clustering 的 Phase 1/2 矛盾**：在 decisions 文件統一——Phase 1 **pre-commit k-means 或 epsilon-ball**，HDBSCAN 明確推 Phase 2（pure-Go HDBSCAN 無堪用版本）。把此 spike 與 E 的 viant/sqlite-vec spike **一起前置**。
6. **新增「connector runtime」前置 slice（或併入 J）~1wk**：registry disjoint-set + source_checkpoint/sync_state Go 持久化 + Watch→Pull scheduler。明寫 K 依賴它，並標 J↔D 為真依賴（J 不可與 D 平行）。
7. **下修 seed-github 重用度**：把 J 的 GitHub Source 從「evolve/M」改「new/L（重用 HTTP/regex/auth helper）」，並新增 GitHub raw 表（含 updated-since cursor）；把 Layer-A 跨來源泛化的 per-source impl 分攤到 J/K，別全壓 I。
8. **修正 Slice L 框架**：刪除「confidence slider 需 deferred JS layer」措辭（graph.js 已 ship/PR #56）；改強調 L 的真實風險在 merge/split/link **backend-blocked on #49/#50/#52**，並在 DAG 明寫這些 RepositoryService method 須於 G/H/I 先交付，否則 L 漲到 ~4-4.5wk。
9. **更新整體時程**：12-16wk → **16-22wk**，並加註「de-double-count D 與 L 的 layout/node-render 重建」。
10. **加一條 pre-flight checklist**：所有開工與 triage 必須在 sync 到 origin/main（a600faa/PR #65）的樹上進行；先在同步樹執行 #20/#21 的 verify→close。

---

*產製：獨立對抗式驗證 Workflow（`v1-plan-feasibility-verify`, run `wf_7e037a7a-02e`）— 5 位風險聚焦驗證者（grep 真實 code + web 核對）+ 1 synthesizer。6 agents / ~398k tokens / 138 tool-uses。*
