//go:build integration

package repository

import (
	"encoding/json"
	"fmt"

	"github.com/LuckyKuang/sub2api-plus/ent/federationoutbox"
	"github.com/LuckyKuang/sub2api-plus/ent/schema/mixins"
	"github.com/LuckyKuang/sub2api-plus/internal/service"
)

// enableFederationOutbox turns on the federation.outbox_enabled gate for the
// current test method only (s.T() is the per-method *testing.T).
func (s *UserRepoSuite) enableFederationOutbox() {
	mixins.SetFederationOutboxEnabled(true)
	s.T().Cleanup(func() { mixins.SetFederationOutboxEnabled(false) })
}

// federationBalanceSnapshotEvents returns this user's balance.snapshot
// outbox rows in insertion order, for asserting on the payload the
// real (non-SQLite-testable) raw-SQL/bulk-update balance paths emit --
// see user_repo_federation_balance.go's doc comment for why these four
// specifically need explicit emission rather than relying on
// FederationOutboxMixin's ent hook.
func (s *UserRepoSuite) federationBalanceSnapshotEvents(userID int64) []federationBalancePayload {
	s.T().Helper()
	rows, err := s.client.FederationOutbox.Query().
		Where(
			federationoutbox.AggregateTypeEQ("user"),
			federationoutbox.AggregateIDEQ(fmt.Sprintf("%d", userID)),
			federationoutbox.EventTypeEQ("balance.snapshot"),
		).
		All(s.ctx)
	s.Require().NoError(err)

	out := make([]federationBalancePayload, 0, len(rows))
	for _, row := range rows {
		var payload federationBalancePayload
		s.Require().NoError(json.Unmarshal([]byte(row.Payload), &payload))
		out = append(out, payload)
	}
	return out
}

// User.Create() itself always emits one balance.snapshot too (see
// federation_outbox_hook_unit_test.go's TestFederationOutbox_EmitsOnUserCreate:
// Balance defaults to 0, which ent's mutation reports as "set" regardless of
// what mustCreateUser's caller passed). Every test below accounts for that
// baseline event rather than expecting a clean slate.

func (s *UserRepoSuite) TestAdjustBalance_EmitsFederationBalanceSnapshot() {
	s.enableFederationOutbox()
	user := s.mustCreateUser(&service.User{Email: "federation-adjust@test.com", Balance: 10})

	_, err := s.repo.AdjustBalance(s.ctx, user.ID, -3)
	s.Require().NoError(err)

	events := s.federationBalanceSnapshotEvents(user.ID)
	s.Require().Len(events, 2, "one from User.Create(), one explicit (AdjustBalance bypasses the ent hook -- raw SQL)")
	latest := events[len(events)-1]
	s.Require().InDelta(7.0, latest.Balance, 1e-6)
	s.Require().Equal("federation-adjust@test.com", latest.Email)
}

func (s *UserRepoSuite) TestAdjustBalance_RejectedNegativeDoesNotEmit() {
	s.enableFederationOutbox()
	user := s.mustCreateUser(&service.User{Email: "federation-adjust-reject@test.com", Balance: 5})
	baseline := len(s.federationBalanceSnapshotEvents(user.ID))

	_, err := s.repo.AdjustBalance(s.ctx, user.ID, -999)
	s.Require().ErrorIs(err, service.ErrBalanceNegative)

	s.Require().Len(s.federationBalanceSnapshotEvents(user.ID), baseline, "a rejected adjustment must not emit a snapshot of a balance that never took effect")
}

func (s *UserRepoSuite) TestSetBalance_EmitsFederationBalanceSnapshot() {
	s.enableFederationOutbox()
	user := s.mustCreateUser(&service.User{Email: "federation-set@test.com", Balance: 10})

	_, err := s.repo.SetBalance(s.ctx, user.ID, 42.5)
	s.Require().NoError(err)

	events := s.federationBalanceSnapshotEvents(user.ID)
	s.Require().Len(events, 2)
	s.Require().InDelta(42.5, events[len(events)-1].Balance, 1e-6)
}

func (s *UserRepoSuite) TestDeductBalance_EmitsFederationBalanceSnapshot() {
	s.enableFederationOutbox()
	user := s.mustCreateUser(&service.User{Email: "federation-deduct@test.com", Balance: 10})

	s.Require().NoError(s.repo.DeductBalance(s.ctx, user.ID, 5))

	events := s.federationBalanceSnapshotEvents(user.ID)
	s.Require().Len(events, 2, "one from User.Create(), one explicit (DeductBalance is ent's bulk Update, which the hook's op filter doesn't cover)")
	s.Require().InDelta(5.0, events[len(events)-1].Balance, 1e-6)
}

func (s *UserRepoSuite) TestUpdateBalance_EmitsFederationBalanceSnapshot() {
	s.enableFederationOutbox()
	user := s.mustCreateUser(&service.User{Email: "federation-update-balance@test.com", Balance: 10})

	s.Require().NoError(s.repo.UpdateBalance(s.ctx, user.ID, 2.5))

	events := s.federationBalanceSnapshotEvents(user.ID)
	s.Require().Len(events, 2, "one from User.Create(), one explicit (UpdateBalance is ent's bulk Update)")
	s.Require().InDelta(12.5, events[len(events)-1].Balance, 1e-6)
}

