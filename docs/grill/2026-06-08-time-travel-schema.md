# Grill: Bitemporal Time-Travel Schema Design

- **Date**: 2026-06-08
- **Phase**: 1, Slice A.5（位於 PR #8 merge 後、Slice B Web UI 之前）
- **Status**: Locked — 進入實作
- **Trigger**: 用戶提案「對工程師來說能像 git 那樣 time-travel 蠻重要」，且資料結構必須在 v0.1.0 tag 凍結前定案
- **Workflow**: 多 agent grill（search-specialist 先做 prior art → architect 正向設計 → silent-failure-hunter 既有 code 失敗模式 → architect-red-team 紅隊壓力測試 → 用戶拍板）

---

## 1. 問題定義

讓 workingbad 在不偏離「單一 Go binary、本地 SQLite、AI 必要能力、disjoint sets 同步」這些不可逆約束的前提下，支援：

1. **時間旅行**：「6/8 14:00 那個 goal 長什麼樣？」
2. **版本史**：「這個 decision 從建立到現在改過幾次？每次改了什麼？」
3. **事件時間 vs 落地時間**：sync 從 GitHub / Slack / ClickUp / git 拉資料時，**保留 source 端事件時間**，不被本地落地時間覆蓋
4. **Audit**：知道每次 supersede 是誰、為什麼

不做：cross-machine snapshot、event sourcing 全套、commit DAG（branch/merge）、compliance audit log。

---

## 2. 為何 v0.1.0 凍結前必須做

ROADMAP 寫死：**v0.1.0 tag 是 migration 凍結點**，tag 後純 additive、永不改舊檔。而 v0.1.0 是 Slice B 結束時打的 tag。

時間欄位語意決策若拖過 v0.1.0：
- 無法把 `created_at` 拆成 `occurred_at` + `ingested_at` 兩種語意
- 無法在既有表加 NOT NULL 欄位（需另開 nullable + 應用層強制）
- 無法統一改 supersede 路徑加 `actor` / `reason`

**結論**：schema-shape 的調整成本在 v0.1.0 前是線性的，過 tag 後是指數的。現在動最便宜。

---

## 3. 三輪 grill 流程紀錄

### Round 0 — Prior Art Survey（search-specialist）

研究 6 條主流路線並排序適配度：

| 方案 | 適配 | 結論 |
|---|---|---|
| Query-Time Versioning（用既有 supersede chain）| ⭐⭐⭐⭐⭐ | ✅ 採為主軸 |
| Bitemporal (SQL:2011 system + valid time) | ⭐⭐⭐⭐ | ✅ 並用：拆 occurred_at / ingested_at |
| Event Sourcing | ⭐⭐⭐⭐ | 🕒 延 Phase 2 加 Sinks 時再評 |
| Trigger-based history table | ⭐⭐⭐ | ❌ 我們 supersede chain 已等價 |
| Dolt commit DAG | ⭐⭐ | ❌ single-writer 不需要 branch/merge |
| XTDB | ⭐⭐ | ❌ 破壞單 binary 假設、JVM 依賴 |

關鍵 deviation：search 建議在 #1 之上加 snapshot table（`(logical_id, valid_at, current_id)`）。**拒絕**：我們既有 `superseded_by` 鏈 + 適當 index 等價，N×N 雙寫無謂。

### Round 1A — 正向設計（architect）

主要產出：

- 每表 schema delta：entries / edges / segments / segment_raw / raw_commits / raw_changes / sync_state / source_checkpoint / entries_fts 各別判斷是否加 `occurred_at`、是否 rename
- Source → occurred_at 映射候選表
- API 用 variadic `opts ...EntryOpts` 保 back-compat
- Migration 提議改既有 0001~0007 + 新加 0008
- Query 層方法簽名 + 核心 SQL pattern
- 5-PR 拆分計畫
- 8 大風險（FTS 同 tx 維護、supersede 邊繼承、modernc 限制、goose forward-only、clock skew、logical_id 共用、…）

### Round 1B — 既有 code 失敗模式（silent-failure-hunter）

掃 conv.go / service.go / pipeline.go / edges.go / queries.go / db.go，產 8 條 P0/P1/P2 風險：

