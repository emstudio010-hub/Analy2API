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

// ContainerNode 定义 AntiGravity / CLIProxyAPI 双模容器与远程节点实体。
type ContainerNode struct {
	ent.Schema
}

func (ContainerNode) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "container_nodes"},
	}
}

func (ContainerNode) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
	}
}

func (ContainerNode) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").
			NotEmpty().
			MaxLen(120),
		field.String("mode").
			Default(domain.ContainerModeLocalDocker).
			MaxLen(32),
		field.String("docker_image").
			Default("routerforme/cliproxyapi:latest").
			MaxLen(255),
		field.String("container_name").
			Default("").
			MaxLen(128),
		field.String("container_id").
			Default("").
			MaxLen(128),
		field.String("host_ip").
			Default("127.0.0.1").
			MaxLen(255),
		field.String("auth_dir").
			Default("").
			MaxLen(512),
		field.Int("host_port").
			Default(8317),
		field.String("management_key_enc").
			Default("").
			SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.String("api_key_enc").
			Default("").
			SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.String("proxy_url").
			Default("").
			MaxLen(512),
		field.String("status").
			Default("stopped").
			MaxLen(32),
		field.Int("auth_files_count").
			Default(0),
		field.Time("last_sync_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.String("last_error").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.JSON("extra", map[string]any{}).
			Default(func() map[string]any { return map[string]any{} }).
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
	}
}

func (ContainerNode) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("mode"),
		index.Fields("status"),
	}
}