func (s *UserRepoSuite) TestApplyRedeemBalanceAdjustment_EmitsFederationBalanceSnapshot() {
	s.enableFederationOutbox()
	user := s.mustCreateUser(&service.User{Email: "federation-redeem@test.com", Balance: 10})

	s.Require().NoError(s.repo.ApplyRedeemBalanceAdjustment(s.ctx, user.ID, 2))

	events := s.federationBalanceSnapshotEvents(user.ID)
	s.Require().Len(events, 2, "one from User.Create(), one explicit (redeem uses raw SQL)")
	s.Require().InDelta(12.0, events[len(events)-1].Balance, 1e-6)
}

func (s *UserRepoSuite) TestDeductBalance_OverdraftPathAlsoEmits() {
	s.enableFederationOutbox()
	user := s.mustCreateUser(&service.User{Email: "federation-deduct-overdraft@test.com", Balance: 5})

	// 透支路径：先走 guarded Update 失败(0 行)，再走 unguarded Update 成功。
	s.Require().NoError(s.repo.DeductBalance(s.ctx, user.ID, 999))

	events := s.federationBalanceSnapshotEvents(user.ID)
	s.Require().Len(events, 2, "the overdraft fallback branch must also emit, not just the guarded happy path")
	s.Require().InDelta(-994.0, events[len(events)-1].Balance, 1e-6)
}

func (s *UserRepoSuite) TestDeductAvailableBalance_EmitsFederationBalanceSnapshot() {
	s.enableFederationOutbox()
	user := s.mustCreateUser(&service.User{Email: "federation-deduct-available@test.com", Balance: 10})

	deducted, err := s.repo.DeductAvailableBalance(s.ctx, user.ID, 25)
	s.Require().NoError(err)
	s.Require().InDelta(10.0, deducted, 1e-6)

	events := s.federationBalanceSnapshotEvents(user.ID)
	s.Require().Len(events, 2, "one from User.Create(), one explicit (DeductAvailableBalance is raw SQL)")
	latest := events[len(events)-1]
	s.Require().InDelta(0.0, latest.Balance, 1e-6)
	s.Require().Equal("federation-deduct-available@test.com", latest.Email)
}

func (s *UserRepoSuite) TestDeductAvailableBalance_ZeroDeductionDoesNotEmit() {
	s.enableFederationOutbox()
	user := s.mustCreateUser(&service.User{Email: "federation-deduct-available-zero@test.com", Balance: 0})
	baseline := len(s.federationBalanceSnapshotEvents(user.ID))

	deducted, err := s.repo.DeductAvailableBalance(s.ctx, user.ID, 5)
	s.Require().NoError(err)
	s.Require().Zero(deducted)

	s.Require().Len(s.federationBalanceSnapshotEvents(user.ID), baseline, "an unchanged balance must not emit a snapshot")
}

func (s *UserRepoSuite) TestFederationOutboxDisabled_BalancePathsEmitNothing() {
	user := s.mustCreateUser(&service.User{Email: "federation-gate-off@test.com", Balance: 10})

	_, err := s.repo.AdjustBalance(s.ctx, user.ID, -1)
	s.Require().NoError(err)
	_, err = s.repo.SetBalance(s.ctx, user.ID, 8)
	s.Require().NoError(err)
	s.Require().NoError(s.repo.DeductBalance(s.ctx, user.ID, 1))
	_, err = s.repo.DeductAvailableBalance(s.ctx, user.ID, 1)
	s.Require().NoError(err)

	count, err := s.client.FederationOutbox.Query().
		Where(federationoutbox.AggregateIDEQ(fmt.Sprintf("%d", user.ID))).
		Count(s.ctx)
	s.Require().NoError(err)
	s.Require().Zero(count, "federation.outbox_enabled=false must not write any outbox row, including User.Create()'s")
}

func (s *UserRepoSuite) TestFederationBalanceSnapshot_AdminBalancesAreFederated() {
	s.enableFederationOutbox()
	admin := s.mustCreateUser(&service.User{Email: "federation-admin-balance@test.com", Role: service.RoleAdmin, Balance: 10})

	_, err := s.repo.AdjustBalance(s.ctx, admin.ID, -1)
	s.Require().NoError(err)
	_, err = s.repo.SetBalance(s.ctx, admin.ID, 8)
	s.Require().NoError(err)
	s.Require().NoError(s.repo.DeductBalance(s.ctx, admin.ID, 1))
	_, err = s.repo.DeductAvailableBalance(s.ctx, admin.ID, 1)
	s.Require().NoError(err)

	events := s.federationBalanceSnapshotEvents(admin.ID)
	s.Require().NotEmpty(events, "admin balances are shared with the overseas mirror")
	s.Require().InDelta(6, events[len(events)-1].Balance, 0.000001)

	upserts, err := s.client.FederationOutbox.Query().
		Where(federationoutbox.AggregateIDEQ(fmt.Sprintf("%d", admin.ID)), federationoutbox.EventTypeEQ("user.upsert")).
		Count(s.ctx)
	s.Require().NoError(err)
	s.Require().Zero(upserts, "admin identity is never federated")
}