| 等級 | 位置 | 問題 |
|---|---|---|
| P0 | conv.go:112-117 | `formatRFC` 對 zero time 默默用 now 取代 — 既存 silent corruption；加 `occurred_at` 後雙倍曝險 |
| P0 | conv.go:107-109 / queries.go:66-67 | `parseRFC` 吞錯，malformed 時序回傳 zero 又被 formatRFC 蓋回 now |
| P1 | service.go:157-162 | `stampTimes` 永遠用 server now 覆蓋 `updated_at`；加 bitemporal 後若改不到位會把 occurred_at 也蓋掉 |
| P1 | pipeline.go:386-387 | `supersedeEntryInTx` 每次 supersede 都 stampTimes(now)，等於 activity 被 re-summarize 後 occurred_at 從 author_time 漂移 |
| P1 | pipeline.go:310-318 | `materializeOne` 完全沒有路徑把 `raw_commits.author_time` 送進 entry — RawChange struct 不帶 author_time |
| P1 | pipeline.go:113-115 | `UpsertRaw` 對 zero `AuthorTime` silent fallback 危險（formatRFC 會把 now 寫進 occurred_at） |
| P2 | edges.go:198,216 | `rePointAllLiveEdges` 用單一 `time.Now()` 跑整輪 — 加 occurred_at 後被 query 時序錯亂 |
| P2 | queries.go:28 | `ListEntries` 硬寫 `is_current = 1`，無 override 路徑；bitemporal 沒對應 query 等於默默漏掉歷史 |

### Round 2 — 紅隊壓力測試（architect, red team mode）

對 Round 1A 設計的 8 個決策逐一攻擊，並提整體設計遺漏。重要對撞：

| 議題 | 1A 原案 | 紅隊改寫 | 採納 |
|---|---|---|---|
| API 簽名 | `opts ...EntryOpts` | OccurredAt/Actor/Reason 升級為 `domain.Entry` first-class 欄位；API 簽名不變 | ✅ 紅隊 |
| Migration 路線 | 改既有 0001~0007 + 0008 | 純 additive（0008/0009/0010），保留 dogfooding DB 不中斷 | ✅ 紅隊 |
| FTS5 加 UNINDEXED occurred_at | 加（為未來 as-of FTS） | 不加 — FTS5 UNINDEXED 欄位**不能用 WHERE 過濾**，是 cargo cult | ✅ 紅隊（技術事實校正） |
| OriginFetched 缺 occurred_at | 直接 reject | Source 回傳 `OccurredAtCandidate[]` + `Primary`，缺則 `quality=degraded` 旗標，**不靜默 fallback** | ✅ 紅隊 |
| 未來時間 | reject @ now+5min | 不檢查，> now+24h 記 warning event；理由：ClickUp due_date 是合法未來 | ✅ 紅隊 |
| source_checkpoint append-only | append-only + background prune | **保持 UPSERT** + 加 `last_success_at / last_failure_at / failure_reason`；只 `sync_state` 改 append-only；prune 改啟動時 | ✅ 紅隊 |
| PR 拆五包 | 5 PR | 單 feature branch + N commit + squash merge；dogfood-first | ✅ 紅隊 |
| segments occurred_at | 不加（Phase 2）— 但 materializeOne 又要用，自相矛盾 | 加 `occurred_at_min / occurred_at_max` 兩實體欄位，UpsertSegment 算一次 | ✅ 紅隊 |

紅隊提出的根本問題（採納為「設計關注事項」）：

1. **`Supersede` 並發**：兩 sync worker 同時 supersede 同 `oldID` 沒寫明 → **採納 `expected_version` 樂觀鎖**
2. **重複 fetch idempotency**：retry 同 PR 再進來 → **採納 `source_event_hash` 指紋**
3. **deep supersede chain**：500 層碰 SQLite recursion limit → **監控 + 未來壓縮**（不在本 slice 實作）
4. **timezone**：UTC 存 + local 顯示 → **Web UI 範疇，延 Slice B**
5. **clock 跳變**：NTP 校正後 now() 倒退 → **記為未來關注事項**，本 slice 不引 monotonic clock
6. **query planner**：as-of + FTS + edges 三段 query 可能選錯 index → **CI 加 `EXPLAIN QUERY PLAN` golden test**

