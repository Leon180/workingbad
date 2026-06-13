# v1.0.0 重構範圍 + 時程 + Issue 處置

> 配套：[pipeline 決策](2026-06-13-v1-pipeline-decisions.md) · [deep research](2026-06-13-v1-llm-aggregation-pipeline-research.md) · **[獨立驗證裁決](2026-06-13-v1-plan-verification-verdict.md)**

> ## ⚠️ 驗證後修正（2026-06-13，GO-WITH-ADJUSTMENTS）
> 5 位獨立對抗式驗證者全數 `needs-adjustment`（無 NO-GO）。**方向 + Gap1/Gap2 選型 + triage 全通過**，但以下必須在動工前修正（細節見 [verdict 文件](2026-06-13-v1-plan-verification-verdict.md)）：
> 1. **Slice D 拆 D1+D2**：D1 = additive node 表 + entry_node_map + backfill（~1wk）；D2 = 搬遷 supersede/version/FTS5/bitemporal + **edges re-key** + 測試重寫（~3-5wk，★最高風險）。今天 supersede/FTS/bitemporal **全在 entries 上**，是核心反轉、不是「加一層」。
> 2. **edges re-key 是破壞性變更**：`edges.from_id/to_id` 指 entry-id（per-version）、81 處引用、`rePointAllLiveEdges` 每次 supersede 都跑。需在 decisions 二選一：(a) 改指 node-id（連動 unique-triple index + re-point 重寫）或 (b) 新增平行 `node_edges` 表。
> 3. **Slice F 是 interface 重設計、非 mock→real swap**：AIProvider 今天**沒有 Split method**、`Relate` 回傳的 Edge **沒有 confidence 欄**、`Classify`/`Relate` 零 production caller（dead slot）。F 升 ~2.5-3wk，含 3-stage orchestrator 骨架（今天唯一驅動是單步 `BatchMaterialize`）。
> 4. **Connector runtime 完全不存在**：`source_checkpoint`/`sync_state` 表零 query 零 method、`OriginPushed` 從未被寫、無 scheduler。新增 ~1wk 前置 plumbing 併入 J，K 依賴它。
> 5. **Pure-Go HDBSCAN 無堪用版本** → Phase 1 pre-commit **k-means/epsilon-ball**，HDBSCAN 推 Phase 2。⚠️ 並消除 decisions(Phase1 含 θ=0.80 clustering) vs 本 plan 的矛盾。
> 6. **Slice L 風險被誤判**：graph.js（PR #56）已 ship pan/zoom/tooltip/edge-filter → confidence slider **更便宜**；真風險是 merge/split/link **backend-blocked on #49/#50/#52**，這些須於 G/H/I 先交付否則 L 漲到 ~4-4.5wk。
> 7. **誠實時程：12-16wk → 16-22wk**。
> 8. **Pre-flight**：所有開工 + triage 必須在 sync 到 origin/main（PR #65）的樹上做（本地兩樹皆過期）。

## Executive Summary

全 pipeline（Phase 1+2+3 含跨源護城河）重構**驗證後估 16-22 週 single-dev serial**（原樂觀 12-16wk 地板不成立，差額來自 Slice D 的 supersede/edges/FTS/bitemporal 搬遷被嚴重低估 + connector runtime / AIProvider Split / clustering spike 三個被誤標 evolve 的淨新子系統）。關鍵路徑 `D2 node-migration → F AIProvider+orchestrator → G split → H aggregate → I relate → L UI`。

## ✅ 核對時抓到的 2 個致命缺口（decisions 漏掉，2026-06-13 已補拍板）

### 缺口 1：Claude 沒有 embeddings API → **定案：`Embedder` interface + 可插拔**

