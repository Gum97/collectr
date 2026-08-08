package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/collectr/collectr/internal/modules/iam/domain"
	"github.com/collectr/collectr/internal/platform/postgres"
)

// RegisterResetAttempt counts a reset request and reports whether it is allowed.
func (s *Store) RegisterResetAttempt(ctx context.Context, email string, limit int, window time.Duration) (bool, error) {
	windowStart := time.Now().UTC().Truncate(window)

	const q = `
		INSERT INTO iam.password_reset_attempts (email, window_start, attempts)
		VALUES ($1, $2, 1)
		ON CONFLICT (email, window_start) DO UPDATE
			SET attempts = iam.password_reset_attempts.attempts + 1
		RETURNING attempts`

	var attempts int
	if err := s.db.QueryRow(ctx, q, email, windowStart).Scan(&attempts); err != nil {
		return false, fmt.Errorf("registering reset attempt: %w", err)
	}
	return attempts <= limit, nil
}

// CreateResetToken issues a password reset token.
//
// Any outstanding token for the same account is invalidated first. Several live
// links would mean an old one, perhaps in a mailbox someone else now reads, still
// works after the owner has already used a newer one.
func (s *Store) CreateResetToken(ctx context.Context, userID uuid.UUID, raw, ipPrefix string, ttl time.Duration) error {
	err := s.db.InTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`UPDATE iam.password_reset_tokens SET used_at = now()
			 WHERE user_id = $1 AND used_at IS NULL`, userID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO iam.password_reset_tokens
				(id, user_id, token_hash, requested_ip_prefix, expires_at)
			VALUES ($1, $2, $3, $4, $5)`,
			uuid.New(), userID, Hash(raw), nullable(ipPrefix), time.Now().UTC().Add(ttl))
		return err
	})
	if err != nil {
		return fmt.Errorf("creating reset token: %w", err)
	}
	return nil
}

// ResetTarget is the account a reset token belongs to.
type ResetTarget struct {
	TokenID    uuid.UUID
	UserID     uuid.UUID
	Email      string
	MFAEnabled bool
	MFASecret  []byte
}

// FindResetToken resolves a token without consuming it.
func (s *Store) FindResetToken(ctx context.Context, raw string) (ResetTarget, error) {
	const q = `
		SELECT t.id, u.id, u.email, u.mfa_enabled, u.mfa_secret_enc
		FROM iam.password_reset_tokens t
		JOIN iam.users u ON u.id = t.user_id
		WHERE t.token_hash = $1 AND t.used_at IS NULL AND t.expires_at > now()
		  AND u.status = 'active'`

	var target ResetTarget
	err := s.db.QueryRow(ctx, q, Hash(raw)).Scan(
		&target.TokenID, &target.UserID, &target.Email, &target.MFAEnabled, &target.MFASecret)
	if postgres.IsNoRows(err) {
		return ResetTarget{}, domain.ErrNotFound
	}
	if err != nil {
		return ResetTarget{}, fmt.Errorf("finding reset token: %w", err)
	}
	return target, nil
}

// ConsumeResetToken sets a new password and closes every session.
//
// All three happen together: consuming the token, changing the password, and
// revoking sessions. A reset that left the attacker's session alive would defeat
// the point of resetting, and one that changed the password without consuming the
// token would leave the link reusable.
func (s *Store) ConsumeResetToken(ctx context.Context, tokenID, userID uuid.UUID, passwordHash string) error {
	err := s.db.InTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE iam.password_reset_tokens SET used_at = now()
			 WHERE id = $1 AND used_at IS NULL`, tokenID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			// Someone else -- or a duplicate submit -- got there first.
			return domain.ErrNotFound
		}

		if _, err := tx.Exec(ctx,
			`UPDATE iam.users SET password_hash = $2 WHERE id = $1`, userID, passwordHash); err != nil {
			return err
		}
		_, err = tx.Exec(ctx,
			`UPDATE iam.sessions SET revoked_at = now(), revoked_reason = 'password reset'
			 WHERE user_id = $1 AND revoked_at IS NULL`, userID)
		return err
	})
	if err != nil {
		return fmt.Errorf("completing password reset: %w", err)
	}
	return nil
}

