# Auto-Grill 設計收斂記錄 — 2026-06-07

> 範圍:workingbad Phase 0 / Phase 1 的 truth-source schema、connector interface、與執行地基。
> 共 15 回合,皆達 consensus 或 majority。三審視角固定為:**務實派**(最小可行/傾向延後)、**架構派**(長期正確/預留接縫)、**工程師體驗派**(DX/成本/隱私)。
> 貫穿全程的單一心智模型:**append-only + supersede 單一寫入路徑;ingest/Summarizer 為唯一翻旗標 writer;爆炸半徑框在一個 source_ref 段;cursor/checkpoint 當優化提示、冪等當權威;全系統 uuid v7 字串 PK(raw_commits sha、raw_changes change_id 例外)**。

---

## 回合 1 — 多 repo scope + goal 跨 repo

**問題(branch:多 repo scope)**:truth source 要不要有 repo/workspace 一等公民概念?三選一:(A) 不存 repo、Phase 1 假設單 repo;(B) Entry 加 repo 欄位、goal 仍綁單 repo;(C) repo 欄位且 goal 可跨 repo。
**why**:直接定死 entries 冪等鍵與 source_checkpoint 主鍵——兩 repo 的 main branch 在 (source, source_ref) 會撞鍵幽靈合併、checkpoint 互覆;改晚了要 schema migration。

- **務實派(high)**:選 B 精簡版。不開 workspaces 表,把 repo 身分塞進 source_ref 複合字串、checkpoint scope。反對 A(是 data corruption bug 非延後)與 C(過度設計)。本 env 當下即掛多 repo,單 repo 假設站不住。
- **架構派(high)**:選 B 強化版。repo 作識別/隔離維度是不可逆地基(Phase 1 必做正式欄位 + (repo_id,source,source_ref) index + checkpoint PK (repo_id,source));goal 跨 repo 是 additive 可逆語意(放寬 part_of constraint 即可),延後。傾向獨立 repos 表。
- **工程師體驗派(high)**:選 B 強化版。多 repo 是工程師日常常態,A 會在 dogfooding 第一週幽靈合併;repo 隔離鍵漏了 corrupt 資料(難回頭)vs 跨 repo goal 晚做只少一聚合視圖(edge 可補)的不對稱性決定了前者現在做、後者延後。

**最終決定(consensus)**:B 強化版。entries 加 **repo_id 正式欄位**(非埋字串,git=remote URL 正規化 hash, manual/fetched=NULL);冪等 unique index=**(repo_id, source, source_ref)**;source_checkpoint PK=**(repo_id, source)**;raw 層存 repo 識別;**goal 不加 repo_id、不做跨 repo goal**(靠 part_of 聚合已帶 repo_id 的 activity)。**否決獨立 repos/workspaces 表**(Phase 1 無正規化需求,未來 workspace 1:N repo 是 additive)。repo_id 生成:remote URL hash 主、初始 commit sha 輔,禁用易變路徑名。

**保留歧見**:務實派主張埋字串而非正式欄位(求最小手術、無查詢需求);裁決按正式欄位走(回填成本不對稱),review 時複查。次要:無 remote 的純本地 repo 用什麼當穩定 id 待 mock-git 契約定案。

---

## 回合 2 — activity 粒度 / work-session 邊界

**問題(branch:work-session 邊界)**:一筆 activity 的邊界由什麼定義、怎麼算成穩定 source_ref?四選一:(A) 1 branch=1 activity;(B) 時間 gap 切窗;(C) PR/merge 邊界;(D) 顯式 session。long-lived branch 退化怎麼辦?
**why**:定死冪等 unique index 第三段內容、Source 與 AIProvider 的分界職責、重合成觸發單位。

- **務實派(medium)**:選 B,source_ref=(repo_id, branch, anchor_sha),禁 window_seq(歷史插入會整批位移炸冪等);加 MAX_COMMITS 上限防灌爆;main 不做特例。
- **架構派(high)**:選 B,source_ref 第三段定死 anchor_first_sha;切窗純免費規則落在 Source 層、不進 LLM;Summarize 觸發單位=一段;失效爆炸半徑框在單段。
- **工程師體驗派(high,有 web 來源)**:選 B 但用 **patch-id 而非 sha** 當 anchor——amend/rebase 改 sha 但 patch-id 不變,唯一能讓 source_ref 在 git 改寫下穩定的算法。GAP 研究無全域最佳值須 config 化。Sources: arxiv 1411.2878 / 1202.2131 / 2009.03115、emergentmind commit-metrics、useworktivity。

