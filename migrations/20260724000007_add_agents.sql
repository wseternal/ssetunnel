-- Agent configuration table for routing and target allowlists.
-- agent_id IS NULL is the default row: config applied to all agents
-- that don't have an explicit per-agent row.
-- Per-agent rows are created only when the admin explicitly configures overrides.

CREATE TABLE "public"."agents" (
    "id" bigserial PRIMARY KEY,
    "agent_id" text UNIQUE,
    "allowed_targets" text[] NOT NULL DEFAULT '{"127.0.0.1:*"}',
    "description" text NOT NULL DEFAULT '',
    "created_at" timestamptz NOT NULL DEFAULT now(),
    "updated_at" timestamptz NOT NULL DEFAULT now()
);

-- Seed the default row (agent_id IS NULL = defaults for all agents).
INSERT INTO "public"."agents" ("agent_id", "allowed_targets", "description")
    VALUES (NULL, '{"127.0.0.1:*"}', 'Default configuration for all agents');