紅隊 meta 主張「現在不是動 bitemporal 的時候、先 dogfood 一個月」：用戶評估後否決，理由 — schema 在 v0.1.0 凍結前的調整成本為線性，過 tag 後為指數，現在投資是正確的時機。

---

## 4. 鎖定的最終設計

### 4.1 Per-table schema delta

| 表 | 新增欄位 | 改名 | 新增 index |
|---|---|---|---|
| `entries` | `occurred_at TEXT NOT NULL`、`actor TEXT NULL`、`reason TEXT NULL`、`source_event_hash TEXT NULL`、`version INTEGER NOT NULL DEFAULT 1` | `created_at` → `ingested_at` | `(logical_id, occurred_at DESC, ingested_at DESC)`、`(type, occurred_at DESC) WHERE is_current=1`、`(logical_id, source_event_hash) WHERE source_event_hash IS NOT NULL` |
| `edges` | `occurred_at TEXT NOT NULL`、`actor TEXT NULL`、`reason TEXT NULL` | `created_at` → `ingested_at` | `(from_id, occurred_at DESC) WHERE is_current=1`、`(to_id, occurred_at DESC) WHERE is_current=1` |
| `segments` | `occurred_at_min TEXT NULL`、`occurred_at_max TEXT NULL` | `created_at` → `ingested_at` | `(repo_id, occurred_at_max DESC)` |
| `segment_raw` | （無） | `created_at` → `ingested_at` | （無） |
| `raw_commits` | （無 — `author_time` 即 occurred_at 語意） | `created_at` → `ingested_at` | 既有 |
| `raw_changes` | （無） | `created_at` → `ingested_at` | 既有 |
| `sync_state` | `occurred_at TEXT NOT NULL`、`logical_id TEXT NOT NULL`、`is_current INTEGER NOT NULL DEFAULT 1`、`superseded_by TEXT NULL` | `created_at` → `ingested_at` | partial unique `(logical_id) WHERE is_current=1` |
| `source_checkpoint` | `last_success_at TEXT NULL`、`last_failure_at TEXT NULL`、`failure_reason TEXT NULL` | `created_at` → `ingested_at` | （無新）— 保 UPSERT |
| `entries_fts` | **無**（紅隊推翻 architect UNINDEXED 提議） | （N/A） | （N/A） |

**註**：
- `version` 欄位用於 Supersede 樂觀鎖（red team #1）。`Supersede(oldID, expected_version, ...)`。每次 supersede 新版本 = 舊版 + 1。
- `source_event_hash` 用於 fetched origin 重複 fetch 指紋（red team #2）。manual / local origin 留 NULL。

### 4.2 Migration sequence（純 additive，每檔可獨立通過 CI）

| 檔 | 內容 | 風險 |
|---|---|---|
| `0008_add_bitemporal_columns.sql` | 全表 ADD COLUMN `ingested_at` / `occurred_at` / `actor` / `reason` / `version` / `source_event_hash` / `last_success_at` / `last_failure_at` / `failure_reason` — 全 nullable | 低 |
| `0009_backfill_bitemporal.sql` | UPDATE: `ingested_at = created_at`、entries/edges `occurred_at = created_at`、segments `occurred_at_min = (SELECT MIN(rc.author_time) FROM raw_commits rc JOIN segment_raw sr ... )`、`version = 1` | 中 — 大 UPDATE |
| `0010_drop_created_at_add_indexes.sql` | DROP COLUMN `created_at`（modernc.org/sqlite 支援 ≥3.35）、SET NOT NULL 透過重建表 pattern、加所有新 partial indexes | 中 — 表重建 |
| `0011_sync_state_append_only.sql` | ALTER sync_state，加 `logical_id` / `is_current` / `superseded_by`；UPDATE 既有 row 設 logical_id = id；加 partial unique | 低 |

**dev DB 處理**：每個 commit 自動跑 migration，dogfooding 連續不中斷。FTS5 entries_fts 不變（紅隊推翻 mirror occurred_at）。

**v0.1.0 tag 前**：四檔可選擇 squash 成單一 migration 0008 給首批使用者乾淨檔案 — 留決策到 tag 前再評。

### 4.3 API shape（紅隊版：欄位放 domain.Entry）

