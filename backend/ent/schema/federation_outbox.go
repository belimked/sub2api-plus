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

// FederationOutbox 联邦发件箱（POC）：仅记录 user.upsert 事件，
// 供外部 pusher 轮询投递到海外部署。写入与触发它的业务变更同事务。
type FederationOutbox struct {
	ent.Schema
}

func (FederationOutbox) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "federation_outbox_events"},
	}
}

func (FederationOutbox) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
	}
}

func (FederationOutbox) Fields() []ent.Field {
	return []ent.Field{
		field.String("aggregate_type").MaxLen(32).NotEmpty(),
		field.String("aggregate_id").MaxLen(64).NotEmpty(),
		field.String("event_type").MaxLen(64).NotEmpty(),
		field.String("payload").
			SchemaType(map[string]string{dialect.Postgres: "text"}),
		// pending -> in_flight -> delivered | failed. in_flight rows a crashed
		// pusher left behind are picked back up once next_retry_at elapses.
		field.String("status").MaxLen(16).Default("pending"),
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

func (FederationOutbox) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("status"),
		index.Fields("status", "next_retry_at"),
	}
}
