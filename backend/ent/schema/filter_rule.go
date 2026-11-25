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

// FilterRule 定义内容风控与脱敏规则实体。
//
// 对应三类动作：prune（删减）、rewrite（整句/模板重写）、replace（同义安全名词替换）。
// 仅作用于系统提示、工具描述与 Harness 注入片段，绝不篡改用户自然提问。
type FilterRule struct {
	ent.Schema
}

func (FilterRule) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "filter_rules"},
	}
}

func (FilterRule) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
	}
}

func (FilterRule) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").
			MaxLen(120).
			NotEmpty(),
		field.String("kind").
			MaxLen(32).
			NotEmpty(),
		field.String("pattern").
			NotEmpty().
			SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.String("replacement").
			Default("").
			SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.Bool("is_regex").
			Default(false),
		field.Bool("case_sensitive").
			Default(false),
		field.Bool("word_boundary").
			Default(true),
		field.String("scope").
			MaxLen(64).
			Default("all_injected"),
		field.Int("priority").
			Default(100),
		field.Bool("enabled").
			Default(true),
		field.Bool("is_builtin").
			Default(false),
		field.String("description").
			Default("").
			MaxLen(500),
	}
}

func (FilterRule) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("kind", "priority"),
		index.Fields("enabled"),
	}
}