**最終決定(consensus)**:採 B 時間 gap 切窗。邊界規則純免費全落 Source 層:(repo_id,branch) 分桶→author-time 排序(時鐘漂移視同段)→gap>GAP_THRESHOLD 或段內達 MAX_COMMITS 封段。**source_ref=encode(repo_id, branch_name, session_anchor patch-id)**,anchor=段內時間最早 commit 的 patch-id(對齊 raw 層 patch-id 連 rewrite 鏈)。**禁 window_seq**。門檻 config 化、寫入 segment metadata、不進 source_ref(改門檻無 migration)。MAX_COMMITS=200。職責:Source.Pull 切段算 anchor、AIProvider.Summarize 只合成。**否決 A/C/D**(C 延 Phase 2 真 PR 後 additive 升級)。

**保留歧見(needs_user)**:GAP 預設值——務實/架構派 4h vs DX 派 90min;因 config 可調無 migration 留 Phase 3 校準。anchor sha vs patch-id 計算成本:patch-id 須自寫(go-git 無原生)但 raw 層本就要算。「長思考」誤切接受為可容忍代價。

---

## 回合 3 — activity 生命週期 / lazy-Summarize pending 狀態

**問題(branch:pending 狀態模型)**:pending(已歸段未合成)到 materialized 在 entries 表怎麼存?(A) 進 entries 加 summary_state;(B) 不進 entries、開 pending_segments 表;(C) 用 type/origin 區隔。冪等鍵、不變量 1、re-Summarize、佔位責任各落哪層?
**why**:卡 entries DDL、repository.upsert 契約、唯讀列表 UI、計費邊界。

- **務實派(high)**:選 B 但不開表——raw row 帶歸段鍵 + LEFT JOIN 反查,stale 靠 patch-id 鏈比對。Phase 1 mock 同步單機不需工作佇列。
- **架構派(high)**:選 B 開正式 segments 表;state 機掛 segments;冪等鍵權威從 entries 遷 segments;re-Summarize 走 superseded。三層職責:raw=不可變事實、segments=歸段+生命週期+冪等鍵、entries=已合成內容。
- **工程師體驗派(high)**:選 B 兩表分層;entries 只含 materialized(唯讀 UI 不出空殼);未進 entries=沒花 token(lazy 變物理事實);冪等鍵權威遷 segments。

**最終決定(majority)**:B 開正式 segments 表(採架構/DX 派,非務實派無表)。segments(PK/unique=(repo_id,source,source_ref), summary_state pending|stale, anchor_patch_id, 門檻 snapshot)+ segment_raw join。**entries 只含 materialized、出生即帶完整 title/body**。冪等鍵權威遷 segments;entries 軟約束「每 segment≤1 live activity」。re-Summarize=append-only 版本替換(舊 superseded、part_of 由新版繼承)。stale 由 ingest 翻、非 entries 自翻。**否決 A**(空 body 汙染 UI、原地更新破不變量 1)、**否決 C**(汙染 type/origin 枚舉)。

**保留歧見(dissent)**:務實派的無表 + LEFT JOIN 方案在「Phase 3 lazy 縮編或改 eager」情境會勝出;若 ROADMAP 待決 lazy vs eager 定為 eager,應回頭評估退無表。segments.summary_state 是否需 materializing 中間態待 Phase 3 並發再定。

---

## 回合 4 — Classify×Summarize 在 segment 邊界的職責切分

**問題(branch:segment→entries 基數)**:「同一 commit 可判 research」與「1 segment=1 entry=1 Summarize」打架。四選一:(A) 整段判單一 type;(B) 每 raw Classify 再二次分桶;(C) 雙鍵 1:N;(D) Phase 1 砍多型、git segment 一律 activity。
**why**:定死 AIProvider 介面 Classify 時機/粒度、segments↔entries 基數、計費爆炸半徑。

- **務實派(high)**:選 D,純 D + 純 A 留 Phase 4。git 自動判 research 在 Phase 1 無標註集無真模型可驗,延後零實際損失。否決 B/C 動不可逆地基。
- **架構派(high)**:選 D + 兩條接縫:Classify 輸入粒度釘 segment 級;軟不變量用 C 形狀措辭(每 segment_id,type ≤1 live)但物理 1:1,使 Phase 1 成 C 退化特例而非 A 死胡同。
- **工程師體驗派(high)**:選 D 但 DDL/軟不變量預鎖 C 形狀;local git ingest 完全不送模型(隱私守到最緊);patch-id 計算複用既有成本。

