schema "public" {
  comment = "standard public schema"
}

table "users" {
  schema = schema.public
  column "id" {
    null = false
    type = bigint
    identity {
      generated = BY_DEFAULT
    }
  }
  column "username" {
    null = false
    type = text
  }
  column "password_hash" {
    null = false
    type = text
  }
  column "totp_secret" {
    null    = false
    type    = text
    default = ""
  }
  column "role" {
    null    = false
    type    = text
    default = "user"
  }
  column "created_at" {
    null    = false
    type    = timestamptz
    default = sql("CURRENT_TIMESTAMP")
  }
  column "perm_connect" {
    null    = false
    type    = boolean
    default = true
  }
  column "perm_agent" {
    null    = false
    type    = boolean
    default = true
  }
  column "disabled_at" {
    null = true
    type = timestamptz
  }
  primary_key {
    columns = [column.id]
  }
  index "users_username_idx" {
    unique  = true
    columns = [column.username]
  }
}

table "tokens" {
  schema = schema.public
  column "id" {
    null = false
    type = bigint
    identity {
      generated = BY_DEFAULT
    }
  }
  column "digest" {
    null = false
    type = text
  }
  column "role" {
    null = false
    type = text
  }
  column "description" {
    null = true
    type = text
  }
  column "created_at" {
    null    = false
    type    = timestamptz
    default = sql("CURRENT_TIMESTAMP")
  }
  column "expires_at" {
    null = true
    type = timestamptz
  }
  column "revoked_at" {
    null = true
    type = timestamptz
  }
  column "user_id" {
    null = true
    type = bigint
  }
  primary_key {
    columns = [column.id]
  }
  index "tokens_digest_idx" {
    unique  = true
    columns = [column.digest]
  }
  foreign_key "tokens_user_id_fkey" {
    columns     = [column.user_id]
    ref_columns = [table.users.column.id]
  }
}

table "user_sessions" {
  schema = schema.public
  column "id" {
    null = false
    type = bigint
    identity {
      generated = BY_DEFAULT
    }
  }
  column "user_id" {
    null = false
    type = bigint
  }
  column "digest" {
    null = false
    type = text
  }
  column "created_at" {
    null    = false
    type    = timestamptz
    default = sql("CURRENT_TIMESTAMP")
  }
  column "expires_at" {
    null = false
    type = timestamptz
  }
  primary_key {
    columns = [column.id]
  }
  index "user_sessions_digest_idx" {
    unique  = true
    columns = [column.digest]
  }
  foreign_key "user_sessions_user_id_fkey" {
    columns     = [column.user_id]
    ref_columns = [table.users.column.id]
  }
}

table "agents" {
  schema = schema.public
  column "id" {
    null = false
    type = bigint
    identity {
      generated = BY_DEFAULT
    }
  }
  column "agent_id" {
    null = true
    type = text
  }
  column "allowed_targets" {
    null    = false
    type    = text_array
    default = sql("'{\"127.0.0.1:*\"}'")
  }
  column "description" {
    null    = false
    type    = text
    default = ""
  }
  column "created_at" {
    null    = false
    type    = timestamptz
    default = sql("CURRENT_TIMESTAMP")
  }
  column "updated_at" {
    null    = false
    type    = timestamptz
    default = sql("CURRENT_TIMESTAMP")
  }
  primary_key {
    columns = [column.id]
  }
  index "agents_agent_id_idx" {
    unique  = true
    columns = [column.agent_id]
  }
}

table "recovery_codes" {
  schema = schema.public
  column "id" {
    null = false
    type = bigint
    identity {
      generated = BY_DEFAULT
    }
  }
  column "user_id" {
    null = false
    type = bigint
  }
  column "code_digest" {
    null = false
    type = text
  }
  column "used_at" {
    null = true
    type = timestamptz
  }
  column "created_at" {
    null    = false
    type    = timestamptz
    default = sql("CURRENT_TIMESTAMP")
  }
  primary_key {
    columns = [column.id]
  }
  index "recovery_codes_code_digest_idx" {
    unique  = true
    columns = [column.code_digest]
    where   = "used_at IS NULL"
  }
  index "recovery_codes_user_id_idx" {
    columns = [column.user_id]
    where   = "used_at IS NULL"
  }
  foreign_key "recovery_codes_user_id_fkey" {
    columns     = [column.user_id]
    ref_columns = [table.users.column.id]
    on_delete   = CASCADE
  }
}
