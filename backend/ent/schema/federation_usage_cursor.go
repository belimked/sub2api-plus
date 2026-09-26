package schema

import (
	"github.com/LuckyKuang/sub2api-plus/ent/schema/mixins"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// FederationUsageCursor 记录 federation-usage-tailer（海外→大陆用量投递）的
// 游标：每个 scope 一行，last_delivered_id 是已成功应用到大陆余额的最大
// usage_log.id。严格按顺序投递、不跳过失败行——见
// openspec/changes/federation-usage-tailer/proposal.md 里的取舍说明。
type FederationUsageCursor struct {
	ent.Schema
}

func (FederationUsageCursor) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "federation_usage_cursors"},
	}
}

func (FederationUsageCursor) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
	}
}

func (FederationUsageCursor) Fields() []ent.Field {
	return []ent.Field{
		field.String("scope").MaxLen(64).NotEmpty(),
		field.Int64("last_delivered_id").Default(0),
		field.Int("attempts").Default(0),
		field.String("last_error").
			SchemaType(map[string]string{dialect.Postgres: "text"}).
			Optional().
			Nillable(),
		field.Time("next_retry_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (FederationUsageCursor) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("scope").Unique(),
	}
}