決策 #6「embeddings 必要」原跟「Ollama OR Claude setup 二選一」矛盾（Claude 無 embedding endpoint）。**定案**：程式內提供獨立 `Embedder` interface（鏡像 AIProvider），實作可選 Ollama-local / 雲端，config 決定；embedder 與 LLM provider **解耦**。資料主權 caveat 寫進 setup docs。
→ 影響 Slice E/F：`AIProvider.Embed()` 改成獨立 `Embedder` interface。

### 缺口 2：sqlite-vec 純 Go 成熟度威脅「單一 Go binary」鐵律 → **定案：spike viant → fallback WASM → 排除 CGO**

Slice E 第一動作 spike `viant/sqlite-vec`（純 Go、保 modernc/sqlite + 交叉編譯）；不堪用 fallback WASM（`ncruces/go-sqlite3`，換 driver 大遷移）；CGO（asg017）排除（違鐵律）。

## Slice 分解（依依賴排序）

| Slice | 目標 | 主要變更（kind/effort） | 依賴 | 估時 |
|---|---|---|---|---|
| **D — Node 層 + mapping** ★最高風險 | entry/node 分層 | migration `nodes` 表 + `entry_node_map`(N:M) `new/XL`；entries→1:1 nodes backfill `new/L`；edges 改 node-edge + `confidence REAL` 欄 `evolve/L`；RepositoryService 加 node CRUD `evolve/L`；sqlc regen `M` | — | ~2-3 週 |
| **E — 向量基建** ★鐵律風險 | embedding + KNN | spike viant→WASM fallback `new/M`；vec table/欄 migration `new/M`；獨立 `Embedder` interface + Ollama 實作 `new/L`；KNN wrapper `new/M` | — | ~1.5-2 週 |
| **F — 真實 AIProvider** | Ollama+Claude | mock→真 client `rewrite/L`；Classify/Summarize/Relate `new/L`；JSON repair+retry+halt-queue+batch/cache `new/L` | — | ~2 週 |
| **G — Step1 split+classify** | LLM 切分 | over-split（flat schema/few-shot/is_ambiguous）`new/L`；寫 node+mapping `new/M`；classify type `new/S` | D,F | ~1 週 |
| **H — Step2 aggregate** | 同源聚合 | vector cluster θ=0.80 `new/M`；clustering algo（k-means/HDBSCAN 外掛）`new/M`；LLM validate+merge+remap `new/M` | D,E,F,G | ~1 週 |
| **I — Step3 relate** | typed edges | Layer A signal extractor（泛化 seed-github Closes#N）`evolve/M`；Layer B temporal+vector→LLM 方向（QA+temporal,丟symmetry）`new/L`；Layer C cross-source sampled `new/L`；edge.confidence 填值 `new/M` | D,F,H | ~2-3 週 |
| **J — 真實 GitHub Source** | incremental sync | Source.Pull+checkpoint cursor `rewrite/L`；raw 層 per source `new/M`；seed-github evolve `evolve/M` | D（可與 F 並行） | ~1 週 |
| **K — GitHub Sink** | push-back | annotates edge+local node→comment/subtask `new/L`；origin local→pushed+sync_state idempotency `evolve/M` | D,J | ~1 週 |
| **L — UI: node+confidence+actions** | 流暢體驗 | node 渲染+source chips `evolve/L`；edge confidence dashed/solid+slider `new/M`；graph actions merge/split/link/annotate `new/L`；collaborators `new/M`；既有 #14-18 `evolve/M` | D,I | ~3 週 |

## 依賴 DAG

```
D (node schema) ─┬─► G ─► H ─► I ─► L
                 ├─► J ─► K
E (vectors) ─────┴─► H, I(LayerB/C)
F (AIProvider) ──┴─► G, H, I
```
- **可並行起跑**：D / E / F（不同檔案、無互鎖）+ J（D 落地後）。
- **關鍵路徑**：D → F → G → H → I → L ≈ 12+12+5+5+12+12 ≈ 58 dev-days ≈ 12 週。

## 時程估算

