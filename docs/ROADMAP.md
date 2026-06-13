# Roadmap & Iteration Mechanism

迭代機制 = **階段錨點（本檔）** + **階段審查（`project-review-checkpoint`）** + **持續學習（`continuous-learning-v2`，可選）**。
每完成一個 Phase，跑一次 checkpoint，把結論寫進 `docs/checkpoints/`，再決定下一步。

## Phases

### Phase 0 — Foundations（current）
- [ ] Go module + 目錄結構（cmd / internal / web）；Go ≥ 1.25(`http.CrossOriginProtection`)
- [ ] `config.yaml` 載入:**koanf v2 + go-playground/validator v10**;ai/db 單例(discriminated union)+ sources/sinks 列表;`*_env` secrets;`Secret` 型別 redaction 守門測試
- [ ] SQLite 連線(**modernc.org/sqlite**,純 Go 免 cgo)+ **goose v3 + embed.FS** migration(forward-only;startup 自動;每檔單一 tx;失敗 fatal abort)
- [ ] **Spike(半天):goose + modernc + FTS5 跨平台 + temp file 測試契約 + patch-id 自寫(`--stable`,amend/rebase/reorder 三案測試)**
- [ ] CI:build / vet / test / lint;**三條 CI gate**(已 tag migration 檔不可變 / 編號連續 / version 與檔數一致)
- **Exit 標準**：`go build` 出單一 binary;啟動讀 config、跑 migration 建好 **6 表 schema**(`entries / edges / segments + segment_raw / raw_commits + raw_changes / sync_state / source_checkpoint`)+ `entries_fts`;spike 全綠。

### Phase 1 — interface + mock + 完整測試（不串外部 API）

**Slice A(CLI-first,達 dogfooding exit)**
- [ ] 鎖定 interface:`Source(Pull/Watch + Capability)` / `Sink(Sync + Capability + SyncPolicy)` / `AIProvider(Classify/Summarize/Relate)` / `RepositoryService`(唯一寫入閘門)
- [ ] repository service 完整契約:`InsertEntry`(per-type validator)/`EditEntry`(COALESCE)/`SetGoalStatus`/`AttachToGoal`/`DetachFromGoal`/`UpsertRaw`/`UpsertSegment`/`BatchMaterialize`/`SyncSubjectRef`/`ListEntries`(WHERE is_current=1)/`CountPendingSegments`/`GetGoalActivities`(沿 iteration_of 遞迴聚合)
- [ ] in-memory mock Source(local-git 分窗契約)/Sink/AIProvider(**決定性純函式 + call-counter**)
- [ ] local-git:branch 分桶 → author-time 排序 → gap(90min)/MAX(200) 切段 → anchor patch-id;ON CONFLICT(repo_id,sha) DO NOTHING;rewrite 走 change_id find-or-create + 翻 is_current
- [ ] Summarize 走 **`BatchMaterialize(scope)` 逐段獨立 tx**;材料化產孤兒 → UI 呼 attachToGoal
- [ ] supersede 同 tx 自動 re-point `part_of`/`relates_to` edges;edges partial unique `WHERE is_current=1`
- [ ] CLI:`sync / list / note / decision / goal / attach / detach / status / summarize`
- [ ] 完整測試(TDD,table-driven):不變量(immutable + supersede + per (segment,type)≤1 live)、冪等(raw sha / segments / sync_state hash skip / disjoint sets fixture / supersede 不雙貼)、pipeline 端到端(mock 全程)、re-Summarize 後 attach 不消失;**測試隔離=`t.TempDir()` 獨立檔 + 跑全 migration**

**Slice A.5(Bitemporal / Time-travel Foundations,v0.1.0 凍結前必收)**

完整 grill 記錄見 [docs/grill/2026-06-08-time-travel-schema.md](grill/2026-06-08-time-travel-schema.md)(3 輪 multi-agent:search prior art / architect 正向 / silent-failure-hunter 既有 code / architect 紅隊壓測 → 用戶拍板 (A))。

**路線**:Query-Time Versioning(用既有 supersede chain)+ bitemporal 時間拆分(`occurred_at` 事件 / `ingested_at` 落地);純 additive migration(不改既有 0001~0007);API 欄位放 `domain.Entry`(不 variadic opts);`source_checkpoint` 保 UPSERT 加 success/failure 時間,`sync_state` 改 append-only;`segments` 加 `occurred_at_min/max` 實體欄位;不動 FTS5(不加 UNINDEXED occurred_at)。

**Branch**:`feature/bitemporal-time-travel`,單一 feature branch,N commit 自包含可 bisect,squash 時機留 v0.1.0 tag 前評估。

