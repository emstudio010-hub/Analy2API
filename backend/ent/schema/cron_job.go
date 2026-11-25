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

// CronJob 定义基于 Cron 表达式的定时任务实体。
//
// 支持定时自动领奖励（harvest）、定时自动测试探活（account_check）、
// 定时代理 IP 巡检（proxy_test）、定时积分余额刷新（balance_refresh）、
// 定时 Token 续期（token_refresh）及组合任务（check_and_harvest）。
type CronJob struct {
	ent.Schema
}

func (CronJob) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "cron_jobs"},
	}
}

func (CronJob) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
	}
}

func (CronJob) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").
			MaxLen(120).
			NotEmpty(),
		field.String("description").
			Default("").
			MaxLen(500),
		field.String("cron_expr").
			MaxLen(100).
			NotEmpty(),
		field.String("timezone").
			Default("Asia/Shanghai").
			MaxLen(64),
		field.String("action").
			MaxLen(40).
			NotEmpty(),
		field.String("target_scope").
			Default("all").
			MaxLen(32),
		field.Int64("group_id").
			Optional().
			Nillable(),
		field.JSON("account_ids", []int64{}).
			Optional().
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
		field.JSON("options", map[string]any{}).
			Optional().
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
		field.Bool("enabled").
			Default(true),
		field.Bool("is_builtin").
			Default(false),
		field.Time("last_run_at").
			Optional().
			Nillable(),
		field.String("last_status").
			Default("").
			MaxLen(32),
		field.Int("last_duration_ms").
			Default(0),
		field.String("last_summary").
			Default("").
			MaxLen(1000),
		field.JSON("last_result", map[string]any{}).
			Optional().
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
		field.JSON("run_history", []map[string]any{}).
			Optional().
			SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
	}
}

func (CronJob) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("enabled"),
		index.Fields("action"),
	}
}
