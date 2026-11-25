package schema

import (
	"cmd2api/ent/schema/mixins"
	"cmd2api/internal/domain"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// AdminKey 定义远程管理 API 密钥实体（adm- 前缀，仅入库哈希）。
type AdminKey struct {
	ent.Schema
}

func (AdminKey) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "admin_keys"},
	}
}

func (AdminKey) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
	}
}

func (AdminKey) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").
			NotEmpty().
			MaxLen(120),
		field.String("key_hash").
			Unique().
			NotEmpty().
			MaxLen(128),
		field.String("key_prefix").
			NotEmpty().
			MaxLen(32),
		field.String("scopes").
			Default("full").
			MaxLen(64),
		field.String("allowed_ips").
			Default("").
			MaxLen(1000),
		field.String("status").
			Default(domain.StatusActive).
			MaxLen(20),
		field.Time("last_used_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("expires_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (AdminKey) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("status"),
		index.Fields("key_prefix"),
	}
}
