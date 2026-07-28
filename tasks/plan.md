# Implementation Plan: Revise `dev-cycle` Skill for Dual-Model Configuration

## Overview
Revise the `dev-cycle` skill at `/Users/jiangzhaohua/.agents/skills/dev-cycle/SKILL.md` to introduce model selection and deduction in Phase 0 Setup. The skill will deduce and prompt the user to confirm/choose two distinct models provided by the agent platform:
1. **High-Reasoning Model**: for `PLAN`, `REVIEW`, and `SIMPLIFY` (upfront architecture, deep analysis, quality audit, safe complexity reduction).
2. **Faster Execution Model**: for `IMPLEMENT` (fast TDD code generation, rapid test-driven iteration).

Phase 0 will deduce these models based on available models provided by the agent/environment, present the assigned models for the 4 steps, and require explicit user confirmation before advancing to Phase 1.

## Proposed Changes

### 1. Update Overview Diagram and Phase Descriptions
In `SKILL.md`:
- Update Phase 0 summary to include model selection.
- Update pipeline flow diagram:
  ```
  Phase 0  SETUP       (branch verification + complexity assessment + model selection & confirmation)
  Phase 1  PLAN        (high-reasoning model: parallel multi-perspective planning → critical review → synthesis → user approval)
  Phase 2  IMPLEMENT   (faster model: incremental vertical slices / TDD)
  Phase 3  REVIEW      (high-reasoning model: six-axis code review)
  Phase 4  SIMPLIFY    (high-reasoning model: complexity reduction)
  Phase 5  WRAP-UP     (squash commits + user recap)
  ```

### 2. Phase 0 Setup Enhancements
Add **Step 0.3: Model Selection & Confirmation**:
- **Get Available Models:** Query or inspect the agent runtime's provided/configured model list or available models (e.g. `Gemini 3.6 Pro / Flash (High)`, `Claude 3.7 Sonnet / Thinking`, `GPT-4o`, `O1/O3`, etc. or current model context).
- **Deduce Two Models:**
  - High-Reasoning Model (e.g. Pro / Thinking / High reasoning): Assigned to `PLAN`, `REVIEW`, `SIMPLIFY`.
  - Faster Model (e.g. Flash / Fast execution): Assigned to `IMPLEMENT`.
- **User Confirmation Gate:** Present the model allocation table to the user and ask for confirmation before proceeding to Phase 1:
  | Step / Phase | Model Role | Selected Model | Rationale |
  |---|---|---|---|
  | **Phase 1: PLAN** | High-Reasoning | `<model-name>` | Upfront architecture, synthesis, risk mitigation |
  | **Phase 2: IMPLEMENT** | Faster Execution | `<model-name>` | Speed, rapid TDD iteration |
  | **Phase 3: REVIEW** | High-Reasoning | `<model-name>` | Six-axis deep quality audit |
  | **Phase 4: SIMPLIFY** | High-Reasoning | `<model-name>` | Safe refactoring & complexity reduction |
- Update Phase 0 Exit Gate:
  - Models for the 4 steps determined and explicitly confirmed by the user.

### 3. Update Phase 1 (PLAN)
- Explicitly mandate launching research agents and doing synthesis using the confirmed **High-Reasoning Model**.

### 4. Update Phase 2 (IMPLEMENT)
- Explicitly mandate performing code generation, TDD cycles (RED -> GREEN -> REFACTOR) using the confirmed **Faster Model** for speed and execution efficiency.

### 5. Update Phase 3 (REVIEW)
- Explicitly mandate carrying out the six-axis code review using the confirmed **High-Reasoning Model**.

### 6. Update Phase 4 (SIMPLIFY)
- Explicitly mandate analyzing and simplifying code using the confirmed **High-Reasoning Model**.

### 7. Pipeline Rules & Recap Updates
- Update Phase 5 (WRAP-UP) recap table to report model selections used for the cycle.
- Update Pipeline Rules to reflect mandatory model confirmation gate in Phase 0.

## Task List

### Phase 1: Edit `SKILL.md`
- [ ] Task 1: Add Step 0.3 (Model Selection & Confirmation) to Phase 0 and update Phase 0 exit gate.
- [ ] Task 2: Update Overview diagram and phase summaries to explicitly highlight the model assignment for `PLAN`, `IMPLEMENT`, `REVIEW`, and `SIMPLIFY`.
- [ ] Task 3: Update Phase 1 (PLAN), Phase 2 (IMPLEMENT), Phase 3 (REVIEW), and Phase 4 (SIMPLIFY) instructions to reference using the assigned High-Reasoning or Faster model.
- [ ] Task 4: Update Phase 5 (WRAP-UP) recap summary table and Pipeline Rules to include model tracking.

### Checkpoint & Verification
- [ ] Review updated `SKILL.md` against all user requirements.
- [ ] Confirm clean formatting and accurate instructions.

## Open Questions / Assumptions
- The skill instructions guide the agent running `dev-cycle` to discover/detect available models provided by its execution environment or active configuration, infer high-reasoning vs faster execution options, and present them in a clear table for user approval.
