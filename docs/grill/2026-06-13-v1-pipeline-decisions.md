# v1.0.0 LLM Aggregation Pipeline — 定稿決策

> 配套研究：[2026-06-13-v1-llm-aggregation-pipeline-research.md](2026-06-13-v1-llm-aggregation-pipeline-research.md)（8 題 deep-research + 對抗式驗證）。
> 本檔 = 「我們決定了什麼」；研究檔 = 「我們查到什麼」。

## 範圍

**MVP = Phase 1+2+3 全 pipeline（含跨源 Layer C 護城河）**。研究估 ~9-11 週。
理由：跨源關聯是 CLAUDE.md 寫的核心差異化、研究反證指出它不是可 defer 的 nice-to-have；先 ship 沒護城河的 MVP 會淪為本地版 Zapier。

## Pipeline 三步（locked）

```
Step 1 split:     per entry，LLM 判 1 vs N node（decided | undecided）
Step 2 aggregate: undecided node 同源聚合（cluster + LLM-validate）
Step 3 relate:    Layer A source-native(0 LLM) → Layer B temporal+vector→LLM → Layer C cross-source sampled
```

## Domain model（locked）

- **Entry** = source-immutable raw record，有 source 給的 unique id，**永不改內容**。
- **Node** = graph 上的工作單位；entry↔node 是 **many-to-many mapping**，LLM 只管 mapping + node↔node edge，不改 entry。
- split 後：1 entry → N node（mapping 1:N）。aggregate 後：N entry → 1 node（mapping N:1）。
- 5 node types：activity / research / discuss / decision / goal。5 edge relations：relates_to / derived_from / blocks / part_of / iteration_of。多 secondary label per node（已 ship）。
- Bitemporal：occurred_at + ingested_at。

## 8 項定稿決策

| # | 決策 | 定案 | 信心 | 關鍵 caveat |
|---|---|---|---|---|
| 1 | **step1 split 偏向** | **Over-split 悲觀** — 列所有可能獨立單位 + `is_ambiguous` flag + 扁平 JSON schema + 3-5 個 edge-case few-shot；下游靠人工 merge + embedding 分群兜底 | medium | constrained decoding 保證結構不保證語意、須配語意驗證迴圈；Ollama >4k token 退化 |
| 2 | **step2 聚合策略** | **Cluster + LLM-validate** — Phase 1 純向量分群 θ=0.80 零 LLM → Phase 2 加選擇性 LLM 驗證 | high | sqlite-vec **只給向量 KNN index、不含 clustering 演算法**；clustering 要外掛純 Go HDBSCAN / 用 k-means/epsilon-ball / 延到 Phase 2 |
| 3 | **step2 block 粒度** | **source_instance**（`GROUP BY source_type, source_instance`，同 repo） | medium | MVP 跳過 meta-blocking；within-instance precision <0.85 才加 WNP（Weight Node Pruning） |
| 4 | **step3 方向性** | **QA + temporal precedence、丟掉 symmetry constraint** | high（帶強反證） | 因果本不對稱、symmetry 邏輯錯；真實 narrative 受 temporal-bias 主導，**生產品質需 Claude 非 Ollama**（Ollama 用戶 step3 降級，須記錄） |
| 5 | **confidence UI** | **二元 dashed/solid (≥0.7) + slider filter**；不顯示原始數字 | medium | 2026 研究示連續編碼更能傳達真實侷限；Phase 2 再加敘事/tooltip calibration |
| 6 | **rollout** | **Phase 1+2+3 一起**（全 pipeline + 跨源） | medium | **embeddings 是 Phase 2 必要、非可選**（反證排除純 temporal+keyword 的 MVP） |
| 7 | **LLM 失敗 fallback** | LLM 掛 → **Halt + queue**（狀態乾淨）；無效輸出 → **JSON repair + flag manual review** | medium | Ollama CPU 的 BM25 hybrid fallback 研究說不可行、不採 |
| 8 | **跨源對稱性** | **不對稱 as-is**（step2 禁聚合 / step3 可關聯）+ **Layer C sampled 進 MVP** | medium | 1k-10k calls/day 預算需積極 sampling；sqlite-vec ~500K 向量天花板 |

