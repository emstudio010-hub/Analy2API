package schema

import (
	"cmd2api/ent/schema/mixins"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
)

// SystemSetting 定义系统运行时动态键值配置实体。
type SystemSetting struct {
	ent.Schema
}

func (SystemSetting) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "system_settings"},
	}
}

func (SystemSetting) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
	}
}

func (SystemSetting) Fields() []ent.Field {
	return []ent.Field{
		field.String("key").
			Unique().
			NotEmpty().
			MaxLen(120),
		field.String("value").
			Default("").
			SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.String("description").
			Default("").
			MaxLen(500),
	}
}