**最終決定(consensus)**:**D-with-seams**。Phase 1 git segment 恆 type=activity、Classify 不在 git 線跑(mock 回固定 activity/信心 1.0);research/decision 走 manual。Classify 簽名不動、輸入粒度釘 segment 級彙整文字、僅 materialize 時呼叫。**軟不變量用 C 形狀「每 (segment_id,type)≤1 live entry」但 Phase 1 物理 1:1**;segments 不存 type、保留 segment_raw,使 Phase 4 多型為 additive 零 migration。多型延 Phase 3/4(整段 Classify 判單一 type)。**否決 B/C-now**。

**保留歧見**:提問者原傾向純 D + 純 A,未要求預鎖 C 措辭;架構/DX 派堅持現在就寫 per (segment,type) 否則退化成 A,本裁決採納。DDL 物理 1:1 vs 直接 1:N:採物理 1:1,segment_raw 已足支撐未來拆分。

---

## 回合 5 — raw 層表設計 / ingest 寫入契約

**問題(branch:raw 層 schema)**:(1) 單表 vs 雙表?(2) 去重/版本鏈鍵?(3) segment_raw 指 sha 還 patch_id?(4) patch-id 算不出/merge commit 怎麼存?
**why**:raw 是不變量 8 唯一可追溯地基,前幾回合的 segment_raw/derived_from/patch-id 鏈全掛它。

- **務實派(high,web)**:單表 raw_commits、segment_raw 指 sha、stale 靠 patch_id 反查、merge 不入 raw。Sources: git-patch-id docs、libgit2 diff_patchid。
- **架構派(high,web)**:雙表但 raw_changes PK 用 surrogate change_id(非 patch_id——merge NULL + cherry-pick 撞值);segment_raw 指 change_id(rewrite 透明);merge 入庫 patch_id=NULL 不當 anchor。Sources: git-patch-id、git rebasing。
- **工程師體驗派(high,web)**:雙表 change_id surrogate;segment_raw 指 change_id(rebase 不誤觸 re-Summarize 計費);merge 入庫不分窗。Sources: go-git utils/diff、go-gitdiff。

**最終決定(majority,2:1)**:**雙表 + segment_raw 指 change_id**。raw_commits(PK=sha, 全欄 + patch_id nullable + change_id FK + is_current + superseded_by);raw_changes(PK=change_id uuid v7, repo_id, patch_id nullable, partial unique (repo_id,patch_id) WHERE NOT NULL)。sha=冪等鍵 INSERT OR IGNORE no-op;amend/rebase 走 find-or-create change_id + 翻 is_current,旗標掛 raw_commits、ingest 單一 writer 同 tx。**segment_raw 指 change_id**(rewrite 時 join 表不動、stale 單跳命中)。patch_id NULL→獨立 change_id 單節點;merge 入庫 patch_id=NULL 不可當 anchor、anchor 跳過 NULL-patch_id change。

**保留歧見(dissent)**:務實派單表+指 sha+patch_id 反查在 Phase 1 純 mock 下能跑、「不漏」成立,列迭代備案(若 find-or-create change_id 是顯著負擔可暫退單表)。前置任務:patch-id 自寫半天 spike(--stable, amend/rebase/reorder 三案契約測試)。

---

## 回合 6 — activity↔goal 連結 / part_of 寫入契約

**問題(branch:part_of edge)**:(1) part_of 誰何時建?(2) edge 冪等鍵與 immutable?(3) supersede 時 part_of 怎麼走?(4) iteration_of × part_of。
**why**:Phase 1 goal 聚合資料流唯一未落 schema 者;edges unique index/superseded_by/tx 邊界不可逆。

- **務實派(high)**:C 半自動;edges append-only + partial unique WHERE superseded_by IS NULL;(3) A 自動 re-point 同 tx;(4) 砍 iteration_of 實作只留枚舉。
- **架構派(high)**:不可逆地基只兩件(edges 帶 is_live+superseded_by、supersede tx 涵蓋 edges);attachToGoal=自動提議退化呼叫點;否決讀時 resolve;iteration_of 是分代快照不自動搬。
- **工程師體驗派(high)**:核心驗收=手動歸類 re-Summarize 後不消失;C 把 attachToGoal 做 repository 一等契約;自動提議掛錯比不掛更傷信任;A re-point 邊際成本近零。

**最終決定(consensus)**:(1) **C 半自動**(materialize 產孤兒, repository 提供 attachToGoal/detachFromGoal, 為 Phase 3 Relate suggest 退化呼叫點)。(2) edges append-only,DDL=(id uuid v7, from_id, to_id, relation 枚舉含 iteration_of, created_at, is_current, superseded_by, metadata),**partial unique (from_id,to_id,relation) WHERE is_current=1**,重複 attach no-op,detach 標 superseded 不刪。(3) **A 自動 re-point**(Summarizer 同 tx 把舊 activity 的 part_of/relates_to 複製指新 id、舊 edge superseded),讀路徑恆 WHERE is_current=1;否決 B 讀時 resolve、否決 C 失效。(4) **part_of 永遠指當時 goal 版本**,iteration 時不自動 re-point,查詢沿 iteration_of 遞迴聚合;Phase 1 砍 iterateGoal 實作、保留枚舉。