```go
// domain/types.go
type Entry struct {
    ID                string
    LogicalID         string
    Type              EntryType
    Title             string
    Body              string
    Source            Source
    SourceRef         string
    SourceEventHash   string    // NEW: fetched 指紋；manual/local 留空
    Origin            Origin
    RepoID            string
    Author            string
    Actor             string    // NEW: 操作者；optional
    Reason            string    // NEW: 原因；optional
    Status            Status
    Version           int       // NEW: 樂觀鎖；新版 = 舊版 + 1
    IsCurrent         bool
    SupersededBy      string
    Metadata          string
    OccurredAt        time.Time // NEW: 事件時間
    IngestedAt        time.Time // RENAMED from CreatedAt
    UpdatedAt         time.Time
}

type Edge struct {
    ID           string
    FromID       string
    ToID         string
    Relation     Relation
    IsCurrent    bool
    SupersededBy string
    Actor        string    // NEW
    Reason       string    // NEW
    Metadata     string
    OccurredAt   time.Time // NEW
    IngestedAt   time.Time // RENAMED
}
```

服務簽名不變（紅隊推翻 variadic opts）：

```go
InsertEntry(ctx, e domain.Entry) (domain.Entry, error)
Supersede(ctx, oldID string, expectedVersion int, replacement domain.Entry) (domain.Entry, error)  // expected_version 樂觀鎖
```

### 4.4 Validator 規則

`validateEntry` 新增：

- `IfOccurredAt.IsZero() && Origin == OriginFetched` → 走 OccurredAtCandidate 解析（見 4.5）；若無 candidate 則設 `quality_degraded=true` 並 fallback 到 `IngestedAt`，不 reject
- `OccurredAt.After(now.Add(24*time.Hour))` → log warning event（不 reject）— ClickUp due_date 合法
- 不再有 `now + 5min` 容忍區間
- `Version <= 0` → reject（內部 bug，呼叫 InsertEntry 不該帶 version）

`validateEdge` 新增：

- `OccurredAt.IsZero()` → fallback 到 `IngestedAt`，不 reject（內部關聯沒有 source 時間）

### 4.5 Source → occurred_at 映射（紅隊版：Candidate + Primary）

```go
// ports/source/types.go
type OccurredAtCandidate struct {
    Time   time.Time
    Field  string  // 例如 "author_time" / "ts" / "date_created"
    Reason string  // 為何此候選；debug 用
}

type PullResult struct {
    Entries []domain.Entry
    // 每個 entry 對應的候選時間集合 — 由 Source 提供，RepositoryService 裁決
    OccurredAtCandidates map[string][]OccurredAtCandidate  // key = entry.ID
    PrimaryOccurredAt    map[string]string                  // key = entry.ID, value = candidate.Field
}
```

| Source | Candidates | Primary | 備註 |
|---|---|---|---|
| **git** | `author_time` / `commit_time` | `author_time` | rebase 時 commit_time 重置，author_time 較穩 |
| **manual (CLI)** | （無 — CLI 給 `--at` 才有；否則 OccurredAt = IngestedAt = now） | — | 允許 backdate 任意過去 |
| **Slack** (Phase 2) | message `ts` / `thread_ts` | `ts` | thread reply 用自己 ts |
| **ClickUp** (Phase 2) | `date_created` / `date_updated` / `date_done` / `date_closed` | 按同步事件挑 | 狀態事件 → date_done；新建 → date_created |
| **GitHub** (Phase 2) | issue/PR `created_at` / `updated_at` / `closed_at` / `merged_at` | 按同步事件挑 | 多事件物件，每事件對映自己時間 |

**RepositoryService 解析規則**：
1. 找 `PrimaryOccurredAt[entry.ID]` 指向的 candidate → 用該時間
2. Primary 不存在或對應 candidate 為 zero → 用第一個非 zero candidate + 記錄 `quality=degraded`
3. 全 zero → fallback `IngestedAt` + `quality=degraded` + warning event

### 4.6 sync_state / source_checkpoint 分別處理

**`source_checkpoint`** — 維持 UPSERT（紅隊修正）：
- `last_success_at`：最近成功 fetch 的 wall clock 時間
- `last_failure_at`：最近失敗 fetch 的時間
- `failure_reason`：失敗原因（給 user 看的訊息）
- cursor blob 仍維持 latest-only（無歷史）
- 真實需求 = resume，不是 reflog

