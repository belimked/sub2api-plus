package mixins

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"

	gen "github.com/LuckyKuang/sub2api-plus/ent"
	"github.com/LuckyKuang/sub2api-plus/ent/hook"
	"github.com/LuckyKuang/sub2api-plus/internal/domain"

	"entgo.io/ent"
	"entgo.io/ent/schema/mixin"
)

// federationOutboxEnabled 是整个 outbox 机制的总开关，默认关闭：未参与联邦的
// 部署（包括海外镜像站）不写任何 federation_outbox_events 行，也不付出额外查询。
// 由 cmd/server/main.go 按 federation.outbox_enabled 在启动时设置；ent schema
// 包读不到 viper 配置，所以用包级变量而不是依赖注入。
var federationOutboxEnabled atomic.Bool

// SetFederationOutboxEnabled 设置 outbox 总开关（启动时调用一次；测试里配合 t.Cleanup 复位）。
func SetFederationOutboxEnabled(enabled bool) { federationOutboxEnabled.Store(enabled) }

// FederationOutboxEnabled 报告 outbox 总开关是否打开。本 hook 与
// internal/repository 里绕过 hook 的余额写入路径共用这一个开关。
func FederationOutboxEnabled() bool { return federationOutboxEnabled.Load() }

// FederationOutboxMixin 在 User 创建、或身份字段(email/status/password_hash)
// 变更时，向 federation_outbox_events 写入一条 "user.upsert" 事件（payload 带
// bcrypt password_hash，海外原样写入，使同一密码两边可登录）。管理员账号
// (role=admin) 从不产生联邦事件：两边管理员邮箱可能相同，同步会覆盖对端管理员。
//
// 铁律：
//   - 只写 outbox，绝不在此发起网络调用；跨境投递交给外部 pusher。
//   - 写入与触发它的业务变更同一事务：业务提交则事件必达，业务回滚则事件不产生。
//
// 只挂 OpCreate | OpUpdateOne，不挂批量 OpUpdate：批量更新
// (`client.User.Update().Where(...)`) 的 mutation 结果是受影响行数，不是实体本身，
// 拿不到变更后的 email/status 去生成 payload。当前代码库里会改 email/status 的调用
// 都经过 UpdateOneID（见 user_repo.go），批量 Update 只用于 balance/concurrency 这类
// 字段，所以这个缺口目前是安全的——但以后如果新增了批量改 email/status 的路径，
// 这里不会捕获到，需要另外处理。批量或原生 SQL 改 balance 的路径由
// internal/repository 自己写 balance.snapshot（user_repo_federation_balance.go）。
// balance 既认 SetBalance 也认 AddBalance（UpdateOneID(...).AddBalance）。
//
// 使用示例：
//
//	func (User) Mixin() []ent.Mixin {
//	    return []ent.Mixin{
//	        mixins.TimeMixin{},
//	        mixins.SoftDeleteMixin{},
//	        mixins.FederationOutboxMixin{},
//	    }
//	}
type FederationOutboxMixin struct {
	mixin.Schema
}

func (FederationOutboxMixin) Hooks() []ent.Hook {
	return []ent.Hook{
		hook.On(func(next ent.Mutator) ent.Mutator {
			return hook.UserFunc(func(ctx context.Context, m *gen.UserMutation) (ent.Value, error) {
				if !FederationOutboxEnabled() {
					return next.Mutate(ctx, m)
				}
				v, err := next.Mutate(ctx, m)
				if err != nil {
					return v, err
				}

				emitIdentity := m.Op() == ent.OpCreate || (m.Op() == ent.OpUpdateOne && identityChanged(m))
				_, setBalance := m.Balance()
				_, addedBalance := m.AddedBalance()
				emitBalance := setBalance || addedBalance
				if !emitIdentity && !emitBalance {
					return v, nil
				}

				u, ok := v.(*gen.User)
				if !ok {
					return v, fmt.Errorf("federation outbox: unexpected mutation result type %T", v)
				}
				if u.Role == domain.RoleAdmin {
					return v, nil
				}
				client := m.Client()
				if emitIdentity {
					if err := EmitUserUpsertOutbox(ctx, client, u); err != nil {
						return v, fmt.Errorf("federation outbox: emit user.upsert: %w", err)
					}
				}
				if emitBalance {
					if err := EmitBalanceSnapshotOutbox(ctx, client, u); err != nil {
						return v, fmt.Errorf("federation outbox: emit balance.snapshot: %w", err)
					}
				}
				return v, nil
			})
		}, ent.OpCreate|ent.OpUpdateOne),
	}
}

// identityChanged 判断本次 mutation 是否设置了 email、status 或 password_hash 字段。用 Get(值,
// ok) 是否 ok 而不是新旧值比较——跟 mixin 设计草案里 m.Balance() 的判断方式一致：
// 字段被显式 Set 就发事件，哪怕设成了跟原值相同的值，代价是极少数误发，换来实现简单。
func identityChanged(m *gen.UserMutation) bool {
	if _, ok := m.Email(); ok {
		return true
	}
	if _, ok := m.Status(); ok {
		return true
	}
	if _, ok := m.PasswordHash(); ok {
		return true
	}
	return false
}

type federationUserPayload struct {
	ID           int64  `json:"id"`
	Email        string `json:"email"`
	Status       string `json:"status"`
	PasswordHash string `json:"password_hash,omitempty"`
}

// EmitUserUpsertOutbox writes one user.upsert event (id, email, status,
// password_hash) through client. Used by the hook and by the pusher's
// backfill-users command, so both produce the same wire format.
func EmitUserUpsertOutbox(ctx context.Context, client *gen.Client, u *gen.User) error {
	payload, err := json.Marshal(federationUserPayload{
		ID:           u.ID,
		Email:        u.Email,
		Status:       u.Status,
		PasswordHash: u.PasswordHash,
	})
	if err != nil {
		return err
	}
	return client.FederationOutbox.Create().
		SetAggregateType("user").
		SetAggregateID(fmt.Sprintf("%d", u.ID)).
		SetEventType("user.upsert").
		SetPayload(string(payload)).
		Exec(ctx)
}

type federationBalancePayload struct {
	ID           int64   `json:"id"`
	Email        string  `json:"email"`
	Balance      float64 `json:"balance"`
	AsOfUsageSeq int64   `json:"as_of_usage_seq"`
}

// EmitBalanceSnapshotOutbox reads the watermark off the same user row this
// mutation just wrote. Note this can lag by one delivered usage row: the
// tailer bumps the watermark via a *separate* admin call after the balance
// subtract that triggers this hook, so the snapshot emitted here may still
// carry the watermark from before that row. That's fine -- see
// openspec/changes/federation-balance-sync/design.md: a stale-low watermark
// only makes the receiving side more conservative, never causes
// over-admission.
func EmitBalanceSnapshotOutbox(ctx context.Context, client *gen.Client, u *gen.User) error {
	payload, err := json.Marshal(federationBalancePayload{
		ID:           u.ID,
		Email:        u.Email,
		Balance:      u.Balance,
		AsOfUsageSeq: u.FederationUsageWatermarkSeq,
	})
	if err != nil {
		return err
	}
	return client.FederationOutbox.Create().
		SetAggregateType("user").
		SetAggregateID(fmt.Sprintf("%d", u.ID)).
		SetEventType("balance.snapshot").
		SetPayload(string(payload)).
		Exec(ctx)
}
