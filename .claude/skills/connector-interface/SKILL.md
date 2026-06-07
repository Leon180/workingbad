---
name: connector-interface
description: Contracts for the three extension seams — Source (input), Sink (output/sync), AIProvider — plus the repository service (sole write gate). Use when adding a new integration (git, GitHub, Slack, ClickUp, Claude), building the sync engine, or wiring AI. Keeps the core decoupled and the product extensible.
origin: workingbad
---

# Connector Interfaces

擴展只透過三個 interface,核心引擎不認得具體整合;**所有寫入必須經 repository service**(唯一寫入閘門,truth-source-schema 不變量 10)。
所有實作須遵守 `truth-source-schema` 的不變量。決策過程見
[docs/grill/2026-06-07-auto-grill.md](../../../docs/grill/2026-06-07-auto-grill.md)。

## Source(輸入)

```go
type Source interface {
    Name() string
    Capability() Capability                                       // fetch_fields/push_fields 程式碼常數
    // Pull = at-least-once + 冪等收斂;checkpoint advance 與資料寫入分離,崩潰=安全重放。
    Pull(ctx context.Context, cur Checkpoint) ([]Entry, Checkpoint, error)
    Watch(ctx context.Context, out chan<- Entry) error            // 可回 ErrUnsupported → Scheduler 降級成輪詢
}

type Checkpoint []byte                                            // opaque blob;Source 自己解讀
```

### local-git source(Phase 1 第一個 Source)

- **fetch-only**(structural;git 不會是 Sink)。
- **repo_id 生成**:remote URL 正規化 hash(主)→ 初始 commit sha(輔,無 remote 時)→ **禁路徑名**(易變)。
- **分窗契約(免費規則,Source 層,不進 AIProvider)**:
  1. 按 `(repo_id, branch_name)` 分桶
  2. 依 author-time 排序(時鐘漂移視同段)
  3. `gap > GAP_THRESHOLD(90min)` 或段內達 `MAX_COMMITS(200)` 封段
  4. anchor = 段內 author-time 最早**且 patch_id 非空**的 change(merge 跳過)
  5. `source_ref = encode(repo_id, branch_name, anchor_patch_id)`;**禁 window_seq**
  6. 門檻寫進 segment metadata、不進 source_ref(改門檻無 migration)
- **寫 raw**:`INSERT ... ON CONFLICT(repo_id, sha) DO NOTHING`(顯式,避免裸 IGNORE 靜默吞約束)。
- **rewrite**(amend/rebase):find-or-create `change_id` + 翻 `is_current`,同 tx;`segment_raw` 指 `change_id` 不動,segments stale 由 ingest 翻。
- patch-id 自寫(`go-git` 無原生):Phase 0 半天 spike(`--stable` 對齊;amend/rebase/reorder 三案測試)。

## Sink(輸出 / 同步)

```go
type Sink interface {
    Name() string
    Capability() Capability
    Sync(ctx context.Context, batch SyncBatch) error              // upsert-by-external_ref,非 append
}

type SyncBatch struct{ Items []SyncItem }                          // {subject_kind, subject_ref, payload, hash}

type SyncPolicy interface {
    Select(entries []Entry) []Entry                                // 過濾
    Render(e Entry) (msg Message, hash string)                     // 映射 + 算 hash(push_fields 投影)
}
```

**Direction policy(消滅迴圈核心)**:每個 connector 用 `Capability()` 宣告 fetch/push_fields。
**registry wiring 階段**(非 validator)驗:
- `fetch_fields ∩ push_fields = ∅`(disjoint sets;echo 結構上不可能)
- `selected ⊆ capability`(config 選的子集要在 connector 能力宣告範圍內)

**冪等(sync_state)**:
- 鍵掛**跨 supersede 穩定的 subject**:git → segment `source_ref`;entry → `logical_id`。**不是 `entry_id`**(supersede 換 id 會雙貼)。
- `last_synced_hash` 算在 **Render 後 push_fields 投影**(Phase 1 = title + body;**排除** edges/origin/id/timestamp)。
- 邏輯:lookup `external_ref` → 同 hash skip / 不同 update / 無 create。
- **edge 同步延 Phase 2**(獨立 `sync_edge_state` additive)。

**第一個實作**:`slack`(Phase 2,Slack API 直連)。`clickup` = 雙向(disjoint sets:fetch ticket no/desc/規格;push activity 進度)。

## AIProvider(必要能力,setup 擇一 local 或 api,無 fallback)

```go
type AIProvider interface {
    Classify(ctx context.Context, content string) (EntryType, float64, error)  // segment 級彙整文字
    Summarize(ctx context.Context, changes []RawChange) (title, body string, err error)
    Relate(ctx context.Context, e Entry, candidates []Entry) ([]Edge, error)
}
```