**`sync_state`** — 改 append-only + supersede（架構保留）：
- 加 `logical_id` / `is_current` / `superseded_by` / `occurred_at`
- 同步路徑：找現有 live row → 若 hash 不同則插新 row + 翻舊 row `is_current=0`；hash 同則 noop（idempotent）
- partial unique `(logical_id) WHERE is_current=1`
- 啟動時 prune 30 天前 non-current row（無 background worker，紅隊主張）

### 4.7 Query 層 API

```go
// repository/queries_temporal.go
func (s *Service) ListEntriesAt(ctx, asOf time.Time, filter EntryFilter) ([]Entry, error)
func (s *Service) EntryHistory(ctx, logicalID string) ([]Entry, error)  // 全版本，occurred_at DESC
func (s *Service) GoalActivitiesAt(ctx, goalLogicalID string, asOf time.Time) ([]Entry, error)
func (s *Service) EdgesAt(ctx, asOf time.Time, opts EdgeFilter) ([]Edge, error)
```

核心 SQL pattern（不建 snapshot table，純走 supersede chain）：

```sql
-- ListEntriesAt: 取每個 logical_id 在 asOf 當下的 current 版本
SELECT e.* FROM entries e
WHERE e.ingested_at <= :asOf
  AND e.occurred_at <= :asOf
  AND (
    e.superseded_by IS NULL
    OR EXISTS (
      SELECT 1 FROM entries s
      WHERE s.id = e.superseded_by AND s.ingested_at > :asOf
    )
  )
  AND <user filter>;

-- EntryHistory: 整條版本鏈
SELECT * FROM entries WHERE logical_id = ? ORDER BY version DESC;
```

**Index 覆蓋**：
- `EntryHistory` ← `(logical_id, occurred_at DESC, ingested_at DESC)` 涵蓋
- `ListEntriesAt` ← 主表掃 + `idx_entries_superseded`（既有 partial index）
- Phase 1 資料量（< 10k entries）full scan 可接受；> 100k 再評估 materialize snapshot table

### 4.8 CLI surface

```
workingbad list --at <RFC3339|relative>   # 時間旅行列表
workingbad history <logical-id>            # 整條版本鏈 + diff
workingbad note --at <RFC3339> ...         # backdate 建立
```

`workingbad diff <t1> <t2>` 留 Slice B 後（需要 entry diff 渲染，CLI 體驗弱）。

### 4.9 並發 & 冪等

- **樂觀鎖**：`Supersede(oldID, expectedVersion, replacement)` — 若 oldID 的 current row `version != expectedVersion` 則 reject 並回傳 `ErrVersionConflict`，caller 重讀重試
- **指紋冪等**：`InsertEntry` 對 OriginFetched 帶 `source_event_hash` 必填；若 `(logical_id, source_event_hash)` 已存在則 noop（partial unique index 防）

### 4.10 deep supersede chain（觀察 + 延後）

- 不在本 slice 實作 compact
- 加 metric/log：supersede 時若 chain 深度 > 50 則 warn
- 未來 v0.2 視 dogfood 數據決定是否引入 compact-to-snapshot

### 4.11 Timezone

- DB 一律存 RFC3339Nano UTC（既有約束維持）
- 顯示層的 local timezone 是 Slice B Web UI 範疇，CLI 預設 UTC 顯示 + `--tz local` flag

### 4.12 Monotonic clock

- 不在本 slice 引入
- 寫入順序仍以 wall clock UTC 為 source of truth
- 若 NTP 校正引發排序問題，將以「supersede chain via version + logical_id 線性掃」做 fallback ordering（version 為單調遞增整數，不受 wall clock 影響）

### 4.13 EXPLAIN QUERY PLAN golden test（紅隊 #6）

CI 加新測：`go test ./internal/repository -run TestQueryPlan_BitemporalReads`，固定 6 個查詢的 query plan，發現變動則 fail。防止 index 退化。

---

## 5. PR / commit 序（紅隊版：single branch + commits + squash）

Branch: `feature/bitemporal-time-travel`

