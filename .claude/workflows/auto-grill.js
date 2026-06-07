// auto-grill — Multi-agent design-convergence harness.
//
// Per round: 1 questioner picks next open branch → 3 lens-diverse answerers
// (web-sourced for tech selections) → 1 synthesizer reconciles into a decision
// (recording dissent). Loop until tree converges or maxRounds. A final scribe
// agent produces a durable markdown record.
//
// args (all optional):
//   topic          string  — what we're grilling (used in titles / prompts)
//   specPaths      string[] — files to read as ground truth
//   groundDecisions string  — already-decided context (don't re-litigate)
//   openHints      string  — known unresolved branches (seed; questioner may expand)
//   maxRounds      number   — cap on rounds (default 14)
//   lenses         string[] — exactly 3 answerer perspectives (default: 務實/架構/體驗)
//
// returns: { roundCount, scribe: { doc_markdown, exec_summary, locked_technical,
//                                  needs_user, spec_changes } }
//
// caller is expected to Write scribe.doc_markdown to a docs/grill/ file.

export const meta = {
  name: 'auto-grill',
  description: 'Multi-agent design-convergence grill: 1 questioner + 3 lens-diverse answerers (web-sourced for tech) + 1 synthesizer per round, looped to convergence, then a scribe records everything to durable markdown.',
  phases: [
    { title: 'Grill rounds' },
    { title: 'Record' },
  ],
}

const args = (typeof input !== 'undefined' && input && input.args) ? input.args : {}

const TOPIC = args.topic || '設計'
const SPEC_PATHS = Array.isArray(args.specPaths) ? args.specPaths : []
const GROUND = args.groundDecisions || '(無已拍板 ground truth)'
const OPEN_HINT = args.openHints || '(由提問者自行發掘 open branches)'
const MAX = Number.isFinite(args.maxRounds) && args.maxRounds > 0 ? args.maxRounds : 14
const LENSES = (Array.isArray(args.lenses) && args.lenses.length === 3)
  ? args.lenses
  : [
      '務實派 — 最小可行、當前 phase 先動得了、避免過度設計、傾向延後',
      '架構派 — 長期正確、可擴展、避免日後 breaking/重寫、預留接縫',
      '工程師體驗/成本/隱私派 — DX、time-to-value、AI 成本、本地隱私',
    ]
const SPEC = SPEC_PATHS.join('\n')

const QUESTION_SCHEMA = {
  type: 'object', additionalProperties: false,
  properties: {
    is_done: { type: 'boolean' },
    branch: { type: 'string' },
    question: { type: 'string' },
    why_it_matters: { type: 'string' },
    decision_kind: { type: 'string', enum: ['technical', 'product-judgment'] },
    recommendation: { type: 'string' },
  },
  required: ['is_done', 'branch', 'question', 'why_it_matters', 'decision_kind', 'recommendation'],
}

const ANSWER_SCHEMA = {
  type: 'object', additionalProperties: false,
  properties: {
    lens: { type: 'string' },
    position: { type: 'string' },
    rationale: { type: 'string' },
    sources: { type: 'array', items: { type: 'string' } },
    tradeoffs: { type: 'string' },
    confidence: { type: 'string', enum: ['high', 'medium', 'low'] },
  },
  required: ['lens', 'position', 'rationale', 'sources', 'tradeoffs', 'confidence'],
}

const DECISION_SCHEMA = {
  type: 'object', additionalProperties: false,
  properties: {
    decision: { type: 'string' },
    rationale: { type: 'string' },
    agreement: { type: 'string', enum: ['consensus', 'majority', 'split'] },
    dissent: { type: 'string' },
    sources: { type: 'array', items: { type: 'string' } },
    decision_kind: { type: 'string', enum: ['technical', 'product-judgment'] },
    needs_user_confirmation: { type: 'boolean' },
    spec_impact: { type: 'string' },
  },
  required: ['decision', 'rationale', 'agreement', 'dissent', 'sources', 'decision_kind', 'needs_user_confirmation', 'spec_impact'],
}

const SCRIBE_SCHEMA = {
  type: 'object', additionalProperties: false,
  properties: {
    doc_markdown: { type: 'string' },
    exec_summary: { type: 'string' },
    locked_technical: { type: 'array', items: { type: 'string' } },
    needs_user: { type: 'array', items: { type: 'string' } },
    spec_changes: { type: 'array', items: { type: 'string' } },
  },
  required: ['doc_markdown', 'exec_summary', 'locked_technical', 'needs_user', 'spec_changes'],
}

