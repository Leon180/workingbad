# v1.0.0 LLM-Aggregation Pipeline — 8 項開放決策研究報告

## Executive Summary

本報告綜整 workingbad（本地優先、LLM 輔助的工程師 truth source）v1.0.0 聚合管線 8 項待拍板決策的研究與對抗式驗證結果。整體傾向清晰：**Step 2 聚合採「向量預分群 + LLM 驗證」而非 O(N²) pairwise**、**block key 用 source_instance**、**Step 1 切分偏向 over-split（有下游人工 merge 兜底）**、**Step 3 方向性用 directional QA pivots（但放棄 symmetry constraint、只保留 temporal precedence）**、**confidence UI 用 dashed/solid 二元 + slider**、**rollout 以 Phase 1+2（ingest + embeddings，零 LLM）為 MVP 並 defer Phase 3 LLM 推理**、**LLM 失敗採 tiered fallback（retry → JSON repair → flag → halt+queue）**、**「禁聚合但可關聯」的不對稱性學界業界皆站得住腳**。多項發現帶有 ⚠️ 中信心與反證——最關鍵的三點是：(1) Phase 1「純 temporal+keyword、零 embedding」交付價值的說法**被反證推翻**，embeddings 是 MVP 必要非可選；(2) Step 3 causal direction 在真實 narrative 上受 temporal-ordering bias 主導，structured QA 不能像合成資料那樣解決，且 symmetry constraint 對因果關係**邏輯上錯誤**；(3) cross-source Layer C 是產品**核心價值差異化**而非可 defer 的 nice-to-have，但在 1k–10k/day 預算下需積極 sampling。以下逐題展開，並在文末附 PM 決策表一覽 8 個 binary choice。

---

## step2-aggregate-strategy — Step 2 聚合策略（pairwise vs cluster+validate）

**信心：high**