## 補拍板：refactor 核對時抓到的 2 個缺口（2026-06-13 已定）

### 缺口 1 — embeddings provider（Claude 無 embedding API）→ **定案：`Embedder` interface + 可插拔實作**

決策 #6「embeddings 必要」跟「Ollama OR Claude setup 二選一」原本矛盾（Claude API 沒有 embedding endpoint）。**定案**：程式內提供獨立 `Embedder` interface（鏡像 `AIProvider` pattern），各 source/setup 各自實作（Ollama nomic-embed-text / 雲端 Voyage / OpenAI embeddings…），由 `config.yaml` 選。
- embedder 與 LLM provider **解耦**：可以 LLM=Claude + Embedder=Ollama-local，也可全雲端。
- **資料主權 caveat**（須在 setup docs 寫明）：選雲端 embedder = embedding 內容送雲；要全本地資料主權就選 Ollama embedder。

### 缺口 2 — sqlite-vec 單一-Go-binary 路線 → **定案：spike viant 純 Go → fallback WASM → 排除 CGO**

- Slice E 第一動作：spike `viant/sqlite-vec`（純 Go，保現用 `modernc.org/sqlite` + 交叉編譯不變）。
- 若成熟度/效能不堪用：fallback 到 WASM（`ncruces/go-sqlite3`，需換 driver、storage 層大遷移）。
- **CGO（asg017/sqlite-vec）排除** — 直接違「輕量、可跨平台編譯」+「純 Go 免 cgo」兩條鐵律。

## 補拍板：驗證後新增的兩條決策（2026-06-13，GO-WITH-ADJUSTMENTS）

### edges re-key 策略（破壞性變更、動 Slice D2 前必須二選一）

驗證發現「edges become node-edges」掩蓋了破壞性：`edges.from_id/to_id` 指 entry-id（per-version、非 logical_id），81 處引用 + `rePointAllLiveEdges` 每次 supersede 都跑 + `idx_edges_live_triple UNIQUE` + bitemporal `EdgesAt` 都建在 entry-id edges 上。**二選一**：
- **(a) edges 改指 node-id** — 連動 unique-triple index + re-point 不變量整套重寫
- **(b) 新增平行 `node_edges` 表、deprecate 舊 edges** — 隔離風險、但雙表並存期複雜

→ 此決策 + `edge.confidence` column 所有權**指派給 Slice D**（D 出 confidence column，消除 D/I 曖昧）。

### clustering 演算法（Phase 1 定案，消除 plan/decisions 矛盾）

純 Go HDBSCAN 無堪用版本（Belval「not in working condition」、其餘 404）。**Phase 1 pre-commit k-means 或 epsilon-ball**（簡單可行）；HDBSCAN 明確推 Phase 2。Step 2 aggregate 的 θ=0.80 clustering **Phase 1 就要能跑**（與 refactor plan 對齊，不 defer）。此 spike 與 Slice E 的 viant/sqlite-vec spike 一起前置。

## 仍待釐清的工程細節

- **LLM provider routing**：Ollama(local 隱私) vs Claude(雲端品質) 對 step3 方向性的品質落差，setup 二選一不變、但須在 docs 記錄品質 caveat。

## 護城河自我檢查（CLAUDE.md 三件交集）

- **LLM 跨 domain 互譯**：step1 split + step3 relate（git→人話）✅ 全 pipeline
- **本地 + bitemporal**：entry immutable + node supersede + sqlite-vec 全本地 ✅
- **語意圖**：5 edge relations + Layer A/B/C 關聯抽取 ✅ 含跨源
- 缺一即降級成本地版 Zapier — 全 pipeline MVP 三者齊備。