**保留歧見**:edges PK 命名(統一 id)、re-point 是否含 relates_to(本決策含, Phase 3 大量 relates_to 可能需背景 compaction)、attach 後被 re-Summarize 的競態(Phase 1 單機 tx 內 re-check is_current 防住)。

---

## 回合 7 — sync_state 表 + Sink 冪等鍵在 supersede 鏈下

**問題(branch:sync_state)**:(1) 冪等鍵掛 entry_id / segment source_ref / 雙鍵?(2) last_synced_hash 算什麼、supersede 後 update 還 new post?(3) Sink 看 Edge 嗎?(4) Phase 1 做到哪?
**why**:草案 sync_state(sink, entry_id,...) 是「entry 原地更新」舊心智,與 supersede 換 id 相撞→git rewrite 後雙貼(spec-review P0#2 最高風險)。

- **務實派(high)**:B 強化(段 source_ref 為鍵, 單一 nullable sync_target_ref);hash 算 Render 後 payload;不加 edge;範圍 C 偏薄(冪等單測, mock sink 端到端不擋 exit)。
- **架構派(high,web)**:C 雙鍵收斂成 sync_subject(subject_kind 枚舉);hash 算 Render payload 不含 edges;範圍 B(建表+mock+完整冪等測試)。Sources: Slack chat.update、ClickUp updatetask。
- **工程師體驗派(high,web)**:B「邏輯身分鍵」抽象成 sync_subject;hash 算 Render 後 push_fields 投影(fetch∩push=∅ 結構延伸到 hash);範圍 B。Sources: Slack/ClickUp docs。

**最終決定(majority)**:(1) 冪等鍵掛跨 supersede 穩定的 **sync_subject**(subject_kind 枚舉:git=segment source_ref, entry=邏輯身分);DDL=(id uuid v7, sink, subject_kind, subject_ref, external_ref, last_synced_hash, last_synced_entry_id, synced_at), UNIQUE(sink, subject_kind, subject_ref)。activity supersede 時 sync_state 不動。(2) **hash 算 Render 後 push_fields 投影**(Phase 1=title+body, 排除 edges/origin/id/timestamp);**Sink.Sync=upsert-by-external_ref**(create/update/skip-by-hash) 非 append。(3) Phase 1 只追 entry-level,subject_kind 枚舉為 additive 接縫,Sink 簽名不改;edge 同步延 Phase 2(獨立 sync_edge_state)。(4) **B**(建表+mock Sink+三條冪等測試 [hash skip / supersede 後 update 不雙貼 / disjoint sets 拒絕],不接真 API)。

**保留歧見(dissent)**:務實派 Q4 較薄(C 偏薄),讓步上限=冪等單測+最小 mock-sink no-op+config 層 fetch∩push=∅,但 supersede-不雙貼回歸測試不可砍。manual goal 若可原地編輯與不變量 1 張力,須在 manual 生命週期釘死(→ 回合 12 解)。

---

## 回合 8 — Summarize materialize 觸發 + AIProvider mock + 部分失敗

**問題(branch:LLM 熱路徑)**:(1) materialize 觸發點?(2) mock Summarize 決定性契約?(3) 部分失敗 tx 邊界?(4) materialize 與 push 順序耦合?
**why**:Phase 1 唯一 LLM 熱路徑,前 7 回合所有 lazy/有界爆炸半徑承諾在此兌現或破功。

- **務實派(high)**:1A+CLI 後門+計數列;mock 決定性純函式+error hook;逐段獨立 tx 不加 failed;兩階段串行不共 tx。
- **架構派(high)**:1A 多 caller materializer;mock det(batch)+可注入;逐段 tx(爆炸半徑釘 1 段);materialize 先於 push 且分 tx(真 API 邊界 Phase 1 就對)。
- **工程師體驗派(high)**:1A+免 LLM 計數列;mock 純函式+call-counter(把 1 segment=1 Summarize 變 CI 紅綠線);逐段 tx 守 api 成本底線;兩階段串行用 SELECT 結構約束防漏更新遠端。

**最終決定(consensus,罕見三審+提問者滿共識)**:(1) **1A 強化**:batchMaterialize(scope) 單一函式, caller=push-preview + workingbad summarize;list 只 SELECT is_current=1 不渲染 segment;**免 LLM 計數提示列**(COUNT segments WHERE pending|stale, 點擊才跑)。(2) mock Summarize=**決定性純函式**(綁 is_current raw 投影+anchor_patch_id, title/body 穩定)+ 注入 hook(WithSummarizeFunc/FailOnSegment/FailAfterN)+ call-counter;Classify mock 維持固定 activity/1.0。(3) **逐段獨立 transaction**(否決整批 rollback),失敗保持 pending/stale,**不加 failed 持久態**,partial-result 回報不靜默吞。(4) **兩階段嚴格串行**(materialize-all barrier → render-for-push, SELECT 條件 summary_state='materialized' AND is_current=1),不共 tx,push 在 DB tx 外(真 API 邊界 Phase 1 就對)。

**保留歧見**:Q1 DX 補償手段程度差異(務實派純 CLI 後門 vs 架構/DX 派加計數列);採計數列、可降級。additive 延後:單段即時 materialize、failed 態+退避、並行 materialize。

---

## 回合 9 — (記錄中無獨立第 9 回合;sync_state log#7、materialize log#8 已涵蓋;下列回合沿用提供資料的 log 編號)

> 提供的 JSON 共 14 個決策物件,內部 log 編號至 #15。本記錄依 JSON 順序呈現,標註各自 log 編號以對齊交叉引用。以上回合 1–8 對應 log#1–#8;以下回合對應 log#10–#15(log#9 在提供資料中以「manual 內容 hash source_ref」散見於 spec-review 引用,無獨立回合)。

---

## 回合 9 (log#10) — config schema + direction-policy 驗證 + secrets 邊界

**問題(branch:config 形狀)**:(1) 頂層形狀 A 扁平/B 列表/C 混合?repo_id 寫死還算出?(2) fetch/push_fields 宣告在哪、誰驗、何時驗?(3) secrets 明文/env 名/keychain?(4) 與已拍板 config 化需求相容性?
**why**:Phase 0 exit「啟動讀 config」與 log#7「config 載入驗證 ∩=∅」的物理地基。

- **務實派(high,web)**:C 混合;direction policy (b) 程式碼常數驗證在 wiring;secrets (b) env var 名;GAP=4h、MAX=200。Sources: koanf、go-playground/validator v10.28。
- **架構派(high,web)**:C;capability 程式碼常數 + config 只選子集;secrets env var only + Phase 1 即做型別 redaction;GAP=90min。Sources: koanf、validator。
- **工程師體驗派(high,web)**:C(ai discriminated union);(b) 強化驗證分兩段;secrets *_env;GAP=90min。Sources: koanf、validator releases。

**最終決定(consensus)**:(1) **C 混合**:ai/db 單例固定區塊(ai discriminated union oneof=local|api), sources/sinks 列表;repo_id 不寫 config、啟動由 mock-git 算(config 存 remote_url)。(2) fetch/push capability **程式碼常數權威**(config Phase 1 不覆寫、留 additive 子集選取接縫);驗證分兩段:koanf+validator 驗語法/oneof/dive,**registry wiring 階段驗 ∩=∅ + selected⊆capability** fatal abort。disjoint fixture=人造 bidirectional connector。(3) secrets **只存 *_env env var 名**(禁明文/keychain 延 Phase 2 additive);redaction Phase 1 結構上免費 + Secret 型別守門測試。(4) 全相容無 nullable 噪音:GAP/MAX per-source settings、subject_kind runtime 概念不進 config。

**保留歧見(needs_user)**:GAP_THRESHOLD 預設——4h(提問者) vs 90min(兩評審);裁決採 90min(避免一天多段併一坨炸 token),低風險可逆。direction policy config 覆寫接縫:留 schema 接縫 Phase 1 不啟用。

---

## 回合 10 (log#11) — repository 寫入契約:per-type 驗證 + FTS5 + status

**問題(branch:入庫寫入契約)**:(1) per-type 必填驗證落哪層?(2) status 對非 goal 噪音?(3) FTS5 鏡射(第一個 FTS migration 前定案=不可逆)?
**why**:entries 凍結後立刻要做的 migration + 寫入 choke point;FTS5×entries PK 型別是結構不可逆。

- **務實派(high,web)**:A 程式碼 validator;status A 留欄位 validator NULL;FTS contentless-delete + 只 is_current + entries 隱式 rowid 對齊(uuid PK 不變)。Sources: sqlite fts5、forum、modernc。
- **架構派(high,web)**:A 宣告式 fieldContract;status A;FTS standalone own-content + entry_id UNINDEXED + uuid v7 PK 不退讓 + 只 is_current。Sources: sqlite fts5 forum。
- **工程師體驗派(high,web)**:A 兩層(tag 管語法 + validator 管 type 語意);status A;FTS ordinary own-content 否決 external-content(content_rowid 對齊 uuid 有坑)。Sources: sqlite fts5、drift#754、modernc。

**最終決定(consensus)**:(1) **程式碼內 per-type validator**(宣告式 fieldContract map + executor, 兩層:struct-tag 語法 + executor type 條件),choke point=**repository.insertEntry 單一入口**;manual source_ref hash 非空一併強制。**否決 B field_contract 表**(遞迴自驗+migration)、**否決 C-as-sole**(條件分歧難純 tag)。(2) **status 留 entries 共有欄位 + validator 強制非 goal NULL**(升入庫不變量);**否決 B 拆 goal_state 表**(goal 也是 entries row、逼 join);狀態機三條(activity 無 status / goal manual setGoalStatus 驗 enum 不驗轉移 / goal fetched 唯讀)。(3) **FTS5 own-content/contentless 虛擬表**,entries 維持 **uuid v7 PK 不退讓**(否決 external-content 逼 INTEGER rowid 撞全系統 PK 策略),**只鏡射 is_current=1**,repository.insertEntry/supersede **同 tx 手動維護不用 trigger**。

**保留歧見(dissent)**:FTS 表型態子分歧——(A) contentless-delete(隱私加分、儲存省, 裁決傾向)vs (B) own-content(除錯直觀);兩者皆靠 entry_id 文字欄 + 同 tx 手動維護,additive 可互換不阻塞。共同延後:status 轉移狀態機、edge 全文搜尋、搜尋 superseded 歷史。

---

## 回合 11 (log#12) — manual entry 生命週期 / 編輯契約

**問題(branch:manual entry 編輯)**:(1) 編輯走 A 原地 UPDATE / B supersede / C origin 分流?(2) 內容 hash source_ref 與編輯改字的身分連續性?(3) goal status 編輯與 iteration 分流?(4) dogfooding 測試斷言?
**why**:manual 是 Phase 1 唯一人類直接寫入路徑 + dogfooding exit,打破「entry 由機器 writer 控制」假設。

- **務實派(medium)**:B-lite supersede + 穩定 logical_id + edit-coalescing;source_ref 管去重、logical_id 管身分;修正 log#7 manual subject 用 hash 的潛在 bug。
- **架構派(high)**:B + logical_id 把邏輯身分從物理 row id 分離 + 幽靈防護從 hash 遷出;logical_id 是「一個欄位解三個未來問題」槓桿;否決 C 維度污染。
- **工程師體驗派(high)**:B + DX 三道糖衣(前端 draft、讀路徑 is_current=1、編輯史免費);manual 零 LLM 故 supersede 免費;logical_id 當 subject 有隱私副效益。

**最終決定(consensus)**:**受控 append-only supersede + stable logical_id + edit-coalescing**。(1) **B**,否決 A(破不變量 1 + 繞 FTS/sync choke point 靜默漏更新)、否決 C(三寫路徑維度污染)。entries 加 **logical_id**(uuid v7 跨編輯穩定身分, create=自身 id, supersede 沿用);editEntry 對 manual 在 COALESCE_WINDOW 內且未被引用/未 push 則原地 UPDATE,否則 supersede。(2) **source_ref(內容 hash)只做 create 去重、身分掛 logical_id**;**sync subject_ref 對 manual 改綁 logical_id**(精確化修正 log#7,hash 會漂移不能當穩定 subject)。(3) 改字/改 status=同 logical_id supersede(setGoalStatus 退化呼叫點),iteration=新 logical_id+edge 分流;Phase 1 只實作前者。(4) 七條斷言(create→logical_id/FTS、edit→新 row 同 logical_id/FTS 新搜得舊搜不到、coalesce 窗內不灌爆、attach 後 edit→edge re-point、setGoalStatus、重建內容 hash 命中不幽靈、已 push 後 edit 不雙貼)。

**保留歧見(needs_user)**:COALESCE_WINDOW 是否真做——架構派傾向全 supersede + 前端 debounce,務實/DX 派傾向 repository 層 coalesce;採帶 coalesce 但設可逆軟旋鈕(=∞ 退回純 B)。UI debounce Phase 1 先不做。

---

## 回合 12 (log#13) — source_checkpoint DDL + opaque cursor + Pull tx 邊界

**問題(branch:source_checkpoint)**:(1) cursor 編碼 A 全 repo 單游標/B per-branch/C 時間 watermark?(2) DDL + advance 是否豁免不變量 1?(3) cursor advance 與 raw/segment tx 邊界?(4) checkpoint 與 materialize 解耦?
**why**:Phase 1 最後一張未定 DDL 的核心表;cursor 編碼在 rebase/force-push 漏抓不可逆。

- **務實派(high)**:B per-branch(封 opaque blob, 冪等當權威 cursor 當提示);advance 原地 UPDATE 豁免不變量 1;at-least-once 不要求強一致;兩進度指標分層。
- **架構派(high)**:B + patch-id 感知;cursor 寧落後不超前;鬆耦合 at-least-once(教科書 idempotent consumer);checkpoint↔materialize 正交。
- **工程師體驗派(high,web)**:B(advisory hint);raw sha unique+change_id 鏈為正確性錨;raw 去重用顯式 ON CONFLICT DO NOTHING(非裸 INSERT OR IGNORE 以免靜默吞約束)。Sources: sqlite wal/sharedcache/pragma、INSERT-OR-IGNORE 文。

**最終決定(consensus)**:(1) **B per-branch opaque blob**(advisory hint, 正確性錨在 raw sha unique + patch_id 鏈);rebase/force-push 改 sha 因冪等收斂而正確。否決 A(全 repo 重判)、C(時鐘漂移 + 違反「不透明 cursor 非 time.Time」)。(2) DDL=(repo_id, source, cursor blob NOT NULL, updated_at, PK(repo_id,source));cursor core 不解讀;**advance=原地 UPDATE,明確豁免不變量 1**(唯一可變表,白紙黑字寫進契約+測試)。(3) **raw/segment 寫入與 checkpoint advance 分離,at-least-once + 冪等收斂**(否決強一致大 tx);崩潰=安全重放(sha no-op + segment upsert no-op);raw 去重用顯式 ON CONFLICT(repo_id,sha) DO NOTHING。(4) **checkpoint 只追 ingest 進度、與 materialize 正交**,materialize 失敗(log#8)不回退 checkpoint。六條契約測試(增量不漏不重、崩潰重放冪等、rebase 不誤增 + 翻 stale、per-branch 隔離)。

**保留歧見(dissent: none, 僅 Phase 2 可逆優化)**:cursor blob 隨 branch 線性長大(Phase 2 prune)、大 repo 重放成本、frontier set 精準度、advance 併進同 tx vs 單獨。賭點:依賴 ingest 冪等鍵永遠存在(git sha / ClickUp task_id 皆有)。

---

## 回合 13 (log#14) — Phase 1 Web UI/CLI 範圍 + state-changing 安全邊界

**問題(branch:人機介面層)**:(1) CLI-only/Web-only/兩者?(2) 唯一寫入閘門立架構不變量?(3) state-changing 安全做到哪?(4) Web 路由範圍 + 零 graph?
**why**:Phase 1 四 deliverable 唯一未定契約;DNS rebinding 對 localhost 是真實攻擊,Origin/Host 驗證+動詞分離必須設進 handler 初始形狀(不可逆)。

- **務實派(high,web)**:Q1 C(CLI 先);Q3 B 但只上不可逆兩件(127.0.0.1+Host 驗證+動詞分離)、CSRF/token 延後留 mutationGuard chain;零 graph。Sources: golang/go#23993、CVE-2025-59956、htmx security、OWASP CSRF。
- **架構派(high,web)**:Q1 C;Q3 B 用 Go1.25 http.CrossOriginProtection 一次到位;唯一寫入閘門立不變量;零 graph 讀時遞迴聚合。Sources: net/http CrossOriginProtection、go csrf.go、alexedwards、host-validation。
- **工程師體驗派(high,web)**:Q1 C;Q3 B 零依賴 stdlib + 自寫 Host allowlist(CrossOriginProtection 不含 DNS rebinding 防護);零 graph、計數列純 COUNT。Sources: CrossOriginProtection、github.blog DNS rebinding、rafter MCP rebinding。

**最終決定(consensus)**:(1) **Q1=C 分階**:CLI-first(Slice A:sync/list/note/decision/goal/attach/detach/status/summarize 達 dogfooding exit), Web 唯讀+表單 Slice B 緊接,共用同一 repository service。(2) **唯一寫入閘門=repository service 架構不變量**,CLI/HTTP thin adapter;寫入清單=createManualEntry/editEntry/attachToGoal/detachFromGoal/setGoalStatus/triggerMaterialize + 唯讀 listEntries/search/getGoalActivities/countPendingSegments;service table-driven 厚測 + adapter 薄整合測試。(3) **Q3=B**,不可逆即設:**127.0.0.1 binding + GET/POST 動詞嚴格分離 + Host header allowlist middleware(防 DNS rebinding)**+ Go1.25 http.CrossOriginProtection(defense-in-depth);CSRF token/local token auth 留 mutationGuard chain seam additive 後補(single-user 本機暫不上)。(4) **零 graph 視覺化**:list 全 5 type WHERE is_current=1 不渲染 segment、計數提示列純 COUNT、goal 詳情頁扁平 part_of 列表(沿 iteration_of 遞迴聚合不畫圖)、路由不含 /graph。

**保留歧見(needs_user)**:Slice B Web 同 Phase vs 延 Phase 1.5(時間預算);Go≥1.25 硬依賴須確認 CI;CSRF/token 延後依賴 single-user 假設(待決 branch,若推翻須提前);editEntry coalesce UI 須明示「最近編輯會合併」。

---

## 回合 14 (log#15) — migration 機制 + DDL 演進紀律 + 測試隔離

**問題(branch:migration 機制)**:(1) goose/golang-migrate/自建?(2) forward-only vs down,凍結時點?(3) 執行時機+失敗+dirty state?(4) 測試隔離 :memory: vs temp file?
**why**:Phase 0 exit「建好 DB schema」地基;前 14 回合「additive/零 migration」承諾在沒有 migration 紀律下是空話。

- **務實派(high,web)**:goose;凍結 tag-time(pre-1.0 可編輯舊檔);自動 migrate+全 tx+abort 根除 dirty;temp file + 全 migrate。Sources: goose、golang-migrate#899、sharedcache、modernc。
- **架構派(high,web)**:goose v3;C 強化 squash 到單一 schema、tag 凍結;startup auto+逐檔 tx+abort;每測 temp file 全 migrate。Sources: goose、modernc、go-sqlite3、manage-connections。
- **工程師體驗派(high,web)**:goose v3;forward-only 兩段(C 過渡→A 凍結)+ CI gate;startup auto+逐檔 tx+abort;temp file(禁裸 :memory: 多連線各自空 DB 的 flaky 地雷)。Sources: goose、modernc、inmemorydb、sqlx#2510、sharedcache。

**最終決定(consensus)**:(1) **goose v3 + embed.FS**(import-only 單 binary, 否決 golang-migrate cgo/相容性、否決自建 NIH);modernc+goose+FTS5 跨平台 + :memory:/temp file 列 Phase 0 spike(併 spike#1)。(2) **forward-only,凍結時點=首個 git tag v0.1.0**(pre-1.0 可編輯未發布檔/squash, tag 後純 additive 疊新檔永不改舊檔不寫 down);三條 **CI gate**(已 tag 檔不可變/編號連續/version 與檔數一致)。(3) **startup 自動 migrate + 每檔單一 transaction + 失敗 fatal abort 不降級**(全 tx 結構消除 dirty state, 同 log#13 冪等可重放);保留顯式 workingbad migrate 逃生口。(4) **temp file(t.TempDir())+ 跑全部真實 migration 重建 schema**(禁裸 :memory: 多連線各自空 DB 的 flaky 地雷;single-writer choke point 天然閃過);至少一條測 migration 鏈本身;CI 速度 t.Parallel()+獨立檔, template clone 為 additive 優化。

**保留歧見(dissent)**:測試 CI 優化現在 vs 延後(採每測全 migrate, template/BEGIN-ROLLBACK 列 additive);squash-edit 多人協作衝突依賴 single-user 假設(推翻則凍結提前);Phase 2 多使用者前加「migrate 前備份 sqlite」護欄(additive seam)。

---

## 跨回合一致性總結

- **單一寫入路徑/單一 writer**:repository service(log#14)→ insertEntry choke point(log#11)→ ingest/Summarizer 同 tx 翻旗標(log#5/#8/#12)。
- **append+supersede 同構四層**:raw_commits is_current(log#5)→ segment summary_state(log#3)→ entry superseded(log#3/#12)→ edge is_current(log#6),唯一例外 source_checkpoint 豁免不變量 1(log#13)。
- **穩定邏輯身分鍵貫穿**:segment source_ref=patch-id anchor(log#2)、segment_raw 指 change_id(log#5)、sync subject_ref(log#7)、manual logical_id(log#12)——皆「指穩定邏輯層、不指會換的物理實體」。
- **爆炸半徑=一個 source_ref 段**:log#2/#4/#7/#8/#13 反覆兌現。
- **技術棧鎖定**:Go 單 binary 免 cgo、modernc/sqlite、goose+embed.FS、koanf v2+go-playground/validator v10、urfave/cli v3+net/http+html/template+htmx、google/uuid v7、Go≥1.25(CrossOriginProtection)。
