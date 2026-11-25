package schema

import (
	"cmd2api/ent/schema/mixins"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// PromptTemplate 定义系统提示词模板实体。
type PromptTemplate struct {
	ent.Schema
}

func (PromptTemplate) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "prompt_templates"},
	}
}

func (PromptTemplate) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
	}
}

func (PromptTemplate) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").
			NotEmpty().
			MaxLen(120),
		field.String("category").
			Default("general").
			MaxLen(64),
		field.String("content").
			NotEmpty().
			SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.String("description").
			Default("").
			MaxLen(500),
		field.Bool("is_builtin").
			Default(false),
		field.Int("priority").
			Default(100),
	}
}

func (PromptTemplate) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("category", "priority"),
		index.Fields("is_builtin"),
	}
}
