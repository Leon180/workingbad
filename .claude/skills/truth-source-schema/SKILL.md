---
name: truth-source-schema
description: Canonical Entry/Edge data model for the truth source. Use whenever designing storage, writing a connector that produces entries, building the Web UI, or adding a new task type. Ensures every source writes consistent, queryable records.
origin: workingbad
---

# Truth Source Schema

衍生層原子單位是 **Entry**,關係用 **Edge**,工作階段生命週期載體是 **Segment**,git 事實在 **raw 雙表**。
**任何寫入必須經 repository service**(不變量 10)。完整 DDL/決策過程見
[docs/grill/2026-06-07-auto-grill.md](../../../docs/grill/2026-06-07-auto-grill.md)(回合 1/3/5/6/7/10/12)。

## 表清單

| 表 | 角色 | PK |
|---|---|---|
| `entries` | 衍生層 materialized(activity/research/discuss/decision/goal) | id (uuid v7) |
| `edges` | 關係圖,append-only + supersede | id (uuid v7) |
| `segments` | 工作階段生命週期載體;**冪等鍵權威所在** | id (uuid v7) |
| `segment_raw` | segments ↔ raw_changes 多對多(指 change_id) | (segment_id, change_id) |
| `raw_commits` | git commit 完整內容(自存,不依賴 git) | `sha` |
| `raw_changes` | rewrite 透明的邏輯變更 + patch-id 鏈 | `change_id` (uuid v7) |
| `sync_state` | Sink 冪等(跨 supersede 穩定的 subject) | id (uuid v7) |
| `source_checkpoint` | Source 增量 cursor(**唯一可變表,豁免不變量 1**) | (repo_id, source) |
| `entries_fts` | FTS5 own-content/contentless 鏡射 `is_current=1` | virtual |

## Entry 欄位

| 欄位 | 型別 | 說明 |
|---|---|---|
| `id` | uuid v7 字串 | 全系統 uuid v7(raw_commits sha / raw_changes change_id 例外) |
| `logical_id` | uuid v7 字串 | **跨編輯穩定身分**:create=自身 id;supersede 沿用 |
| `type` | enum | activity \| research \| discuss \| decision \| goal |
| `title` / `body` | string / text | |
| `source` | enum | git \| github \| slack \| clickup \| claude \| manual |
| `source_ref` | string | **僅 create 去重**;身分鍵權威在 segments / logical_id |
| `origin` | enum | fetched(唯讀) \| pushed \| local |
| `repo_id` | string \| NULL | **隔離鍵**:git=remote URL 正規化 hash;manual / 非 git fetched = NULL |
| `author` | string | |
| `status` | enum \| NULL | open/in_progress/done/archived;**validator 強制非 goal 型 NULL** |
| `is_current` | bool | append-only + supersede 當前版指標 |
| `superseded_by` | uuid v7 \| NULL | 被誰取代 |
| `metadata` | json | 不污染核心欄位 |
| `created_at` / `updated_at` | time | UTC |

### type 語意
- `activity` — **一個工作階段的人類語言記錄**(≈ PR description),由模型 `Summarize` 從一段 raw 合成。與 git commit 是不同 domain;1 或 N commits → 1 activity;**不是每個 commit 都變 activity**(可被判 research/其他)。
- `research` — 調查/探索筆記(manual / claude session,或某些 commit 被判於此)。
- `discuss` — 討論串/對話(slack)。
- `decision` — 拍板的決策 + 理由(本專案決策吃自己狗糧寫這)。
- `goal` — 任務的聚合域;**可變、可遞迴**(`iteration_of` 疊更高層 goal=迭代歷史)。可變以 **append-only supersede** 實現。來源:fetched(標來源) / manual(標手動);manual 不自動 push。

### 兩層 + 流向
```
raw 層
 ├ raw_commits(PK=sha, 自存完整內容, 不依賴 git gc)
 └ raw_changes(PK=change_id uuid v7, partial unique (repo_id, patch_id) WHERE NOT NULL)
     ▲ amend/rebase = find-or-create change_id + 翻 is_current(同 tx);patch_id NULL → 獨立節點
     ▲ merge commit 入庫但 patch_id=NULL、**不可當 anchor**

  segment_raw 指 change_id(非 sha)→ rewrite 透明、stale 單跳命中
       ▼
segments  ((repo_id, source, source_ref))  ← 冪等鍵權威
 ├ source_ref = encode(repo_id, branch_name, anchor_patch_id)
 ├ summary_state ∈ {pending, materialized, stale}
 ├ anchor = 段內 author-time 最早且 patch_id 非空的 change
 ├ 切窗規則(免費,Source 層,不進 AIProvider):
 │   分桶 (repo_id, branch) → author-time 排序 → gap>GAP_THRESHOLD 或達 MAX_COMMITS 封段
 │   **GAP_THRESHOLD=90min, MAX_COMMITS=200**(config 化、寫進 segment metadata、不進 source_ref)
 │   **禁 window_seq**(歷史插入會位移炸冪等)
 └ pending 不進 entries(只計數提示)
       ▼
衍生 entries(materialized,出生即帶完整 title/body)
 ├ Phase 1 git 線恆 type=activity;Classify mock 固定 (activity, 1.0);多型延 Phase 3/4
 ├ re-Summarize = append-only supersede(舊 is_current=0,新版新 id,part_of 連動 re-point)
 └ FTS5 only 鏡射 is_current=1
```

