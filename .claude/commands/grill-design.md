# /grill-design — 多 agent 自動 grill 設計樹

呼叫 `auto-grill` workflow,用 1 提問 + 3 多視角評審 + 裁決者 + 記錄者,
把任何設計樹收斂到具體決定 + 留下持久記錄。技術選型必查最新資料附 URL 來源。

## 用法
```
/grill-design <topic>
```
`<topic>` 例如:「workingbad Phase 0/1 地基」、「Slack sink 推送策略」、「AI provider 切換 UX」。

## 執行步驟

1. **收集脈絡**(若使用者未明示則詢問):
   - `topic`:這次 grill 主題(用於記錄檔名與 prompt 標題)
   - `specPaths`:當 ground truth 的檔案清單。預設帶:
     - `/Users/lileon/project/workingbad/.claude/CLAUDE.md`
     - `/Users/lileon/project/workingbad/docs/ROADMAP.md`
     - `/Users/lileon/project/workingbad/.claude/skills/truth-source-schema/SKILL.md`
     - `/Users/lileon/project/workingbad/.claude/skills/connector-interface/SKILL.md`
   - `groundDecisions`:從 spec 摘要已拍板部分(字串,讓提問者不重問)
   - `openHints`:已知未解 branches(字串,可空,提問者可擴充)

2. **呼叫 workflow**:
   ```
   Workflow({
     name: 'auto-grill',
     args: { topic, specPaths, groundDecisions, openHints, maxRounds: 14 }
   })
   ```
   背景跑,完成會收到通知。

3. **完成後落地**:
   - 從結果取 `scribe.doc_markdown`,Write 到 `docs/grill/YYYY-MM-DD-<topic-slug>.md`
   - 回報 `exec_summary`、`locked_technical`、`needs_user`、`spec_changes`
   - 對 `needs_user`(product-judgment)用 `AskUserQuestion` 問用戶確認
   - 對確定的 `locked_technical` + 用戶確認的 `needs_user`,Edit 對應 spec 檔
   - 不要擅自鎖 product-judgment 項

## 何時用
- 設計階段 / Phase 邊界,要把多面向設計**收斂到具體決定**
- 想用多視角(務實 / 架構 / 體驗)壓力測試假設
- 技術選型需要**最新資料 + 來源依據**(防訓練資料過時)
- 一次性研討會代替多輪互動 grill-me(成本高、時間久,但收斂深)

## 何時不用
- 單一明確決定 → 直接決定或用互動 `grill-me`
- 純實作問題 → `search-first` 或直接 code
- 已知答案的瑣事 → 別浪費 60+ agents 跑空

## 成本提醒
- 一次完整跑 ≈ 60–75 個 agent、3–5M token、20–60 分鐘背景時間。
- 建議跑前先用 grill-me 或聊天**鎖定大方向 + 寫進 groundDecisions**,讓 auto-grill 只解開放 branches,避免重新討論已決策。