### 學界共識
Entity resolution 透過 **blocking**（縮減候選 pair）＋ clustering 避免 exhaustive O(N²)。N=100–10,000 區間，學界確立的做法是兩階段：bi-encoder 向量預分群（保守 epsilon）以 O(n log m) KNN 取 top-k，再由 LLM 在 cluster 內 re-rank / 驗證成員。[In-context Clustering-based ER with LLMs](https://arxiv.org/html/2506.02509v1) 建議 set-size ~9、hierarchical clustering、transitivity exploitation，達成 5× API call 縮減與 150% 準確度提升。[Blocking & Filtering Survey](https://arxiv.org/pdf/1905.06167) 將 blocking 確立為實務 ER 的必要前置。Similarity threshold θ 為 domain-specific，經 F1 最大化校準，常見語意預設 0.75–0.95（Sentence-BERT 典型 0.8）。

### 業界主流
**Bi-encoder + cross-encoder re-ranking** 為生產標準：[Sentence Transformers Cross-Encoders](https://www.sbert.net/examples/cross_encoder/applications/README.html) 顯示 10k 句子 bi-encoder 編碼 5 秒 vs exhaustive cross-encoder 65 小時。orchestration 偏好 exact match → BM25 hybrid → vector → LLM 驗證的瀑布式（[Entity Resolution Playbook](https://www.minimalistinnovation.com/post/entity-resolution-orchestration-framework)），並對 0.6–0.8 confidence 區間設人工 review。

### 我們約束下的可行範圍
**Pairwise O(N²) 在 N=100–10k 不可行**（N~1k 以上即失控）。可行路線：向量單階分群（sqlite-vec cover-tree）→ 僅對模糊 cluster 邊界做 LLM 驗證，成本約 pairwise 的 ~5%，N=1k 全程 <200ms。

> ⚠️ **驗證修正（澄清，非硬錯）**：原 finding 寫「HDBSCAN or cover-tree via sqlite-vec」措辭誤導——**sqlite-vec 只提供向量 INDEX（cover-tree / brute-force）做 KNN，不提供 clustering 演算法**。HDBSCAN 是獨立的純 Go lib（Belval/hdbscan），若用需外掛整合。Phase 1 實作路徑應具體化為以下其一：(1) 接受 epsilon-ball 或 k-means 作為比 HDBSCAN 更簡單的替代；(2) clustering 在 indexing 前一次性離線跑；或 **(3) Phase 1 只用向量 indexing，把 clustering 整個 defer 到 Phase 2**。學界共識（2506.02509）支持 clustering-first，但純 Go 實作路徑需落地。

### PM 該拍板的 trade-off
**(A) Pairwise**（前期 LLM 便宜、per-pair 分數可供下游排序）僅在 N<100 嚴格成立（~5k LLM calls）。**(B) Cluster + validate**（先 block、少 API call、較高 latency）在 N=10k 時 LLM call 少 90%，需 threshold 調校。因 domain model 本就禁止 cross-source 聚合，B 對齊更佳。**研究員傾向 B**，且 rollout 上 Phase 1 先 ship 純向量分群（零 LLM、dogfood-ready），Phase 2 再加選擇性 LLM 驗證。起始 epsilon=0.80（Sentence-BERT 預設），於各 source_instance 內以 ground-truth F1 最大化精修。Two-stage（bi-encoder + LLM）對 Phase 1 是 overkill，defer 到 Phase 2。

---

## step2-same-source-granularity — Step 2「同來源」block key 粒度

**信心：medium**

### 學界共識
Blocking key 粒度存在根本權衡：太粗（source_type="all GitHub"）→ false positive；太細（source_subject="某 issue thread"）→ false negative（把真重複切進不同 block）。共識做法是 **hierarchical / multi-level blocking**：粗 key 高 recall 縮減候選，再以 meta-blocking 剪枝消 false positive（[Blocking Survey](https://arxiv.org/pdf/1905.06167)、[Supervised Meta-blocking, VLDB](http://www.vldb.org/pvldb/vol7/p1929-papadakis.pdf)）。Key 選擇依 domain cardinality 校準，無普世規則。

### 業界主流
> ⚠️ **驗證修正（citation issue）**：原 finding 稱 Dedupe 用「set-cover algorithm 學 blocking rules」**證據不支持**。實際上 [Dedupe](https://docs.dedupe.io/) 在訓練階段用 **active learning** 從 field-type 預設調校 blocking predicates，而非顯式 set-cover 最佳化。[Python Record Linkage Toolkit](https://recordlinkage.readthedocs.io/) 則允許明確的 per-attribute blocking（Sorted Neighbourhood）。兩者都把粒度決策交給學習規則或使用者選擇，避開硬編單層 hierarchy。

### 我們約束下的可行範圍
單 Go binary + sqlite-vec + Ollama/Claude 下，**只有兩個較粗策略可行**：(1) **source_type** — 成本最低、false positive 風險最高，需 meta-blocking 精修；(2) **source_instance** — 成本中等、風險中等，天然契合 hub-and-spoke。**source_subject 不建議**（窄而稀疏的 block、極高 false negative）。Hierarchical/meta-blocking 可行但需存 block graph metadata 並實作剪枝演算法（index 時 O(N²)，僅適 batch ingest 非 streaming）。學習式 blocking（Dedupe-style）需訓練資料與 active-learning UI，defer 到 Phase 2+。

### PM 該拍板的 trade-off
**(A) source_type**（all GitHub 為一 block）：最大 recall、最簡實作、需積極 meta-blocking 砍 false positive。**(B) source_instance**（同 repo）：前期降 false positive、尊重 connector 邏輯邊界，但可能漏跨 repo 的同一 goal。**研究員傾向 B**：對齊 connector-interface 契約（每個 Source 有 canonical instance），降低 meta-blocking LLM 負擔，實作簡單（`GROUP BY source_type, source_instance, cluster_id`），跨 instance 的 merge 留給 Step 3 cross-source long-tail。**MVP 跳過 meta-blocking**，用 eval harness 量 false positive rate，僅當 within-instance precision < 0.85 才加。

> ⚠️ **驗證修正（術語）**：原 finding 的「Weighted Node Centric Clustering」是非標準術語。正確學界用語為 **Weight Node Pruning (WNP) / Node-Centric Pruning**（Papadakis et al. 2013–2016 的 WNP/CNP/WEP/CEP），實作時請引用正確演算法名。

---

## step1-split-boundary — Step 1 切分邊界判斷（1 vs N work units）

**信心：medium**

### 學界共識
Schema 強制下 structured output 可靠度高：constrained decoding 顯著提升 schema 合規（[LLMStructBench](https://arxiv.org/pdf/2602.14743)、[SLOT](https://arxiv.org/html/2505.04016v1)）。Few-shot 普遍勝 zero-shot 10–25% absolute accuracy，exemplar **多樣性比數量重要**（[Commit Classification](https://arxiv.org/html/2605.02033v1)、[SEE-Few NER](https://arxiv.org/pdf/2210.05632)）。Precision-recall 權衡不可避免：[Semantic Chunking](https://arxiv.org/html/2410.13070v1) 顯示 over-segmentation 製造冗餘且不值算力——意味下游 merge 成本是真實的。

> ⚠️ **驗證修正 / 反證（多項，需強調）**：
> - **「99.5–99.9% schema 合規」無法證實**——LLMStructBench 論文存在但 PDF 無法萃取該數字；應軟化為「constrained decoding 顯著提升 schema 合規」不帶確切百分比。
> - **constrained decoding ≠ 語意正確**：ACL 2025 顯示受限時準確度掉 17.1%；constrained decoding 保證**結構**不保證**值**正確，驗證仍必要。
> - **Azure DevOps 引用張冠李戴**：該 blog 講的是 work item↔commit 雙向**連結**，不是 LLM 驅動的 entry 切分；應改引 untangling-commits 類論文。
> - **Mistral-7B schema 限制被略過**：7B 本地模型對 nested/conditional schema 會掙扎；複雜 schema 建議 Nemo 12B / Qwen 14B。
> - **few-shot 邊際遞減更陡**：k=5 後幾乎無提升，10-shot 比 2-shot 貴 ~5× token 卻近零增益。

### 業界主流
[Anthropic Claude Structured Outputs](https://platform.claude.com/docs/en/build-with-claude/structured-outputs) 用 constrained decoding，建議扁平 required 欄位優於深層 optional。[PARSE](https://arxiv.org/html/2510.08623v1) 把 JSON schema 當「自然語言契約」最佳化後驗證。Enterprise action-item 抽取（[AWS Nova](https://aws.amazon.com/blogs/machine-learning/meeting-summarization-and-action-item-extraction-with-amazon-nova/)）證實 few-shot + CoT + 結構化 list + 人工兜底的 pattern。注意：Claude structured outputs 為新近（2025 末）功能，PARSE 為替代框架而非共識。

### 我們約束下的可行範圍
可行：扁平 JSON schema（僅 required）+ few-shot + Claude structured outputs，**或** Ollama（mistral-7b/llama3.2 + SLOT 後處理 + embedding 驗證迴圈）。不可行：複雜 nested schema、100+ optional、union types（破壞 grammar 編譯）。短內容（one-liner）與長內容（大 PR）須分別處理：few-shot **必含 edge case**（"Bumped version"=1、"Fix bug + add test"=2、"Updated docs"=模糊）；Ollama 在 >4k token 退化。3–5 shots 為甜蜜點。sqlite-vec 可攜性需注意——純 Go 路線（viant/sqlite-vec）較不成熟，CGO 版破壞跨平台單 binary，WASM 模式需 ncruces/go-sqlite3。

### PM 該拍板的 trade-off
**(A) Over-split（悲觀）**：prompt「列出所有 distinct work units」+ `is_ambiguous` flag，下游用 embedding + 時間鄰近分群。保留 signal，但噪音高、需人工 merge cluster。**(B) Under-split（樂觀）**：prompt「只抽明確獨立單位」，接受合併、只在明確矛盾時切。圖更乾淨，但 context 邊界損失、難復原。**研究員傾向 A（over-split）**：因下游有人工 merge 安全閥，over-split 可用 embedding 分群復原、under-split 是有損的。Prompt 結構：`{entries:[{id,description,type,confidence}], total_count}`，4–5 個涵蓋變異的 few-shot，無 union/optional。記錄 `is_ambiguous`，5% 人工抽樣校準 drift。

---

## step3-relate-direction — Step 3 Layer B 因果方向性穩定化

**信心：high（但帶強反證）**

### 學界共識
LLM 因果方向性本質脆弱：模型 hidden state 編碼方向資訊（線性探針 ~97%），但 Yes/No 輸出無法可靠表達（反轉 pair ~50%）。核心是**輸出介面失效**：structured multiple-choice 恢復 ~99%（0.988）準確度，verbal Yes/No 僅 ~26%（[Causal Tongue-Tie](https://arxiv.org/html/2605.25891v1)）。Order sensitivity 主導，prompt engineering 無法解決（[Order Sensitivity](https://arxiv.org/pdf/2402.15637)）。Temporal RE（TimeBank/TempEval）的 symmetry/transitivity 全域約束能改善一致性，但完整 temporal closure 是 NP-complete。有標註資料時 **fine-tuning 勝 prompting 12.8–20.5 F1**（[Prompting vs Fine-tuning](https://arxiv.org/html/2406.16899)），但需跨越根本理解落差。

### 業界主流
兩個生產 pattern：(1) **Directional QA pivots**（[KnowQA](https://arxiv.org/html/2410.04752v1)）——雙向各問一次（"Is A caused by B?" / "Is B caused by A?"）；(2) **Directed graph encoders + iterative learning**（[iLIF](https://arxiv.org/html/2405.20608v1)）——用 difference feature (z_i − z_j) 捕捉不對稱。Constraint 後處理（symmetry/transitivity）廣泛用於 temporal。開源參考：[EventStoryLine](https://github.com/tommasoc80/EventStoryLine)、[BECauSE v2.0](https://aclanthology.org/W17-0812/)（binary，非方向性 fine-tune-ready）、[Ollama embeddings](https://docs.ollama.com/capabilities/embeddings)（零成本但對稱、無法表達方向）。

### 我們約束下的可行範圍
單 binary + sqlite-vec + Ollama/Claude：(1) **Directional QA pivots 可行**（~2 calls/pair）；(2) **constraint 後處理可行但受限**（NP-complete，只能對 <100 node 連通分量或貪婪近似，只增一致性不增表達力）；(3) **embedding + LLM 判斷部分可行**（embedding 對稱，方向仍需 LLM）；(4) **BECauSE fine-tune 不可行**（binary、需 GPU）；(5) **反向確認可行但有噪音**（~50% 反轉 pair 仍被判因果）。需 GPU/Python 的學術玩具（closure solver、BERT fine-tune、GNN ensemble）排除。

> ⚠️ **強反證（須明示，這是 high 信心但帶重大保留）**：
> - **Temporal-ordering bias 主導**：[Failure Modes on Narratives](https://openreview.net/forum?id=...)（2410.23884）與 Temporal Robustness（2503.17073）顯示 LLM 從**敘事時序**而非真因果推論，Llama 3.1/Gemma 2 在事件排序近 ~50% chance level。structured QA 在合成 CLadder 達 99% **不等於**真實 narrative causality——fine-tuning 文獻顯示此落差 12.8–20.5 F1 且 QA 補不滿。
> - **「Symmetry constraint 是免費一致性勝利」是錯的**：若 A causes B，symmetry 斷言「B has A」**因果上不連貫**。正確做法是：用矛盾偵測（不同抽取器的 A→B vs B→A 標記為衝突），**不要斷言反向邊**。對因果關係**只應施加 temporal precedence constraint**（若 A 時間先於 B，禁 B→A 邊），不可施 symmetry。
> - **Ollama 本地顯著不如 Claude**：7B 本地模型在 multi-step 推理失敗；「Phase 1 用 Ollama dogfood」有讓使用者誤信因果有效的風險。
> - **無生產級 LLM 因果系統 >80% F1**被引用——對 Phase 1 可行性是 red flag。

### PM 該拍板的 trade-off
**(1) Structured QA + graph constraints**：雙向問 + symmetry/有限 transitivity 後處理；structured 格式 ~99%、相容 binary、Ollama 全本地，但 2× API/pair、latency 略高。**(2) Iterative directed-graph learning**：LLM 分類 + edge difference vector 精修；單 pass，但需向量存儲、state machine 複雜、難 debug。**研究員傾向 (1)**，但**修正後**：(event_a, event_b, context) → 雙向 QA → 矛盾則預設 VAGUE；**用 temporal precedence 取代 symmetry**；defer transitivity（NP-complete）。蒐集反轉 pair 追蹤脆弱度，50+ 樣本後迭代。Ollama 僅供測試/debug，生產 causal 信號建議用 Claude。

---

## confidence-representation — 每條 edge 的 confidence 在 UI 如何呈現

**信心：medium**

### 學界共識
KG 不確定性視覺化研究（[NetHOPs](https://arxiv.org/abs/2108.09870)、[Bioinformatics Uncertainty Survey](https://www.frontiersin.org/journals/bioinformatics/articles/10.3389/fbinf.2022.793819/full)）顯示，confidence 的**靜態視覺編碼**（邊粗細、透明度、顏色）造成認知負荷與誤判——「連研究者都難正確判讀 error bar」。Animation/sampling（如 NetHOPs）藉知覺判斷 offload 頻率估計，勝過靜態編碼，但代價是需互動探索而非一眼判讀。

### 業界主流
業界把 confidence 分三層：(1) 高訊號 per-edge 編碼（粗細、solid vs dashed 二元 ~0.7+）；(2) power user 的 filter slider 漸進揭露；(3) 預設隱藏 + tooltip。Trust calibration（[AI UX Design Guide](https://www.aiuxdesign.guide/patterns/trust-calibration)）強調使用者誤判原始分數，應顯示 per-domain accuracy（"95% 確定" vs "在猜"）。

> ⚠️ **驗證修正（多項 overstated）**：
> - [yfiles](https://www.yfiles.com/resources/how-to/guide-to-visualizing-knowledge-graphs) **並未**建議 dashed/solid 二元編碼，而是 badge（confidence）+ 粗細（關係強度）+ 顏色（predicate）。原 finding 誇大廠商背書。
> - [yworks blog](https://www.yworks.com/blog/empowering-llm-development-visualization-knowledge-graphs) 不討論 confidence 視覺化（只談 layout/clustering）。
> - 「學界研究顯示二元勝連續」**缺引用且被反證**。
> - dashed line 的認知負荷優勢無實證；user study 顯示 dashed 受偏好但表現較差，僅能區分 ~3 級不確定（粗細/透明度可區分更多）。

### 我們約束下的可行範圍
單 binary + sqlite-vec：(1) solid/dashed 二元（≥0.7 solid，Ollama-cheap）✓；(2) opacity gradient（0.3–1.0，極少 client 數學）✓；(3) slider filter（零 LLM）✓；(4) NetHOPs animation（需即時 resample，高 token，**不建議 MVP**）✗；(5) tooltip calibration text（需 Summarize call，中成本，post-MVP 可行）。**避免**對終端使用者顯示原始 0.0–1.0 數字（連專家都誤判）。

### PM 該拍板的 trade-off
**(A) 二元閾值（dashed <0.7 / solid ≥0.7）+ slider**：UI 簡單、即時視覺分離、便宜。**(B) Gradient opacity + tooltip calibration text**：保留資訊保真度、可細粒度 filter，但需 Summarize call 成本 + 使用者素養訓練。

> ⚠️ **反證直接挑戰原傾向**：[arXiv:2602.01264](https://www.frontiersin.org)（2026 "Shades of Uncertainty", N=47 受控研究）發現**連續（saturation）編碼提升 perceived reliability 並幫使用者認知模型侷限**，而二元編碼提升瞬時信心卻降低真實理解。這直接牴觸「二元勝連續」之說。

**研究員傾向（修正後）**：MVP 用 **dashed/solid 二元 + 選配 slider**（實作便宜、零 LLM、familiar idiom），但**承認二元與連續為對等可行、權衡不同**（信心提升 vs 真實侷限認知），而非宣稱二元有研究背書。新增 hybrid 選項 C（gradient opacity + slider）供考慮。Phase 2 再加 per-entry confidence 敘事與 sampling animation。不對使用者顯示原始數字。

---

## phased-rollout-cutoff — 一次到位 vs 漸進式 rollout 的 cutoff

**信心：medium**

### 學界共識
KG 系統受益於**漸進形式化**（[Semantic Ladder](https://arxiv.org/pdf/2603.22136)），從 raw 到形式 ontology。LLM 對「跨異質來源語意統一、動態 schema、複雜推理」是**必要**，對 ontology 設計/品質精修是 nice-to-have（[LLM-KG Survey](https://arxiv.org/html/2510.20345v1)）。多跳推理自然分層為 question decomposition（無需 LLM）、breadth-first 檢索（純圖遍歷）、final synthesis（LLM）（[StepChain GraphRAG](https://arxiv.org/html/2510.02827v1)）。

> ⚠️ **驗證修正（critical citation error）**：原 finding **誤引 [iText2KG](https://arxiv.org/html/2409.03284v1)**——該論文 LLM（GPT-4）是**所有抽取階段的核心**，cosine similarity 只是 fallback matching，**並非「無需全域 LLM 推理的語意去重」**。finding 把「zero-shot（無訓練）」與「LLM-free（不需 LLM）」混為一談，錯誤。應修正為：LLMs 對跨異質來源語意統一是必要；Phase 3 的 cross-source 聚合（需 LLM entity fusion）才是真差異化，非 Phase 1 ingest。

### 業界主流
Enterprise KG 採 pilot：1–2 部門、3–5 來源、5–8 entity types、10–15 relation types、瞄準 1–2 高價值問題、90 天 Phase 1（[Improvado Guide](https://improvado.io/blog/enterprise-knowledge-graph)）。成功門檻是回答具體問題與可量測產出。[Microsoft GraphRAG](https://github.com/microsoft/graphrag/issues/741) 用 caching + 選擇性 community re-summarization 做增量 indexing。[Neo4j](https://neo4j.com/blog/genai/knowledge-graph-vs-vectordb-for-retrieval-augmented-generation/) 顯示 vector DB 為初期記憶、graph 層後加。

### 我們約束下的可行範圍
Phase 1（source-native，零 LLM）：ingest 正規化 entries、表面 source-native 信號（commit count/time delta、issue status、時序）。Phase 2（+ embeddings）：Ollama 本地 embed → sqlite-vec → 語意相似檢索。Phase 3（LLM 聚合 + relate）：分類、cross-source 分群、弱 edge 偵測。

> ⚠️ **反證推翻 Phase 1-only 價值論**：
> - [Temporal KG Retrieval](https://arxiv.org/abs/2510.16715)（2404.00492）顯示**單靠時序對 entity disambiguation 與多跳不足**；graph-based 在 aggregation query 勝 vector-only RAG 3×，在 5+ entity query 用 embedding 無顯式 graph 時近 0% 準確度。
> - heuristic 去重（無 LLM/embedding）在同義詞、typo、縮寫、多語、大小寫上**一致失敗**。
> - **結論修正**：Phase 1（純 temporal+keyword、無 embedding）**不足以**作為獨立 MVP，不應對工程師宣稱「tangible value」。**Phase 2（embeddings）是必要非可選**。sqlite-vec re-index 時間取決於 embedding 推理，非只儲存（1M×3072-dim ~8.5s/query）。

### PM 該拍板的 trade-off
**(A) Ship Phase 1+2 一次到位、defer Phase 3**：~6–8 週、零 LLM setup、全離線、交付可查詢的本地 truth source。**(B) Phase 1+2+3 全設計**：+2–3 週（eval + 整合），解鎖語意聚合 + cross-source relate（即護城河）。**研究員傾向 A（Phase 1+2 為 MVP，defer Phase 3）**，dogfood 驗證 data model/UI/schema 穩定後再投 LLM-heavy 推理。Ollama 為預設、Claude 為 config fallback。

> ⚠️ **但須注意 cross-source 反證**：把 Phase 3（cross-source 聚合）defer 意味 MVP **沒有 cross-source 能力**，這牴觸核心價值主張（見下節 cross-source-asymmetry 的 Layer C 反證）。**A 的前提是 Phase 1+2 必含 embeddings**——Phase 1 alone 已被反證排除。

---

## llm-failure-fallback — LLM 失敗與無效輸出的降級策略

**信心：medium**

### 學界共識
Constrained decoding 為結構化輸出黃金標準，但有 trade-off（嚴格約束改善合規卻犧牲推理任務語意準確）。失敗升級採 tiered：依瞬時性分類（rate limit → retry；憑證錯 → fail fast），exponential backoff + full jitter 防 thundering herd，circuit breaker 防級聯，graceful degradation 為最終 fallback。Validation-repair-retry 迴圈設 cap（2–3 次），無法解析則 flag 人工 review（[Draft-Conditioned Constrained Decoding](https://arxiv.org/pdf/2603.03305)、[Six Sigma Agent](https://arxiv.org/pdf/2601.22290)）。

### 業界主流
生產三層：retry + backoff + jitter（同步 3 次/30s，背景 5–7 次）、circuit breaker（如 60s 內 5 失敗跳閘）、graceful degradation（cached/簡化輸出）。[Portkey](https://portkey.ai/blog/retries-fallbacks-and-circuit-breakers-in-llm-apps/)、LiteLLM、Vercel AI SDK 實作。無效輸出用 JSON repair lib（Go: dinson/json-repair；[Rust: jsonrepair](https://crates.io/crates/jsonrepair)）修語法，再對 schema 驗證；耗盡則 flag 而非無限 retry（[Apxml](https://apxml.com/courses/prompt-engineering-llm-application-development/chapter-7-output-parsing-validation-reliability/handling-parsing-errors)、[TianPan](https://tianpan.co/blog/2026-03-11-llm-api-resilience-production)）。

> ⚠️ **驗證修正（citation issues）**：Portkey blog 的確切數字（3 attempts、30s、5 failures/60s）**論文未提，疑似杜撰/轉述**。Six Sigma Agent 論文主談 Byzantine voting 共識，非經典 failover。各數字閾值來自不相容來源，不應呈現為統一共識。

### 我們約束下的可行範圍
單 binary + sqlite-vec + Ollama/Claude：(1) **LLM 掛掉**——不要只 fall back 純向量分群；degraded mode 可用離線 BM25 keyword 分群或 cached similarity，或 halt 新推理但標記 entries `pending_llm` 下個 sync window retry。(2) **無效輸出**——嵌入 JSON repair lib → repair → validate → 用；repair 失敗則保持 mapping 不變 + `retry_count++`，2–3 次後 `manual_review=true` 跳過。(3) **circuit breaker**——in-process 3-state 存 sqlite（非 Redis），~200 行 Go。(4) **retry**——exponential backoff (1/2/4/8/16s) + ±20% jitter，30s timeout，只 retry 瞬時錯（5xx/429/timeout/network）。不可行：CRDT sync repair、parallel hedging、進階 constrained decoding（需 vLLM/Outlines）。

> ⚠️ **反證（須明示）**：
> - **Claude Structured Outputs（2026）達 <0.1% 失敗**——若用 Claude，原生 schema enforcement 優於自製 repair loop；Phase 1 建議優先 Claude structured outputs，除非隱私必須 Ollama。
> - **JSON repair 只修語法不修 schema 違規**——語意失敗仍需 retry，「repair 避免 retry」說法被削弱。
> - **Hybrid fallback 內部不一致**：cache embeddings 做 BM25 fallback 是矛盾（用的是 cached vector 非 BM25）；純 BM25 則損失語意品質，牴觸「無 degraded node」目標。
> - **Ollama CPU fallback 不可行**：CPU latency 退化 5–10×，工程師同時跑其他工作時無法用。
> - **Manual review queue 無界成長**：系統性失敗是 model regression 先兆，queue 會變瓶頸非安全閥。

### PM 該拍板的 trade-off
**選擇 1（LLM 不可用）**：**(A) Halt + queue**（標 `pending_llm`、下個 cycle retry，狀態乾淨但 outage 期阻塞）vs **(B) Hybrid fallback**（cached vector + BM25，管線續跑但品質低、需標記重跑）。**研究員傾向 A（halt + queue）**——Phase 1 更安全，hybrid 加 1–2 工程週測試。**選擇 2（無效輸出）**：**(A) Retry**（送錯誤回模型，2× cap，可能不收斂、燒 token）vs **(B) Repair + flag**（repair JSON、coerce、語意失敗則 `manual_review`，不 retry）。**研究員傾向 B**，retry 只保留給 API 失敗。**修正後建議**：若用 Claude 則優先其原生 structured outputs；數字閾值承認來源變異不呈現為統一共識；manual review queue 需附成長率估計與分流負擔。

---

## cross-source-asymmetry — 「禁聚合但可關聯」不對稱性是否自洽

**信心：medium**

### 學界共識
Entity resolution / alignment（連結）與 entity merging（合併為單一 node）是**不同操作**。連結可不合併：實體保持獨立、由關係捕捉對應。[Neo4j Agent Memory](https://neo4j.com/labs/agent-memory/explanation/resolution-deduplication/) 用 `SAME_AS` 軟連結，merging 保留給高信心 match。此不對稱（「禁聚合但可關聯」）對齊最佳實踐：Step 2 聚合（高確定性、source 內）+ Step 3 關聯（容忍不確定、軟 edge），同時保留 data lineage 與 schema isolation（[Entity Alignment Survey](https://arxiv.org/abs/1908.08210)、[Efficient ER on Heterogeneous Records](https://arxiv.org/pdf/1610.09500)）。

### 業界主流
[Neo4j](https://neo4j.com/blog/developer/entity-resolved-knowledge-graphs/) 用 `SAME_AS` 軟連結保留原始記錄。Senzing 用中介 node 經 `RESOLVES` 關係連結不合併。[Linkurious](https://linkurious.com/blog/entity-resolution-knowledge-graph/) 區分 entity resolution（去重）與 entity linking（跨來源關聯）。sqlite-vec/sqlite-vss 支援本地關係評分。

> ⚠️ **驗證修正（多項 overstated/未驗證）**：
> - Nature/Springer 引用在付費牆後，無法公開驗證。
> - Linkurious「區分 ER 與 linking 並支援 many-to-many」**被誇大**，其文件聚焦 consolidation。
> - Senzing「中介 node + RESOLVES」架構**文件未明確支持**（只提 attribution/lineage）。
> - **Neo4j `SAME_AS` 在 confidence 超過閾值時 merge，非低於**——低於閾值保持未連結，**牴觸 finding 的「軟連結優先」架構**。生產系統（Neo4j/Senzing/TigerGraph）因軟連結 UX 複雜而**偏向 source 內硬合併**，finding 的 soft-first 不如宣稱主流。

### 我們約束下的可行範圍
**完全可行**。Step 2 聚合（source 內、sqlite-vec 分群）便宜。Step 3 多層連結：Layer A（零成本 source-native，如時序鄰近、git branch）、Layer B（sqlite-vec embedding + 廉價 LLM）、Layer C（高 LLM、sampled cross-source）。Lineage 免費（junction table 存 entry.id, node.id, confidence, created_at）。

> ⚠️ **反證（須明示，挑戰 Layer C「可 defer」定位）**：
> - **LLM 成本現實**：GPT-4 對 500K pair 做 ER 要 $1,800；Layer C cross-source 在 1k–10k/day 預算下**除非積極 sampling 否則不可行**。
> - **sqlite-vec 效能天花板**：>200K–500K 向量急遽退化，1M×3072-dim query >100ms——「fully feasible」忽略此天花板。
> - **Layer C 不是可 defer 的 nice-to-have**：它是區隔 truth source 與本地 git parser 的**核心差異化**。Defer 到 post-MVP 意味 MVP 無 cross-source 聚合，**破壞核心價值主張**。在 1k–10k 預算下 Layer C 需積極 sampling 或變不可行。
> - junction table lineage 在規模下非微不足道；軟連結 UI（顯示 10 個「可能相同」node）需 clustering + confidence + merge affordance，非免費。

### PM 該拍板的 trade-off
**選擇 1（Merge-first vs Link-first）**：**(A) 積極**——source 內高信心（>0.95）自動 merge，Step 3 跨來源 relate（UX 簡單、損 audit trail）；**(B) 保守**——跳過 Step 2 merge，全軟連結 + confidence（保留 lineage、需 UI 確認）。**選擇 2（Layer C）**：**(A) 只 ship Layer A+B**，cross-source 連結由使用者驅動/defer；**(B) 實作 Layer C sampled**（每 batch 隨機 N 對）。**研究員傾向採用此不對稱性 as-is（學界業界皆站得住腳）**，Step 2 source 內分群 consolidate（immutable、audit 保留）、Step 3 三層 + confidence weight、永不刪 entries。**但修正**：Layer C 因是核心差異化，**MVP 即需 sampled 版本**而非全 defer；明示 sqlite-vec ~500K 天花板與軟連結 UX 成本。

---

## PM 決策表

| 決策 | 選項 A | 選項 B | 研究員傾向 | 信心 |
|---|---|---|---|---|
| **step2-aggregate-strategy** | Pairwise O(N²) loop（前期 LLM 便宜、per-pair 分數） | Cluster + LLM-validate（先 block、少 API call、需 θ 調校） | **B**（Phase 1 先純向量分群零 LLM，θ=0.80 起；Phase 2 加選擇性 LLM 驗證） | high |
| **step2-same-source-granularity** | source_type（all GitHub，最大 recall、需 meta-blocking） | source_instance（同 repo，前期降 FP、對齊 connector） | **B**（`GROUP BY source_type, source_instance`；MVP 跳過 meta-blocking，<0.85 precision 才加 WNP） | medium |
| **step1-split-boundary** | Over-split 悲觀（保 signal、下游 merge 兜底） | Under-split 樂觀（圖乾淨、context 損失難復原） | **A**（over-split + 扁平 schema + 3–5 edge-case few-shot + 人工 merge；constrained decoding 須配語意驗證） | medium |
| **step3-relate-direction** | Structured QA + graph constraints（~99% 結構格式、相容 binary） | Iterative directed-graph learning（單 pass、需向量、難 debug） | **A 但修正**（雙向 QA + **temporal precedence 取代 symmetry**、defer transitivity；真實 narrative 受時序 bias，生產用 Claude 非 Ollama） | high（帶強反證） |
| **confidence-representation** | 二元 dashed/solid（≥0.7）+ slider（簡單、便宜） | Gradient opacity + tooltip calibration（保真、需 Summarize call） | **A（MVP）但承認與 B 對等**（2026 研究示連續編碼提升真實侷限認知；不顯示原始數字；Phase 2 加敘事） | medium |
| **phased-rollout-cutoff** | Ship Phase 1+2 一次到位、defer Phase 3（~6–8 週、零 LLM、離線） | Phase 1+2+3 全設計（+2–3 週、解鎖護城河） | **A 但 Phase 2 embeddings 必要**（Phase 1 純 temporal+keyword 被反證排除；注意 defer Phase 3 = MVP 無 cross-source） | medium |
| **llm-failure-fallback** | LLM 掛：Halt + queue（狀態乾淨、outage 阻塞）；無效輸出：Retry | LLM 掛：Hybrid BM25 fallback；無效輸出：Repair + flag | **Halt+queue（A）+ Repair+flag（B）**（用 Claude 則優先原生 structured outputs <0.1%；JSON repair 只修語法；Ollama CPU fallback 不可行） | medium |
| **cross-source-asymmetry** | Merge-first 積極（自動合併 >0.95、損 audit） | Link-first 保守軟連結；Layer C：A 只 A+B vs B sampled | **採不對稱 as-is + Layer C sampled 進 MVP**（Layer C 是核心差異化非可 defer；1k–10k 預算需積極 sampling；sqlite-vec ~500K 天花板） | medium |

---

*產製方式：custom deep-research Workflow（`llm-pipeline-deep-research`, run `wf_ae3b81a2-5f7`）— 8 個鎖定問題各一條 research track（search-specialist agentType）→ 對抗式 citation/claim 驗證 → 單一 synthesizer 整合。17 agents / ~826k tokens / 469 tool-uses。前兩次改用 `auto-grill` 皆因 questioner 自選 schema-邊界題目而偏題、已 kill。*