// ChangePassword updates a signed-in user's password and ends their other
// sessions, leaving the one they are using.
func (s *Store) ChangePassword(ctx context.Context, userID, keepSessionID uuid.UUID, passwordHash string) error {
	err := s.db.InTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`UPDATE iam.users SET password_hash = $2 WHERE id = $1`, userID, passwordHash); err != nil {
			return err
		}
		_, err := tx.Exec(ctx,
			`UPDATE iam.sessions SET revoked_at = now(), revoked_reason = 'password changed'
			 WHERE user_id = $1 AND id <> $2 AND revoked_at IS NULL`, userID, keepSessionID)
		return err
	})
	if err != nil {
		return fmt.Errorf("changing password: %w", err)
	}
	return nil
}

// StoreRecoveryCodes replaces a user's MFA recovery codes.
func (s *Store) StoreRecoveryCodes(ctx context.Context, userID uuid.UUID, hashes [][]byte) error {
	err := s.db.InTx(ctx, func(tx pgx.Tx) error {
		// Regenerating invalidates the old set: a printout from two years ago
		// should stop working the moment a new one is issued.
		if _, err := tx.Exec(ctx,
			`DELETE FROM iam.mfa_recovery_codes WHERE user_id = $1`, userID); err != nil {
			return err
		}
		for _, h := range hashes {
			if _, err := tx.Exec(ctx,
				`INSERT INTO iam.mfa_recovery_codes (id, user_id, code_hash) VALUES ($1, $2, $3)`,
				uuid.New(), userID, h); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("storing recovery codes: %w", err)
	}
	return nil
}

// ConsumeRecoveryCode spends one recovery code, reporting whether it was valid.
func (s *Store) ConsumeRecoveryCode(ctx context.Context, userID uuid.UUID, codeHash []byte) (bool, error) {
	// Conditional on being unused, so the same code cannot be spent twice by two
	// concurrent attempts.
	tag, err := s.db.Exec(ctx, `
		UPDATE iam.mfa_recovery_codes SET used_at = now()
		WHERE user_id = $1 AND code_hash = $2 AND used_at IS NULL`, userID, codeHash)
	if err != nil {
		return false, fmt.Errorf("consuming recovery code: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// RemainingRecoveryCodes counts the unused codes.
func (s *Store) RemainingRecoveryCodes(ctx context.Context, userID uuid.UUID) (int, error) {
	var n int
	if err := s.db.QueryRow(ctx,
		`SELECT count(*) FROM iam.mfa_recovery_codes WHERE user_id = $1 AND used_at IS NULL`,
		userID).Scan(&n); err != nil {
		return 0, fmt.Errorf("counting recovery codes: %w", err)
	}
	return n, nil
}

// PurgeExpiredResetTokens clears spent and stale reset tokens.
func (s *Store) PurgeExpiredResetTokens(ctx context.Context, olderThan time.Duration) (int64, error) {
	cutoff := time.Now().UTC().Add(-olderThan)
	tag, err := s.db.Exec(ctx,
		`DELETE FROM iam.password_reset_tokens WHERE expires_at < $1 OR used_at < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("purging reset tokens: %w", err)
	}
	if _, err := s.db.Exec(ctx,
		`DELETE FROM iam.password_reset_attempts WHERE window_start < $1`, cutoff); err != nil {
		return 0, fmt.Errorf("purging reset attempts: %w", err)
	}
	return tag.RowsAffected(), nil
}

// DeleteRecoveryCodes removes every unused code for a user.
//
// Called when the second factor is switched off: codes are a way past that
// factor, so leaving them behind would keep a bypass alive for a protection
// that no longer exists.
func (s *Store) DeleteRecoveryCodes(ctx context.Context, userID uuid.UUID) error {
	if _, err := s.db.Exec(ctx,
		`DELETE FROM iam.mfa_recovery_codes WHERE user_id = $1`, userID); err != nil {
		return fmt.Errorf("deleting recovery codes: %w", err)
	}
	return nil
}