**19 commit 序**(細節見 grill doc §5):
- [ ] (1) fix conv.go P0 silent corruption (formatRFC zero/parseRFC swallow)
- [ ] (2-5) migrations 0008(add cols)/0009(backfill)/0010(drop created_at + indexes)/0011(sync_state append-only)
- [ ] (6) domain.Entry/Edge 加 OccurredAt/IngestedAt/Actor/Reason/Version/SourceEventHash + 全 caller rename
- [ ] (7) supersede 繼承 occurred_at / rePoint 邊保留原 occurred_at (修紅隊 P1)
- [ ] (8) materializeOne 從 raw_commits.author_time 拉 occurred_at 灌入 activity entry
- [ ] (9) Source.PullResult 加 OccurredAtCandidate[] + Primary
- [ ] (10) validateEntry 走 candidates / >24h future 警告 / quality_degraded fallback
- [ ] (11) segments occurred_at_min/max 於 UpsertSegment 算入
- [ ] (12) source_checkpoint 加 last_success_at/last_failure_at/failure_reason (維持 UPSERT)
- [ ] (13) sync_state append-only 流程 + 啟動 prune 30 天前 non-current
- [ ] (14) Supersede expected_version 樂觀鎖(並發 supersede 防護)
- [ ] (15) source_event_hash fetched 重複 fetch 冪等
- [ ] (16) ListEntriesAt / EntryHistory / GoalActivitiesAt / EdgesAt + 測試
- [ ] (17) CLI:`list --at` / `history`(diff 留 Slice B 後)
- [ ] (18) integration test:time-travel journey + EXPLAIN QUERY PLAN golden(防 index 退化)
- [ ] (19) 本決策寫成 `decision` Entry(dogfooding)

**明確排除**(grill doc §7):FTS5 mirror occurred_at、snapshot table、Event Sourcing、Commit DAG、background prune、monotonic clock、deep chain compact、Web UI 雙時間呈現(留 Slice B)。

- **Exit 標準**(grill doc §6):能在 CLI 復現任一時刻 entry/edge/goal 狀態;re-summarize 後 occurred_at 不漂移;supersede 並發測試後者拿 ErrVersionConflict;EXPLAIN QUERY PLAN golden 覆蓋 6 個查詢;本決策入庫為 decision Entry。

**Slice B(Web,緊接 A.5,同 Phase 內)** — landed as PR #N (feature/slice-b-web-ui)
- [x] Web UI:列表 5 type(零 graph) + type 篩選 + 時間旅行 `?at=<RFC3339>` + 計數提示列 + 手動建表單(research/decision/goal) + goal 詳情扁平 `part_of` 列表;entry detail 頁顯示 supersede chain(bitemporal "git log" 等價)
- [x] Web 安全:**127.0.0.1 listener-level binding + Host allowlist middleware + GET/POST 動詞分離 + `http.CrossOriginProtection`**;`mutationGuard` chain seam 留 additive(single-user 假設下暫不上 CSRF token / local token auth)
- [x] CLI / HTTP 共用同一 `RepositoryService`;httptest E2E journey 覆蓋:create goal → seed segment → materialize → attach activity → status done → bitemporal 時光機驗證 v1 仍可見
- [x] 40 web tests / 150 total green;`workingbad serve` CLI subcommand;templates + static assets via `embed.FS`(單一 binary 維持)

**延後到後續 PR**(同 schema,不阻擋 v0.1.0 tag):
- entry edit form(目前只能新增,不能改;CLI 也沒這個 — Phase 1 dedupe 模型下「supersede 改」概念較複雜,延後到使用上痛了再做)
- goal 詳情頁顯示 supersede 歷史(目前只有 entry detail 有 chain table)
- `EdgesAt` Web 視圖 / `GoalActivitiesAt` 時光機(目前只有 `ListEntriesAt`)
- htmx 漸進式增強(目前是純 server-side render + form post)

- **Exit 標準**:mock 資料跑完整 pipeline 全綠;Slice A 即達 dogfooding(能用 workingbad 管它自己的 decision/goal/note);Slice B 緊接交付;**v0.1.0 tag 凍結 migration 檔**;零外部依賴。

**Slice C(Graph UI,Slice B merge 後,同 Phase 內,v0.1.0 tag 前可選)**

完整 grill 記錄見 [docs/grill/2026-06-09-graph-ui.md](grill/2026-06-09-graph-ui.md)(3 輪 multi-agent:search prior art / architect 純 SVG 可行性 / ui-ux 工作流與美學)。設計鎖路線:純 SVG + Go template + htmx,**0 JS framework**;git-style swim lane(每個 goal 一條水平 lane,類型用點色,lane 用 stroke 色);bitemporal `?at=` 直接複用 Slice B query。

