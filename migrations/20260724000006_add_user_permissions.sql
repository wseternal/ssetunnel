-- Add per-user permission columns.
-- perm_connect: allow connecting to agent tunnels via entry TCP.
-- perm_agent:   allow registering and maintaining an agent tunnel.
-- Both default to true for backward compatibility (all existing users gain both).

ALTER TABLE "public"."users" ADD COLUMN "perm_connect" boolean NOT NULL DEFAULT true;
ALTER TABLE "public"."users" ADD COLUMN "perm_agent" boolean NOT NULL DEFAULT true;

-- Migrate existing agent-role users: keep role='user' with perm_agent=true, perm_connect=false.
UPDATE "public"."users" SET role = 'user', perm_connect = false WHERE role = 'agent';
