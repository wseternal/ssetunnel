-- Add nullable "user_id" foreign key to existing tables
ALTER TABLE "public"."tokens" ADD COLUMN "user_id" bigint NULL;
ALTER TABLE "public"."tokens" ADD CONSTRAINT "tokens_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "public"."users" ("id");

ALTER TABLE "public"."pins" ADD COLUMN "user_id" bigint NULL;
ALTER TABLE "public"."pins" ADD CONSTRAINT "pins_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "public"."users" ("id");

ALTER TABLE "public"."admin_sessions" ADD COLUMN "user_id" bigint NULL;
ALTER TABLE "public"."admin_sessions" ADD CONSTRAINT "admin_sessions_user_id_fkey" FOREIGN KEY ("user_id") REFERENCES "public"."users" ("id");
