# Tasks: Revise `dev-cycle` Skill

- [x] **Task 1: Add Step 0.3 (Model Selection & Confirmation) to Phase 0**
  - Acceptance Criteria:
    - Step 0.3 clearly explains how to detect available models provided by the current agent/environment.
    - Deduced mapping is defined: High-Reasoning for `PLAN`, `REVIEW`, `SIMPLIFY`; Faster model for `IMPLEMENT`.
    - Includes a structured table template and explicit user confirmation requirement before advancing to Phase 1.
    - Exit gate of Phase 0 includes model confirmation.

- [x] **Task 2: Update Overview & Pipeline Flow Diagram**
  - Acceptance Criteria:
    - Overview diagram and summary list reflect model selection in Phase 0.
    - Phase 1, 2, 3, 4 summaries mention High-Reasoning vs Faster model usage.

- [x] **Task 3: Update Phase 1, Phase 2, Phase 3, Phase 4 Descriptions**
  - Acceptance Criteria:
    - Phase 1 (PLAN) explicitly instructs using the confirmed High-Reasoning model.
    - Phase 2 (IMPLEMENT) explicitly instructs using the confirmed Faster model.
    - Phase 3 (REVIEW) explicitly instructs using the confirmed High-Reasoning model.
    - Phase 4 (SIMPLIFY) explicitly instructs using the confirmed High-Reasoning model.

- [x] **Task 4: Update Phase 5 (WRAP-UP) and Pipeline Rules**
  - Acceptance Criteria:
    - Phase 5 recap table includes a row for Models Used (`High-Reasoning: ... / Faster: ...`).
    - Pipeline Rules note model selection/confirmation as a required Phase 0 gate.