S=0.5d / M=2d / L=5d / XL=12d。Serial 總和 ≈ **77 dev-days ≈ 15-16 週**；含 D/E/F/J 早期並行則關鍵路徑 ≈ **12 週**。誠實範圍：**12-16 週 single-dev**（vs 研究樂觀 9-11 週）。並行（多人/多 session）可壓到 ~10 週但 D 是序列化瓶頸（所有人等它）。

## 關鍵風險與待決工程細節

1. **Slice D 是最危險的一塊** — entry/node bifurcation 動到核心模型，現有所有 query / web handler / graph layout 都假設 `entry == node`。建議先寫完整 migration + backfill + 全測試綠再往下。
2. **embeddings provider（缺口1）** — 補拍板後才能定 Slice E/F 的 embedder。
3. **sqlite-vec 路線（缺口2）** — Slice E spike 決定，可能威脅單一 binary 鐵律。
4. **clustering 演算法** — k-means（簡單、需定 K）vs epsilon-ball（簡單、需定半徑）vs 外掛純 Go HDBSCAN（貼合研究但整合成本）。
5. **Ollama step3 方向性降級** — 已記錄 caveat，但若多數 dogfood 用戶選 Ollama，Layer B 品質要實測。

## Issue 處置表

> ⚠️ #49/#50/#52 在舊 backlog 是「P2/P3 backend nice-to-have」，但**新 pipeline 下它們是 over-split + LLM-fallback 決策明確依賴的「人工修正安全網」** → 從 backlog **晉升為核心 pipeline 元件**。

| # | 標題 | 處置 | 對應 slice | 理由 |
|---|---|---|---|---|
| #49 | Service.MergeEntries | **rescope↑核心** | H + L | Step2 aggregate 的人工 merge 安全網（over-split 兜底）；從 P2 升核心 |
| #50 | Service.SplitEntry | **rescope↑核心** | G + L | Step1 split 的人工切分 fallback；從 P3 升核心 |
| #52 | generic edge link/unlink | **rescope↑核心** | I + L | Step3 relate 的人工 edge 修正；從 P2 升核心 |
| #48 | collaborators | **rescope** | L | 變 node-level collaborators（actor 在 edge/node 上）；node 層落地後做 |
| #14 | pan+zoom SVG | keep | L | UI polish，折進 Slice L，P1 |
| #15 | htmx 側欄 | keep | L | node detail panel，折進 Slice L，P1 |
| #16 | hover tooltip | keep | L | UI polish，P2 |
| #17 | edge-type filter | **rescope** | L | 跟 confidence slider 合併成一個 edge filter 面板 |
| #18 | search highlight | keep | L | UI polish，P2 |
| #25 | Dependency Graph + Dependabot | keep-as-is | ops | 跟 pipeline 無關、需你翻 settings toggle，P1 保留 |
| #20 | Server.Close | **verify→close** | — | 應已由已 merge 的 PR #62 實作；確認後關 |
| #21 | errors.Is + path.Base nits | **verify→close** | — | 同上、PR #62 已含；確認後關 |
| #41 | L2 auto-apply | defer-post-v1 | — | CI 便利、跟 pipeline 無關、等 L1 一週資料 |
| #32 | cockroachdb/errors | defer-post-v1 | — | 標 deferred、跟 pipeline 無關 |
| #64 | retro 2026-06-10→11 | keep | — | retro issue、待你 review 後關 |

## 建議下一步

**先做 Slice D（node schema bifurcation）** — 它是依賴樹的根（blocks 6 個 slice）、最危險的 migration、且是純本地工作（不含 LLM/vector/connector 不確定性）。趁 D 在做，並行 spike 兩個缺口：(a) embeddings provider 拍板、(b) sqlite-vec 路線 spike（Slice E 前置）。D 一綠，整條鏈解鎖。

**先別碰** Slice E/F 的實作直到兩個缺口拍板 — 否則 embedder/向量路線改變會讓已寫的 code 重做。