| # | Commit | 範圍 | 自包含可 bisect？ |
|---|---|---|---|
| 1 | `fix(repo): make formatRFC reject zero time, parseRFC propagate errors` | conv.go 兩 P0 silent corruption | ✅ |
| 2 | `feat(schema): migration 0008 add bitemporal columns (nullable)` | 0008 + sqlc regen | ✅ |
| 3 | `feat(schema): migration 0009 backfill bitemporal from created_at` | 0009 | ✅ |
| 4 | `feat(schema): migration 0010 drop created_at, add indexes` | 0010 + 既有 SQL/sqlc 全 rename | ✅ |
| 5 | `feat(schema): migration 0011 sync_state append-only` | 0011 | ✅ |
| 6 | `feat(domain): rename CreatedAt→IngestedAt, add OccurredAt/Actor/Reason/Version/SourceEventHash` | domain/types.go + 全 callers | ✅ |
| 7 | `feat(repo): supersede inherits occurred_at; rePoint preserves edge occurred_at` | edges.go + pipeline.go | ✅（含 regression test） |
| 8 | `feat(repo): materializeOne pulls author_time from raw_commits into entry.OccurredAt` | pipeline.go + queries.sql | ✅ |
| 9 | `feat(source): introduce OccurredAtCandidate / Primary on PullResult` | ports/source/types.go + mock adapter | ✅ |
| 10 | `feat(repo): validateEntry uses candidates; warn at >24h future; quality=degraded fallback` | service.go | ✅ |
| 11 | `feat(repo): segments occurred_at_min/max populated at UpsertSegment` | pipeline.go | ✅ |
| 12 | `feat(repo): source_checkpoint last_success_at/last_failure_at/failure_reason` | service.go | ✅ |
| 13 | `feat(repo): sync_state append-only flow + startup prune` | service.go + db.go | ✅ |
| 14 | `feat(repo): Supersede expected_version optimistic lock` | service.go + edges.go | ✅ |
| 15 | `feat(repo): source_event_hash idempotency on fetched insert` | service.go | ✅ |
| 16 | `feat(repo): ListEntriesAt / EntryHistory / GoalActivitiesAt / EdgesAt + tests` | queries_temporal.go | ✅ |
| 17 | `feat(cli): list --at, history subcommands` | cmd/workingbad/commands.go | ✅ |
| 18 | `test(integration): scripted time-travel journey + EXPLAIN QUERY PLAN golden` | main_test.go + new file | ✅ |
| 19 | `docs(decision): write Bitemporal Adoption as decision Entry` | dogfooding | ✅ |

Squash policy：v0.1.0 tag 前再評是否 squash 為單一 commit；目前以可 bisect 為優先。

---

## 6. 接受標準（Slice A.5 Exit）

- [ ] 全 19 commit 通過 CI（build / vet / test / lint）
- [ ] `workingbad list --at <T>` 可重現任一時刻的 entries 狀態，含 supersede 過的 entry
- [ ] `workingbad history <logical-id>` 列整條版本鏈 + 每版 occurred_at / actor / reason
- [ ] `EdgesAt(T)` 不顯示 T 時刻尚未存在或已 supersede 的 edges
- [ ] 從 mock git source 拉一段 commits，產生的 activity entries 帶正確 occurred_at（= 該 segment 最早 author_time）
- [ ] re-summarize 後新版 activity 的 `occurred_at` 維持與舊版相同（不漂移到 re-summarize 時間）
- [ ] supersede 並發測試：兩個 caller 同時用相同 expectedVersion，後者收到 `ErrVersionConflict`
- [ ] fetched 重複 fetch 同一 source_event_hash → noop
- [ ] EXPLAIN QUERY PLAN golden 測試覆蓋 6 個 at-time 查詢
- [ ] 本決策本身寫成 `decision` Entry 進 truth source

---

## 7. 明確排除（本 slice 不做）