**已完成 PR**:
- [x] (PR #11 base) layout package(Sugiyama 風 swim-lane,8 tests)+ `/graph` route + SVG template + dark theme;218 tests 全綠

**Follow-up issues**(各自獨立 PR,獨立可 merge):
- [ ] pan/zoom(滑鼠滾輪 + 拖拉,~30 行 vanilla JS,不算 framework)
- [ ] htmx 側欄 detail panel(click 節點 swap 詳情,不跳新頁)
- [ ] hover tooltip preview(目前只有 native SVG `<title>`)
- [ ] cross-lane edge filter toggle(預設只顯示 part_of 隱含 + blocks,其他關係 toggle)
- [ ] 搜尋 highlight(輸入關鍵字非匹配節點淡出)
- [ ] 真實時間軸刻度(目前是 occurred_at evenly spread,沒有 tick mark)
- [ ] 多 part_of 的 entry 視覺化(目前只屬一條 lane,alphabetical 取最小)
- [ ] N > 500 規模:barycenter ordering 降 edge crossing

**永不做**(grill doc §7):drag-reposition、節點內 body preview、4 種以上顏色同時主圖、3D。

### Phase 2 — Sinks & 同步
- [ ] `Sink` interface + 同步引擎（可自定義 mapping、idempotent）
- [ ] Slack sink（走 Slack API 直連）
- [ ] ClickUp sink（goal Entry → task / 進度更新，走 ClickUp REST API）
- **Exit 標準**：commit/PR → 整理 → 自動更新 Slack + ClickUp，且重送不重複。

### Phase 3 — 真實 AIProvider（local 或 api）
AI 是必要能力,無 rule fallback。把 Phase 1 mock 的 AIProvider 換成真的。
- [ ] local provider(Ollama)：activity Summarize、Relate embedding
- [ ] api provider(Claude)：同介面,走 `cost-aware-llm-pipeline` 的 budget/caching
- [ ] `product-ai-eval`(自寫 skill)：標註集 + Summarize(LLM-judge)/Relate(precision@k) 量測
- **Exit 標準**：local 或 api 任一可生成堪用 activity；品質有 eval 數據;api 模式有 budget 上限。

### Phase 4 — 擴展來源
- [ ] GitHub source（PR / issue / review）
- [ ] Claude session source（從 coding session 更新任務）
- [ ] ClickUp source（fetch ticket no/desc/規格,唯讀）。與 Phase 2 push(activity) 組成雙向,
      但 **fetch 集合 ∩ push 集合 = ∅**(disjoint sets)→ 結構上無迴圈,不需 CRDT/衝突仲裁。
- [ ] 更多 sink（Notion / 其他 tracker）

## Checkpoint 規則
- 觸發：每個 Phase 的 Exit、或每 ~10 個 commit、或重大架構決策前。
- 動作：呼叫 skill `project-review-checkpoint`，產出報告至 `docs/checkpoints/<date>-<phase>.md`。
- 重大決策一律寫成 `decision` 型 Entry 進 truth source（吃自己的狗糧）。

## 已拍板決策（dogfooding：應寫成 decision Entry）

**產品 / 定位**
- 定位:truth source = 產品本體(工程師本地工作記憶),同步是 killer feature;受益者=工程師(免手動回報 toil)。
- 整合走外部服務官方 API(Slack/ClickUp REST);MCP 只用於連 Claude 那條線與開發期。
- 產品型別 `commit` → **`activity`**(≠ git commit:模型合成的工作階段人類語言記錄,1 或 N commit;非每個 commit 都變 activity)。
- AI 必要能力,setup 擇一 local/api,**無 fallback**(只約束 Summarize 合成);**型別由 LLM 依內容判斷**;branch 僅分組(免費);Summarize 建議 **lazy(push-preview 觸發)**。
- 雙向同步用 **disjoint fetch/push sets + origin 標記** 解迴圈(非 CRDT);fetched 唯讀。
- `goal` = 任務聚合域,**可變、可遞迴**(往上疊更高 goal=迭代歷史,append-only);用 **`iteration_of`** edge。git=fetch-only。
- **push 初期=手動**:本地預覽 → 確認(可修正) → 推全部設定目標。預覽 = 內建外送同意。
- goal 來源 fetched(標來源)/ manual(標手動);manual goal 不自動 push;raw 對工程師可見;UI 為主入口。
- **Phase 1 鎖 single-user 本機假設**;CSRF token/local token auth 延後到多人需求或 Phase 2(已留 middleware seam)。

**Schema / 資料模型**
- **6 張核心表**:`entries / edges / segments + segment_raw / raw_commits + raw_changes / sync_state / source_checkpoint` + `entries_fts`。完整 DDL 見 [grill 紀錄](grill/2026-06-07-auto-grill.md)。
- 全系統 **uuid v7 字串 PK**(`raw_commits.sha`、`raw_changes.change_id` 例外)。
- entries 加 **`repo_id`**(隔離鍵)、**`logical_id`**(跨編輯穩定身分)、`status`(validator 強制非 goal=NULL)、`is_current`/`superseded_by`。
- **冪等鍵權威遷至 `segments`**(`(repo_id, source, source_ref=encode(repo_id, branch, anchor_patch_id))`);entries 改軟約束「每 (segment_id, type) ≤1 live」(不變量 9)。
- raw 雙表:`raw_commits(PK=sha)` + `raw_changes(PK=change_id, partial unique (repo_id,patch_id) WHERE NOT NULL)`;`segment_raw` 指 change_id;merge commit 入庫 patch_id=NULL 不可當 anchor。
- edges append-only + supersede;partial unique `WHERE is_current=1`;**Summarizer 同 tx 自動 re-point** part_of/relates_to。
- `sync_state` 鍵掛**跨 supersede 穩定的 subject**(`subject_kind`:git=segment source_ref / entry=logical_id);`Sink.Sync=upsert-by-external_ref`;hash 算 Render 後 push_fields 投影。
- `source_checkpoint` = **唯一可變表,豁免不變量 1**;cursor opaque blob,advisory hint;at-least-once + 冪等收斂;與 materialize 正交。
- **raw 自存完整內容**,不依賴 git;改寫 additive;**patch-id** 連版本鏈;依賴的衍生 entry 失效/重新合成。
- **Repository service = 唯一寫入閘門**(不變量 10);CLI/HTTP/sync 都是 thin adapter。
- FTS5 = own-content/contentless,只鏡射 `is_current=1`,uuid PK 不對齊 rowid,同 tx 手動維護(不用 trigger)。

**預設參數**(全 config 化,可調無 migration)
- `GAP_THRESHOLD=90min`,`COALESCE_WINDOW=5min`,`MAX_COMMITS=200`

**Pipeline**
- Phase 1 git 線恆 `type=activity`;Classify mock 固定 `(activity, 1.0)`;多型延 Phase 3/4(**D-with-seams**)。
- Source.Pull 分窗純免費規則,不進 AIProvider;Summarize 走 `BatchMaterialize(scope)` 逐段獨立 tx。

**技術棧**(已研究,純 Go 單 binary 守得住)
- Go ≥ 1.25(`http.CrossOriginProtection`)、`modernc.org/sqlite`(FTS5 內建)、`goose v3 + embed.FS`、`knadh/koanf v2 + go-playground/validator v10`、`urfave/cli v3`、`net/http + html/template + htmx`、`google/uuid` v7、`go-git`(patch-id 自寫)、`viant/sqlite-vec`(備)、`Ollama` + `anthropic-sdk-go`(Phase 3)。

**Migration 紀律**
- forward-only;**`schema-frozen` marker tag 為凍結點**(marker 前可編輯未凍結檔——含 Slice D node 手術改既有 migration;marker 後純 additive,**永不改舊檔不寫 down**)。
  - 2026-06-13 脫鉤:凍結點從 `v0.1.0` 版號 tag 改為**獨立 `schema-frozen` tag**。原因——版號改由 release-please 依 conventional commit 自動 bump,若仍綁 `v0.1.0` 會在 release-please 首次 cut v0.1.0 時誤觸凍結(可能在 Slice D 未完成前)。marker 由人工在 node 模型(Slice D)驗證綠後 push。見 [docs/grill/2026-06-13-v1-pipeline-decisions.md](grill/2026-06-13-v1-pipeline-decisions.md)。
- startup 自動 migrate + 每檔單一 tx + 失敗 fatal abort(dirty state 結構消除)。
- 三條 CI gate:已凍結檔不可變 / 編號連續 / version 與檔數一致。
- 測試隔離:每測 `t.TempDir()` 獨立檔 + 跑全 migration;**禁裸 `:memory:`**。

**版本機制**(2026-06-13)
- release-please 依 conventional commit 自動維護版號(feat→minor / fix·perf→patch / pre-1.0 breaking 留 0.x) + CHANGELOG + bump `binaryVersion`,開 reviewable release PR,merge 才 cut tag。
- **能力里程碑**(跨源護城河、雙向同步…)是 GitHub Release 的 notes 敘事,**不是特定版號**;binary 散布仍留 v1.0.0。

**Web 安全**(不可逆即設,Slice B 起點)
- 127.0.0.1 binding + Host allowlist middleware + GET/POST 動詞分離 + `http.CrossOriginProtection`。
- CSRF token / local token auth 留 mutationGuard chain seam,Phase 2 多人需求 additive 上。

## 待決問題（blocking decisions）
1. **Phase 2 / 多人模式安全邊界**:token 存放(keychain?)、CSRF token、local token auth、api 模式送雲審計 —— Phase 2 或推翻 single-user 假設時收緊。
2. 產品正式名稱(codename: workingbad)。
3. 時間預算(決定 Phase 2+ 該砍什麼)。