能力切分(成本/職責):
- **Classify** = LLM 依內容,輸入粒度釘 **segment 級彙整文字**(不是單一 raw commit)。**Phase 1 git 線短路**(mock 回固定 `(activity, 1.0)`);多型延 Phase 3/4。
- **分組** = branch 規則,免費,**不進 AIProvider**(只決定哪些 commit 合一,不決定型別)。
- **Summarize** = 必要;**建議 lazy**:`push-preview` 觸發 `batchMaterialize`,日常瀏覽不花 LLM。
- **Relate** = 預設 **embedding 餘弦相似度**(便宜、低幻覺);強關係且使用者要求才升 LLM,設 precision 門檻。

### mock 契約(Phase 1)
- 決定性純函式:輸入 = is_current raw 投影 + anchor_patch_id;輸出 title/body 穩定可重現。
- 可注入 hook:`WithSummarizeFunc / FailOnSegment / FailAfterN`(失敗注入)。
- **call-counter**:把「1 segment ≤1 Summarize 呼叫」變 CI 紅綠線(防 lazy 退化成 eager)。
- Classify mock 維持固定 `(activity, 1.0)`,git 線不呼叫。

成本/品質量測延後到 `product-ai-eval`(自寫,eval-harness 量錯對象)。

## Repository service(唯一寫入閘門,不變量 10)

所有寫入經此 service;CLI / HTTP / sync 都是 thin adapter,**不准繞過**。

```go
type RepositoryService interface {
    // 寫入(append-only + supersede 路徑)
    InsertEntry(ctx, e Entry) error                              // **per-type validator** choke point
    EditEntry(ctx, id string, patch Patch) error                 // manual: COALESCE_WINDOW(5min) 內+未引用/未 push → 原地;否則 supersede
    SetGoalStatus(ctx, goalID, status) error                     // supersede;狀態機驗 enum
    AttachToGoal(ctx, activityID, goalID) error                  // edges append-only
    DetachFromGoal(ctx, edgeID) error

    // ingest / materialize 熱路徑
    UpsertRaw(ctx, c RawCommit) (RawChange, error)               // sha 冪等 + change_id find-or-create + 翻 is_current(同 tx)
    UpsertSegment(ctx, s Segment) error                          // (repo_id, source, source_ref) 冪等
    BatchMaterialize(ctx, scope Scope) error                     // **逐段獨立 tx**;失敗保持 pending/stale,不加 failed 持久態;呼叫 AIProvider.Summarize
    SyncSubjectRef(ctx, sink, kind, ref, ext, hash) error        // sync_state upsert

    // 唯讀
    ListEntries(ctx, filter Filter) ([]Entry, error)             // WHERE is_current=1
    CountPendingSegments(ctx) (int, error)                       // 免 LLM 計數提示列
    GetGoalActivities(ctx, goalID) ([]Entry, error)              // 沿 iteration_of 遞迴聚合,扁平輸出
}
```

**per-type validator**(程式碼內 `fieldContract map`,否決 DB 表):
- goal 帶 status / 其他 type 強制 status=NULL
- manual `source_ref` 必為內容 hash(非空)
- 必填欄位(title 等)按 type 條件強制

**BatchMaterialize 行為**:
- 觸發點:`push-preview` + `workingbad summarize`(手動)。
- 邏輯:`pending|stale` segments 逐段獨立 tx,同 tx 內呼叫 `Summarize` → `InsertEntry` → 翻 segment.summary_state=materialized → 連動 attach/re-point edges → 寫 FTS5。
- 任一段失敗回報 partial-result(不靜默吞);**不加 failed 持久態**(下次重試)。

**push-preview 兩階段嚴格串行**:`materialize-all` barrier → `render-for-push`(SELECT `summary_state='materialized' AND is_current=1`)→ Sink.Sync。**不共 tx**(API 邊界 Phase 1 就對)。

## Config + registry 驗證

- **形狀**:`ai` / `db` 單例 discriminated union(`ai: { kind: local|api, ... }`)+ `sources` / `sinks` 列表。
- **repo_id** 不寫 config;啟動由 local-git 算 remote URL hash。
- **secrets** 只存 `*_env` 環境變數名(禁明文;keychain 延 Phase 2 additive)。
  Go 端 `Secret` 型別 `String()` / `MarshalYAML` / `MarshalLog` 一律回 `[REDACTED]` + 守門測試。
- **兩段驗證**:
  - koanf v2 + go-playground/validator v10(語法 / oneof / dive)
  - **registry wiring 階段**:`∩=∅` + `selected ⊆ capability`(看程式碼常數,validator 做不到)→ 失敗 fatal abort。

## 註冊 / 新增 connector checklist
1. 宣告 `Capability` 程式碼常數(fetch_fields / push_fields)
2. 實作 interface、註冊 factory
3. config 範例 + `*_env` secrets
4. 測試:table-driven + disjoint sets fixture(故意造重疊 fixture 驗證 wiring 拒絕)
5. 更新本 skill
