package schema

import (
	"cmd2api/ent/schema/mixins"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
)

// ModelPrice 定义标准模型定价库实体（单位：USD / 1M tokens 或统一标准单价）。
type ModelPrice struct {
	ent.Schema
}

func (ModelPrice) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "model_prices"},
	}
}

func (ModelPrice) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
	}
}

func (ModelPrice) Fields() []ent.Field {
	return []ent.Field{
		field.String("model").
			Unique().
			NotEmpty().
			MaxLen(150),
		field.Float("input_price").
			Default(0).
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}),
		field.Float("output_price").
			Default(0).
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}),
		field.Float("cache_read_price").
			Default(0).
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}),
		field.Float("cache_write_price").
			Default(0).
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,10)"}),
		field.String("notes").
			Default("").
			MaxLen(500),
	}
}