| 項目 | 為何不做 | 何時重評 |
|---|---|---|
| FTS5 entries_fts mirror occurred_at | UNINDEXED 不能 WHERE 過濾，as-of FTS 可 JOIN 主表 | > 100k entries 或熱路徑數據 |
| snapshot table（valid_at, current_id）| supersede chain + index 等價，N×N 雙寫無謂 | > 100k entries 且 at-time query 是熱路徑 |
| Event sourcing 全套 | Phase 2 加 Sinks 才需要 event 流式 | Phase 2 |
| Commit DAG / branch / merge（Dolt-style）| single-writer 不需要 | Phase 4+ 多人模式 |
| Background prune worker | startup prune 足夠，無 race | 觀察到 SQLite 膨脹 > 100MB |
| Monotonic clock 排序 | wall clock 排序對 single-user 足夠；version 欄位提供 fallback | 觀察到 NTP 校正引發排序錯亂 |
| Deep chain compact | chain 深度監控 + 警告，先觀察 | > 50 層 chain 出現於 dogfooding |
| Web UI 雙時間呈現 | Slice B 範疇 | Slice B |
| Cross-machine snapshot / audit | 違反「本地」定位 | 永不 |

---

## 8. 風險登記

| ID | 風險 | 緩解 | 監控 |
|---|---|---|---|
| R1 | 19 commit 中某個讓 dogfooding DB 進入半改名狀態 | 每 commit 自包含、CI 全綠才 merge；4-5 commit 為分水嶺（DB rename 完成） | dogfood 期間斷掉 = 立刻回滾 |
| R2 | 紅隊 silent failure 修不淨（formatRFC / parseRFC）導致 occurred_at 被污染 | commit #1 先修；加單元測試覆蓋 zero time / malformed time | 測試覆蓋率 |
| R3 | materializeOne 改動破壞 Slice A 既有 idempotency | commit #8 先寫 regression test 再改 code | 既有 pipeline 測試全綠 |
| R4 | optimistic lock 在 single-process single-writer 場景過度設計 | Phase 2 多 process 同步前不會真實受益；但結構就位後 future-proof | 觀察 ErrVersionConflict 頻率 |
| R5 | 19 commit 工期估計過於樂觀，拖到 v0.1.0 tag 時程 | 中段（commit #10 後）做一次 mid-slice 自評；必要時砍 #16-#17 延後 | 兩週節點 checkpoint |
| R6 | 紅隊「現在不該做 bitemporal」的擔憂成真：dogfood 後發現雙時間軸沒人用 | 接受 — schema 加欄位後 NULL 為 default，code 不強制使用即可不擾 | dogfood 一個月後評估 |

---

## 9. References

### Agent reports
- search-specialist（Round 0）: prior art 6 條 — bitemporal, SCD2, event sourcing, history table, Dolt, XTDB
- architect（Round 1A）: 正向設計 8 章節
- silent-failure-hunter（Round 1B）: 8 條 P0-P2 既有 code 失敗模式
- architect red-team（Round 2）: 8 議題逐攻擊 + 7 根本問題

### 影響檔案
- `internal/domain/types.go` — Entry / Edge / Segment / RawCommit 結構
- `internal/repository/conv.go` — formatRFC / parseRFC 兩 P0 silent corruption
- `internal/repository/service.go` — InsertEntry / Supersede / validateEntry / stampTimes
- `internal/repository/pipeline.go` — materializeOne / UpsertRaw / UpsertSegment / supersedeEntryInTx
- `internal/repository/edges.go` — rePointAllLiveEdges / 邊 occurred_at 繼承
- `internal/repository/queries.go` — ListEntries / 新增 queries_temporal.go
- `internal/repository/queries/*.sql` — sqlc 來源
- `internal/repository/migrations/0008..0011*.sql` — 新加
- `cmd/workingbad/commands.go` — list / history subcommand

### 跨參考
- [ROADMAP.md](../ROADMAP.md) Slice A.5
- [docs/grill/2026-06-07-auto-grill.md](2026-06-07-auto-grill.md) 14 輪初始設計
- [docs/checkpoints/2026-06-08-post-slice-a-review.md](../checkpoints/2026-06-08-post-slice-a-review.md) Slice A 後多 agent review
- Prior art 連結（search-specialist 報告）：bitemporal modeling、SQL:2011 temporal、SQLite temporal、event sourcing 等

---

## 10. 後續

1. 立刻：開 `feature/bitemporal-time-travel` branch，按 19 commit 序執行
2. commit #4 完成（DB rename 收斂）後做一次 mid-slice 自評
3. Slice A.5 結束 → Slice B Web UI（吃 at-time query 為列表來源）
4. v0.1.0 tag 前評估是否 squash 0008-0011 migration

—— 結