const rounds = []
const log = []

phase('Grill rounds')
for (let i = 0; i < MAX; i++) {
  const logStr = log.length ? JSON.stringify(log) : '(尚無)'

  const q = await agent(
    `你是 grill-me 的提問者(主題:${TOPIC}),走設計樹找下一個最該解的設計問題。
${SPEC ? `先 Read 這些 spec 當 ground truth:\n${SPEC}\n` : ''}
${GROUND}
${OPEN_HINT}
本 session 已解回合(問題→決定):${logStr}
請提出**下一個最重要、尚未解決**的設計問題(一次一個,別重問已解的)。
優先序:會牽動 schema/interface 的 > 影響後續實作的 > 純產品主觀項(命名/時間預算)。
若重要技術 branch 都已解、只剩 trivial 或需老闆主觀拍板項,設 is_done=true。
標 decision_kind(technical=可用資料/邏輯判定;product-judgment=需人主觀),附 recommendation。`,
    { schema: QUESTION_SCHEMA, phase: 'Grill rounds', label: `Q${i + 1}` }
  )
  if (!q || q.is_done) { if (q) rounds.push({ done: true, note: q.recommendation }); break }

  const answers = await parallel(LENSES.map((lens, k) => () =>
    agent(
      `你是設計評審,視角=「${lens}」。
${SPEC ? `先 Read spec 當 ground truth:\n${SPEC}\n` : ''}
${GROUND}
本 session 已解:${logStr}
要評審的問題【${q.branch}】:${q.question}
背景:${q.why_it_matters}
從你的視角給明確 position + rationale + tradeoffs + confidence。
**若涉及技術選型/版本/函式庫,必須用 WebSearch/WebFetch 查最新資料並在 sources 附 URL,避免技術過時。**
須與已拍板方向相容;若你認為某已決策該翻案,需附強證據並在 rationale 明說。簡潔。`,
      { schema: ANSWER_SCHEMA, phase: 'Grill rounds', label: `A${i + 1}.${k + 1}` }
    )
  ))

  const decision = await agent(
    `你是 grill-me 的裁決者。
問題【${q.branch}】:${q.question}
提問者建議:${q.recommendation}
三位評審意見(JSON):${JSON.stringify(answers.filter(Boolean))}
${GROUND}
已解 log:${logStr}
綜合成**一個具體、可執行的決定**,與專案方向一致。
標 agreement(consensus/majority/split);在 dissent 保留有價值的不同意見供迭代。列 sources(彙整評審的)。
判 decision_kind;若 product-judgment(命名、時間預算、需老闆主觀取捨)設 needs_user_confirmation=true 但仍給推薦默認。
spec_impact 寫清楚要改哪個檔/欄位。`,
    { schema: DECISION_SCHEMA, phase: 'Grill rounds', label: `S${i + 1}` }
  )

  rounds.push({ q, answers: answers.filter(Boolean), decision })
  log.push({
    branch: q.branch,
    question: q.question,
    decision: decision ? decision.decision : null,
    needs_user: decision ? decision.needs_user_confirmation : null,
  })
}

phase('Record')
const scribe = await agent(
  `你是記錄者。把這場 auto-grill(主題:${TOPIC}) 的完整結果整理成交付物。
所有回合(JSON):${JSON.stringify(rounds)}
產出:
1) doc_markdown:完整可讀 markdown 記錄。標題「# Auto-Grill 設計收斂記錄 — ${TOPIC}」。每回合一節:問題(branch+why)、三視角各一段摘要(含 confidence 與 sources URL)、**最終決定**(含 agreement)、保留的歧見。技術決定務必保留 sources。
2) exec_summary:3-6 行。
3) locked_technical:可直接鎖定的技術決定清單(每條一行)。
4) needs_user:需老闆主觀拍板的項 + 推薦默認(每條一行)。
5) spec_changes:建議改哪些 spec 檔/欄位(每條一行)。
忠實反映,不要編造未發生的討論。`,
  { schema: SCRIBE_SCHEMA, phase: 'Record', label: 'scribe' }
)

return { roundCount: rounds.length, scribe }