## Edge

| 欄位 | 說明 |
|---|---|
| `id` | uuid v7 |
| `from_id` / `to_id` | Entry id |
| `relation` | relates_to \| derived_from \| blocks \| part_of \| iteration_of |
| `is_current` / `superseded_by` | append-only + supersede |
| `created_at` | UTC |
| `metadata` | json |

- **partial unique** `(from_id, to_id, relation) WHERE is_current=1` → 重複 attach no-op;detach 標 superseded 不刪。
- **Summarizer 同 tx 自動 re-point**:supersede activity 時把舊版的 part_of / relates_to 複製指向新版、舊 edge superseded;讀路徑恆 `WHERE is_current=1`。
- `part_of` 永遠指當時的 goal 版本;iteration 不自動 re-point;查詢沿 `iteration_of` 遞迴聚合。
- `derived_from` 也用於衍生 entry 連回 raw。

## sync_state

| 欄位 | 說明 |
|---|---|
| `id` | uuid v7 |
| `sink` | string |
| `subject_kind` | enum:**git=segment source_ref / entry=logical_id**(跨 supersede 穩定) |
| `subject_ref` | string |
| `external_ref` | string |
| `last_synced_hash` | string(Render 後 **push_fields 投影**的 hash,Phase 1=title+body) |
| `last_synced_entry_id` | uuid v7 |
| `synced_at` | UTC |

`UNIQUE(sink, subject_kind, subject_ref)`;`Sink.Sync = upsert-by-external_ref`(create/update/skip-by-hash),非 append。

## source_checkpoint(唯一可變表,豁免不變量 1)

| 欄位 | 說明 |
|---|---|
| `repo_id` / `source` | 複合 PK |
| `cursor` | blob,opaque,per-branch;Source 自己解讀 |
| `updated_at` | UTC |

- cursor 是 **advisory hint**;正確性錨在 raw sha unique + patch_id 鏈。
- advance = **原地 UPDATE**(僅此一表合法)。
- raw/segment 寫入與 checkpoint advance **分離**:at-least-once + 冪等收斂;崩潰=安全重放。
- checkpoint 只追 ingest 進度,與 materialize **正交**(materialize 失敗不回退)。

## 不變量(invariants)

1. **Immutable 更新 + append-only supersede**:改 Entry/Edge/segment/raw 旗標 = 新 row 或翻 is_current/superseded_by。**唯一例外:`source_checkpoint`**(advance 原地 UPDATE)。
2. **冪等寫入**:`raw_commits(sha)` / `raw_changes((repo_id,patch_id) partial unique WHERE NOT NULL)` / `segments((repo_id,source,source_ref))` / `sync_state((sink,subject_kind,subject_ref))`。entries 不再有 (source,source_ref) unique,改軟約束見不變量 9。
3. **fetch / push 集合不可重疊**:registry wiring 階段驗 fetch_fields ∩ push_fields = ∅。
4. **fetched 資料唯讀**(UI 與 repository 強制)。
5. **type 封閉**:新增 type 需同步本 skill + Classify mock + UI + per-type validator。
6. **metadata 不污染核心**:會被查詢/同步依賴的欄位必須升為正式欄位。
7. **時間一律 UTC** 儲存,顯示層才轉本地。
8. **可追溯**:raw 自存完整內容,不依賴 git 保留;改寫 additive。
9. **per (segment_id, type) ≤1 live entry**:Phase 1 物理 1:1(git segment 恆 activity);措辭預鎖 1:N 形狀,Phase 4 多型 additive。
10. **單一寫入閘門**:repository service 是唯一 entry/edge writer;CLI/HTTP/sync 皆 thin adapter,**不准繞過**。
11. **ingest / Summarizer 為唯一翻旗標 writer**:`is_current`/`superseded_by` 只能在 ingest(raw)、Summarizer(衍生 entries + 連動 edges) 的 transaction 內被翻。

## FTS5

- `entries_fts` = **own-content/contentless** 虛擬表(否決 external-content)。
- 只鏡射 `is_current=1` 的 entries(搜不到歷史)。
- entries 維持 **uuid v7 PK 不退讓**;FTS 不對齊 rowid,靠 `entry_id` 文字欄。
- **同 tx 手動維護**(repository.insertEntry / supersede 路徑連動),不用 trigger。

## 測試隔離契約

- 每測 `t.TempDir()` 開**獨立 sqlite file**,跑全部真實 migration 重建 schema。
- **禁裸 `:memory:`**(modernc 多連線各自空 DB 的 flaky 地雷)。
- 至少一條測 migration 鏈本身。
- CI:`t.Parallel()` + 獨立檔。

## 預設參數(config 化、可調、無 migration)

- **GAP_THRESHOLD = 90 min**(已拍板;config 化,dogfooding 後校準)
- **MAX_COMMITS = 200**
- **COALESCE_WINDOW = 5 min**(manual 連續編輯折疊窗;設 ∞ 退回純 supersede)

## 待決(本 skill 相關)
- (無重要)Phase 2+ 會再開:cursor blob prune、relates_to background compaction、edge 全文搜尋。

相關契約見 `connector-interface`。
